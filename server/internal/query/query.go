// Package query implementa a Query API: seleção adaptativa de tabela (bruto/1m/1h)
// pela janela e step, e resposta colunar compacta.
package query

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/chquery"
)

type Handler struct{ ch *chquery.Client }

func NewHandler(ch *chquery.Client) *Handler { return &Handler{ch: ch} }

// Request é o corpo de POST /api/query.
type Request struct {
	Metric  string            `json:"metric"`
	Tenant  string            `json:"tenant"`
	Filters map[string]string `json:"filters"`  // labels exatos
	GroupBy []string          `json:"group_by"` // chaves de label para agrupar (ex.: ["host"] = 1 série/host)
	From    string            `json:"from"`     // RFC3339
	To      string            `json:"to"`       // RFC3339
	Step    int               `json:"step"`     // segundos
	Agg     string            `json:"agg"`      // avg|min|max|sum|last

	// IncludePartial mantém na resposta o balde de tempo que ainda não fechou.
	// É INTERNO (`json:"-"`): nenhum cliente HTTP pode pedi-lo, porque um balde
	// aberto é sempre um número menor/instável que engana quem lê o gráfico.
	// Existe para as checagens de PRESENÇA — perguntas do tipo "esta série ainda
	// reporta?" (ver alerting.containerReporting), onde o valor não importa e
	// perder o balde aberto poderia fazer o servidor concluir "sumiu" por engano.
	IncludePartial bool `json:"-"`
}

// Series é uma série temporal (labels + valores alinhados ao vetor ts).
type Series struct {
	Labels map[string]string `json:"labels"`
	Values []*float64        `json:"values"`
}

// Response é a resposta colunar.
type Response struct {
	Metric string   `json:"metric"`
	Table  string   `json:"table"`
	TS     []int64  `json:"ts"` // epoch segundos
	Series []Series `json:"series"`
	// AggEfetivo é a agregação que REALMENTE foi calculada. Só difere da pedida
	// quando a pedida não existe na fonte disponível (hoje: `last` em janela longa,
	// que cai para média). Recusar a consulta quebraria painéis já salvos; devolver
	// outra estatística calada seria mentira. Devolver e DIZER é o meio-termo — a
	// tela mostra isso no rodapé do painel.
	AggEfetivo string `json:"agg_efetivo,omitempty"`
}

// aggLast é a agregação "último valor da janela". Fica separada das outras porque
// é a única que NÃO pode ser recalculada a partir de um rollup (ver tableFor).
const aggLast = "last"

// aggRate é a agregação para CONTADORES acumulados — bytes de rede, por exemplo.
// O valor cru de um contador só cresce, então plotá-lo direto desenha uma reta
// subindo até 10¹² e ninguém consegue responder "quanto de rede este servidor
// usou às 3h". A taxa responde: quanto o contador andou no intervalo, dividido
// pela duração do intervalo.
//
// A taxa é medida ENTRE baldes (ver rateSQL), não DENTRO de cada balde. A versão
// anterior fazia `greatest(max(value) - min(value), 0) / step`, e isso media só o
// trecho do contador coberto pelas amostras que caíram dentro do balde — jogando
// fora o pedaço entre a última amostra de um balde e a primeira do seguinte, e
// ainda assim dividindo pelo passo inteiro. Duas medições no ClickHouse de dev:
//
//   - viés sistemático PARA BAIXO em 12 de 12 baldes de 1 min do host `notebook-dev`
//     (coleta a cada 15 s), de −0,5% a −24,5% — às 20:27 o painel dizia 19.122,7 B/s
//     onde a taxa real era 23.969,3 B/s. Com 4 amostras por balde, 3 dos 4
//     intervalos ficam dentro do balde: perde-se ~1/4 do tráfego, sempre para menos.
//
//   - com UMA amostra por balde (agente em modo cron, step=60 — é como
//     `mail.exemplo.com.br` roda) `max - min` é a diferença de uma amostra para ela
//     mesma: EXATAMENTE ZERO, enquanto a taxa real era 190.138 B/s. E `emptiness.ts`
//     classifica 0 como "ok", então nem "sem dados" aparecia: a tela AFIRMAVA que o
//     servidor não trafega rede. O painel de fábrica (dashboards/starter.go, painel
//     "Rede — recebido") e a aba Rede do servidor usam exatamente este caminho.
//
// O `greatest(..., 0)` continua existindo para o RESET do contador (interface
// recriada, reboot): o delta fica negativo e vira 0 em vez de desenhar um vale
// abaixo de zero num gráfico de tráfego. Já a falta do ponto de referência anterior
// (primeiro balde da janela, ou balde depois de uma lacuna) vira NULL — LACUNA, não
// zero: "não dá para calcular" e "não trafegou nada" são afirmações diferentes.
const aggRate = "rate"

// maxLastWindowSeconds é o teto de janela do `last`, deliberadamente MUITO abaixo
// da retenção da tabela crua (7 dias, deploy/migrations/clickhouse/001_metrics.up.sql)
// — que é o outro limite, já que "o último valor" só existe onde a LINHA individual
// ainda existe. O motivo deste teto é custo de leitura, não retenção: `metrics` tem
// ORDER BY (tenant_id, metric, ts) — sem `host` na chave —, então filtrar por host
// não poda nada e a consulta varre todas as linhas daquela métrica no período.
//
// O amplificador é o painel ao vivo: server/internal/live/live.go re-consulta CADA
// painel assinado a cada 1 segundo. Um painel salvo com "Último" numa janela de 7
// dias, deixado aberto numa aba, viraria uma varredura de tabela crua de 7 dias por
// segundo — e o painel roda num servidor só. 24 horas cobre com folga o uso real
// ("qual é o valor agora") a 1/7 do custo.
const maxLastWindowSeconds = 86400

// --- Tetos de custo da consulta -------------------------------------------------
//
// Todos existem pelo mesmo motivo: até aqui o ÚNICO teto da Query API era
// maxGridBuckets (20 000), e ele limita a GRADE de saída — não o trabalho que o
// ClickHouse faz para produzi-la. O resultado era um DoS autenticado barato: qualquer
// usuário logado (ou uma TV, cujo token é aberto por decisão de produto) montava um
// corpo de requisição pequeno que custava gigabytes no banco.

// maxGroupByKeys limita quantas CHAVES de label podem entrar no group_by.
//
// Cada chave vira uma coluna no GROUP BY e um par no map() de saída. Medido em dev:
// group_by com 1500 chaves, janela de 39 dias, 5 requisições paralelas em
// /api/tv/query levaram o ClickHouse a 2,70 GiB de RSS e 256% de CPU, e deixaram 10
// consultas ainda vivas em system.processes depois de todas as cinco já terem
// respondido erro ao cliente. Com 2000 chaves uma única requisição estourou os 20 s
// do contexto.
//
// 8 cobre com folga o uso real: o front agrupa por 1 chave (host) na esmagadora
// maioria dos painéis, e 2–3 no pior caso (host+mount, host+container, host+device).
// Não existe pergunta de monitoramento que precise de nove dimensões simultâneas num
// gráfico — a partir daí ninguém lê a legenda.
const maxGroupByKeys = 8

// maxWindowFor é o teto de JANELA por tabela — e, como a tabela é escolhida pela
// agregação (tableFor), é na prática o teto de janela por agregação.
//
// Cada teto é a RETENÇÃO da própria tabela (deploy/migrations/clickhouse):
// metrics TTL 7 dias, metrics_1m TTL 90 dias, metrics_1h TTL 730 dias. Pedir mais do
// que a tabela guarda não devolve nenhum dado adicional — só manda o ClickHouse
// varrer um intervalo que ele sabe estar vazio, e monta uma grade de baldes
// proporcional ao absurdo pedido. O `last` tem um teto próprio e bem menor
// (maxLastWindowSeconds, 24 h), pelo motivo de custo descrito acima.
func maxWindowFor(table string) int {
	switch table {
	case "metrics_1h":
		return 730 * 86400
	case "metrics_1m":
		return 90 * 86400
	default: // metrics (crua)
		return 7 * 86400
	}
}

// maxSeriesRows é o teto de LINHAS que o SQL de séries pode devolver (séries ×
// baldes). Não é um teto de produto — é o ponto a partir do qual a resposta deixa de
// caber em qualquer tela e passa a ser só consumo de memória no servidor e no
// browser: 1 milhão de pontos são ~8 MB só de ponteiros na montagem da grade densa,
// antes de virar JSON.
//
// Ao ser ATINGIDO a consulta vira ERRO, não resposta truncada. Truncar seria a
// falha de sempre deste painel: devolver um gráfico que parece completo e termina
// cedo, sem dizer que terminou cedo.
const maxSeriesRows = 1000000

// queryBudget é o prazo máximo de UMA consulta de séries, aplicado dentro do
// QuerySeries — e não no handler — de propósito: assim ele vale para TODA porta de
// entrada de uma vez (POST /api/query, POST /api/tv/query, WebSocket ao vivo,
// avaliador de alertas), inclusive as que ficam fora deste pacote. A rota de TV, que
// passava o r.Context() pelado, era a que não tinha prazo nenhum: as 5 requisições
// paralelas medidas só pararam aos 30 s, no timeout do cliente HTTP do chquery.
// context.WithTimeout respeita um prazo menor já existente no contexto pai, então
// nenhum chamador com orçamento mais apertado é afrouxado por isto.
const queryBudget = 20 * time.Second

// pickTable escolhe bruto/1m/1h pela janela e step (downsampling adaptativo, §4.1).
func pickTable(rangeSeconds, step int) string {
	switch {
	case step >= 3600 || rangeSeconds > 90*86400:
		return "metrics_1h"
	case step >= 60 || rangeSeconds > 7*86400:
		return "metrics_1m"
	default:
		return "metrics"
	}
}

// tableFor escolhe a tabela levando a AGREGAÇÃO em conta, não só janela e step.
//
// Os rollups (metrics_1m/metrics_1h) guardam avg_state, min_val, max_val e cnt —
// não guardam o instante de cada amostra, então não há como extrair deles o
// "último valor" do balde. Até aqui `agg=last` caía no `default` do aggExpr e
// virava avgMerge: a tela oferecia "Último (last)" e devolvia a MÉDIA, sem aviso.
// Agora `last` é atendido pela tabela crua enquanto ela cobre a janela pedida; se
// a janela ultrapassa a retenção crua, a consulta é RECUSADA com mensagem clara —
// devolver outra estatística com o nome errado é pior que devolver erro.
// tableFor devolve a tabela a consultar e a agregação EFETIVA — que pode diferir da
// pedida quando a pedida não existe na fonte disponível.
func tableFor(rangeSeconds, step int, agg string) (tabela, aggEfetivo string) {
	if agg == aggLast && rangeSeconds > maxLastWindowSeconds {
		// Fora da janela do `last`, os rollups só têm média/mín/máx. Cai para média
		// em vez de recusar: existe painel salvo com "Último" e faixa de 30d, e ele
		// funcionava antes — recusar agora o apagaria da tela de quem não mexeu em
		// nada. Quem informa a troca é o rodapé do painel, via AggEfetivo.
		return pickTable(rangeSeconds, step), "avg"
	}
	if agg == aggLast {
		return "metrics", aggLast
	}
	return pickTable(rangeSeconds, step), agg
}

// aggExpr monta a expressão SQL da agregação.
//
// aggRate NÃO passa por aqui: a taxa entre baldes precisa de função de janela
// (lagInFrame) sobre o resultado já agrupado, o que não cabe numa expressão de
// SELECT. buildSQL desvia para rateSQL antes de chamar aggExpr — se algum dia
// alguém chamar aggExpr com "rate", cai no default (média) e o número sai errado
// sem avisar, por isso o desvio é a PRIMEIRA coisa que buildSQL faz.
func aggExpr(table, agg string) string {
	if table == "metrics" {
		switch agg {
		case "min":
			return "min(value)"
		case "max":
			return "max(value)"
		case "sum":
			return "sum(value)"
		case aggLast:
			// Último valor do balde = o `value` da linha de maior `ts`. argMax faz
			// exatamente isso numa passada, sem subconsulta.
			return "argMax(value, ts)"
		default:
			return "avg(value)"
		}
	}
	// Tabelas de rollup guardam estados/agregados por bucket (avg_state, min_val,
	// max_val, cnt). Como TODO caminho da UI usa step>=60, é SEMPRE um rollup que
	// atende — então o `sum` precisa estar certo aqui.
	switch agg {
	case "min":
		return "min(min_val)"
	case "max":
		return "max(max_val)"
	case "sum":
		// Soma REAL dos valores brutos. Cada linha de rollup guarda a média do seu
		// bucket (avg_state) e a contagem de amostras (cnt); média×contagem = soma
		// daquele bucket, e a soma disso reconstrói a soma total exata. Antes era
		// `sum(cnt)`, que devolvia a CONTAGEM de amostras (não a soma dos valores) —
		// distorcia painéis, Explore e regras de alerta com agregação "soma".
		return "sum(finalizeAggregation(avg_state) * cnt)"
	default:
		// `last` nunca chega aqui: tableFor já recusou a consulta antes de montar o
		// SQL. Sobram avg e agregações desconhecidas, que caem na média (o default
		// documentado da API).
		return "avgMerge(avg_state)"
	}
}

func (h *Handler) Query(w http.ResponseWriter, r *http.Request) {
	var req Request
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Metric == "" {
		http.Error(w, "requisição inválida (metric obrigatório)", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	resp, err := h.QuerySeries(ctx, req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, http.StatusOK, resp)
}

// QuerySeries executa a consulta e devolve a resposta colunar (reusável fora do HTTP).
func (h *Handler) QuerySeries(ctx context.Context, req Request) (Response, error) {
	if req.Metric == "" {
		return Response{}, fmt.Errorf("metric obrigatório")
	}
	if req.Tenant == "" {
		req.Tenant = "default"
	}
	from, err1 := time.Parse(time.RFC3339, req.From)
	to, err2 := time.Parse(time.RFC3339, req.To)
	if err1 != nil || err2 != nil || !to.After(from) {
		return Response{}, fmt.Errorf("from/to inválidos (RFC3339, to>from)")
	}
	if req.Step <= 0 {
		req.Step = 60
	}
	// O `to` vem do RELÓGIO DO CLIENTE — as cinco telas de gráfico mandam
	// `new Date()` do navegador — e é ele que decide qual balde ainda está aberto.
	// Sem travar no relógio do servidor, um kiosk de TV sem NTP ou um laptop voltando
	// do sleep pedia um `to` no futuro e o balde ainda em formação voltava como valor
	// real; o selo de frescor aprovava, porque media a idade contra o mesmo relógio
	// torto. Medido em dev com 90 s de adiantamento: o painel entregou 4.886,0 B/s de
	// um balde que só fechava dali a 23 s, quando o balde fechado valia 11.142,5 B/s —
	// 2,3× de diferença numa tela que ninguém está olhando de perto.
	to = naoFuturo(to)
	// Teto de dimensões ANTES de qualquer trabalho: a validação é O(1) e o que ela
	// barra custa gigabytes no banco (ver maxGroupByKeys).
	if len(req.GroupBy) > maxGroupByKeys {
		return Response{}, fmt.Errorf("group_by com %d chaves: o máximo é %d (agrupe por menos dimensões)",
			len(req.GroupBy), maxGroupByKeys)
	}
	if !to.After(from) {
		// Faixa inteiramente no futuro depois do corte. Não é erro do usuário (o
		// pedido era válido no relógio dele) e não existe medida a devolver: resposta
		// VAZIA, que a tela desenha como "sem dados" — e não um gráfico inventado.
		tabela, _ := tableFor(0, req.Step, req.Agg)
		return Response{Metric: req.Metric, Table: tabela, TS: []int64{}, Series: []Series{}}, nil
	}
	janela := int(to.Sub(from).Seconds())
	table, aggEfetivo := tableFor(janela, req.Step, req.Agg)
	if teto := maxWindowFor(table); janela > teto {
		return Response{}, fmt.Errorf(
			"janela de %d dias excede o máximo de %d dias para a fonte %s (a retenção dessa tabela); reduza o período",
			janela/86400, teto/86400, table)
	}
	aggPedida := req.Agg
	req.Agg = aggEfetivo
	// Prazo próprio da consulta — vale para toda porta de entrada, inclusive a TV.
	ctx, cancel := context.WithTimeout(ctx, queryBudget)
	defer cancel()
	// Enforcement por usuário: se o contexto traz um escopo (chamada HTTP autenticada),
	// restringe aos hosts permitidos. Sem escopo (evaluator de alertas, TV pública) não
	// filtra. Admin devolve predicado vazio. O predicado usa labels['host'].
	hostPred := ""
	if scope, ok := authz.ScopeFrom(ctx); ok {
		hostPred = scope.HostPredicate("labels['host']")
	}
	sql, err := buildSQL(table, req, from, to, hostPred)
	if err != nil {
		return Response{}, err
	}
	rows, err := h.ch.QueryJSON(ctx, sql)
	if err != nil {
		return Response{}, fmt.Errorf("erro na consulta: %w", err)
	}
	// Bateu no LIMIT: a resposta seria um gráfico que termina cedo sem avisar. Erra.
	if len(rows) >= maxSeriesRows {
		return Response{}, fmt.Errorf(
			"a consulta devolveria mais de %d pontos: aumente o passo, reduza a janela ou filtre por menos séries",
			maxSeriesRows)
	}
	resp := shape(req.Metric, table, rows, from, to, req.Step, req.IncludePartial)
	if aggEfetivo != aggPedida {
		resp.AggEfetivo = aggEfetivo // a tela avisa no rodapé que a agregação trocou
	}
	return resp, nil
}

// buildSQL monta a consulta agregada por bucket de tempo e por conjunto de labels.
// hostPred, quando não vazio, é o predicado de escopo do usuário (ex.: labels['host'] IN
// (...) ou 1=0) — aplicado a TODA consulta, cobrindo dashboards, Explore e live num único
// ponto, sem depender de o handler lembrar de filtrar.
func buildSQL(table string, req Request, from, to time.Time, hostPred string) (string, error) {
	step := req.Step
	if step <= 0 {
		step = 60
	}
	var where []string
	where = append(where, fmt.Sprintf("tenant_id = %s", quote(req.Tenant)))
	where = append(where, fmt.Sprintf("metric = %s", quote(req.Metric)))
	// `from` alinhado PARA BAIXO no mesmo passo da grade. Sem isso a consulta e a
	// grade discordam sobre o primeiro balde: `bucketGrid` alinha para baixo (é o que
	// o `toStartOfInterval` faz), publica o balde que contém `from` — e o SQL, filtrando
	// pelo `from` cru, não entrega as linhas dele. O resultado é LACUNA ONDE HÁ MEDIDA,
	// que é o espelho exato do balde parcial da borda direita: lá o painel afirmava um
	// valor que não mediu, aqui ele nega um valor que mediu.
	//
	// No rollup dói mais: em `metrics_1m` o `ts` é o INÍCIO do minuto, então um `from`
	// às 13:59:09 descarta a linha das 13:59:00 inteira — sessenta segundos de medida
	// somem porque o pedido caiu nove segundos depois da virada do minuto.
	desde := from.Unix() / int64(step) * int64(step)
	// A taxa entre baldes precisa do balde ANTERIOR ao primeiro da grade para ter com
	// que comparar; sem ele, a borda esquerda de todo gráfico de rede nasceria como
	// lacuna. Lê-se um passo a mais e o SELECT externo descarta esse balde extra
	// (WHERE t >= desde), então a grade publicada não muda.
	leitura := desde
	if req.Agg == aggRate {
		leitura -= int64(step)
	}
	where = append(where, fmt.Sprintf("ts >= toDateTime(%d)", leitura))
	where = append(where, fmt.Sprintf("ts < toDateTime(%d)", to.Unix()))
	if hostPred != "" {
		where = append(where, hostPred)
	}
	for k, v := range req.Filters {
		if !safeLabel(k) {
			return "", fmt.Errorf("label inválido: %q", k)
		}
		where = append(where, fmt.Sprintf("labels[%s] = %s", quote(k), quote(v)))
	}
	// Para a taxa, o valor do balde é o ÚLTIMO do balde — e num contador acumulado o
	// último é o maior. Por isso `rate` agrega por `max` aqui e a divisão acontece
	// depois, no SELECT externo, entre um balde e o anterior (ver taxaEntreBaldes).
	aggInterno := req.Agg
	if aggInterno == aggRate {
		aggInterno = "max"
	}
	agg := aggExpr(table, aggInterno)
	// bucket de tempo pelo step pedido
	bucket := fmt.Sprintf("toStartOfInterval(ts, INTERVAL %d SECOND)", req.Step)

	// group_by por DIMENSÃO: quando informado, projetamos e agrupamos apenas pelas
	// chaves de label pedidas (uma série por combinação desses valores) em vez de pelo
	// conjunto COMPLETO de labels. Ex.: group_by=["host"] colapsa cpu-por-core numa
	// série por servidor. O `labels` de saída contém só as chaves de group_by, montado
	// como um Map do ClickHouse para o shape()/toLabels não precisarem mudar.
	if len(req.GroupBy) > 0 {
		// O ClickHouse 24.8 recusa `SELECT map('host', labels['host']) ... GROUP BY
		// labels['host']` (Code 215: o analisador não casa `labels['host']` dentro do
		// map com a chave de GROUP BY). Solução: agrupar num SUBQUERY por aliases
		// escalares seguros (g0, g1, …) e montar o Map só no nível externo — assim
		// nenhuma referência ao `labels` base sobra na projeção agregada. Funciona
		// igual na tabela bruta e nos rollups; o shape de saída (t, labels, v) não muda.
		mapPairs := make([]string, 0, len(req.GroupBy)*2)
		innerCols := make([]string, 0, len(req.GroupBy))
		groupCols := make([]string, 0, len(req.GroupBy))
		for i, k := range req.GroupBy {
			if !safeLabel(k) {
				return "", fmt.Errorf("group_by inválido: %q", k)
			}
			alias := fmt.Sprintf("g%d", i)
			innerCols = append(innerCols, fmt.Sprintf("labels[%s] AS %s", quote(k), alias))
			mapPairs = append(mapPairs, quote(k), alias)
			groupCols = append(groupCols, alias)
		}
		inner := fmt.Sprintf(
			"SELECT toUnixTimestamp(%s) AS t, %s, %s AS v FROM %s WHERE %s GROUP BY t, %s",
			bucket, strings.Join(innerCols, ", "), agg, table, strings.Join(where, " AND "), strings.Join(groupCols, ", "),
		)
		agrupado := fmt.Sprintf(
			"SELECT t, map(%s) AS labels, v FROM (%s)",
			strings.Join(mapPairs, ", "), inner,
		)
		if req.Agg == aggRate {
			return taxaEntreBaldes(agrupado, step, desde), nil
		}
		return agrupado + fmt.Sprintf(" ORDER BY t LIMIT %d", maxSeriesRows), nil
	}

	agrupado := fmt.Sprintf(
		"SELECT toUnixTimestamp(%s) AS t, labels, %s AS v FROM %s WHERE %s GROUP BY t, labels",
		bucket, agg, table, strings.Join(where, " AND "),
	)
	if req.Agg == aggRate {
		return taxaEntreBaldes(agrupado, step, desde), nil
	}
	return agrupado + fmt.Sprintf(" ORDER BY t LIMIT %d", maxSeriesRows), nil
}

// maxLacunaEmPassos é a distância máxima, em passos da grade, que ainda admite
// derivar uma taxa. Dois baldes contíguos dão a taxa do intervalo entre eles; com
// um buraco no meio, a taxa média sobre o buraco continua sendo uma medida real do
// contador. Acima disso não é mais medida — no meio de dez minutos sem amostra cabe
// uma reinicialização de máquina que zera o contador sem deixar rastro, e a divisão
// devolveria um número plausível e falso. Além do teto, o balde sai como LACUNA.
const maxLacunaEmPassos = 3

// taxaEntreBaldes transforma a série de um CONTADOR ACUMULADO na sua taxa.
//
// A versão anterior calculava `greatest(max−min, 0) / step` DENTRO de cada balde, e
// com isso ignorava o que o contador andou entre a última amostra de um balde e a
// primeira do seguinte. Medido em dev: viés para baixo em 12 de 12 baldes, de −0,5%
// a −24,5%. E com UMA amostra por balde — que é o estado permanente de um host em
// modo cron, onde `max == min` — o resultado era exatamente ZERO: a tela de rede
// afirmava, com uma reta em 0 B/s, que um servidor vivo não trafegava nada. Zero é
// uma afirmação, e essa era falsa.
//
// Aqui a taxa é a diferença entre o último valor de um balde e o do balde anterior,
// dividida pelo tempo REAL entre eles (não pelo passo presumido). O balde que não
// tem com o que se comparar não vira 0: some da resposta, e a grade densa o desenha
// como buraco. `cv >= pv` derruba o balde em que o contador andou para trás — que é
// reinício de interface ou de máquina, não tráfego negativo.
func taxaEntreBaldes(agrupado string, step int, desde int64) string {
	janela := "PARTITION BY labels ORDER BY t ROWS BETWEEN 1 PRECEDING AND CURRENT ROW"
	comAnterior := fmt.Sprintf(
		"SELECT t, labels, v AS cv, lagInFrame(v) OVER (%s) AS pv, lagInFrame(t) OVER (%s) AS pt FROM (%s)",
		janela, janela, agrupado,
	)
	// `t >= desde` descarta o balde extra que buildSQL mandou ler à esquerda: ele
	// existe só para dar comparação ao primeiro balde da grade e não é publicado.
	return fmt.Sprintf(
		"SELECT t, labels, (cv - pv) / (t - pt) AS v FROM (%s)"+
			" WHERE pt > 0 AND t > pt AND cv >= pv AND (t - pt) <= %d AND t >= %d"+
			" ORDER BY t LIMIT %d",
		comAnterior, maxLacunaEmPassos*step, desde, maxSeriesRows,
	)
}

// maxGridBuckets limita o tamanho da grade densa. 20 000 pontos por série já é
// mais do que qualquer tela consegue desenhar (um monitor 4K tem ~4 000 colunas),
// e cada ponto custa memória em TODAS as séries da resposta — 50 séries × 20 000
// pontos já são 1 milhão de ponteiros. Acima disso a grade é abandonada e a
// resposta volta a conter só os instantes que vieram do banco: a consulta continua
// funcionando (nada de erro na cara do operador), mas naquela resolução absurda as
// lacunas voltam a aparecer como reta. É um limite de proteção de memória, não de
// produto — nenhuma faixa oferecida pela UI chega perto dele.
// naoFuturo trava um instante no relógio do SERVIDOR.
//
// O `to` de toda consulta vem do relógio de quem pergunta, e é ele que decide qual
// balde ainda está aberto. Um kiosk de TV sem NTP adiantado em 90 s pedia um `to` no
// futuro, e o balde ainda em formação voltava como valor real — com o selo de frescor
// aprovando, porque media a idade contra o mesmo relógio torto. Não é hipótese: em dev
// o painel entregou 4.886,0 B/s de um balde que só fechava dali a 23 s, enquanto o
// balde fechado valia 11.142,5 B/s. O relógio do observador não pode decidir o que já
// foi medido.
func naoFuturo(t time.Time) time.Time {
	if agora := time.Now(); t.After(agora) {
		return agora
	}
	return t
}

const maxGridBuckets = 20000

// bucketGrid devolve a grade DENSA de inícios de balde entre `from` e `to`.
//
// Os baldes são alinhados ao RELÓGIO porque é assim que o ClickHouse agrupa
// (toStartOfInterval(ts, INTERVAL n SECOND) = intDiv(unix, n) * n); a grade tem de
// usar exatamente o mesmo alinhamento, senão nenhum ponto do banco cairia nela.
//
// O último balde é o último que TERMINA dentro de `to`. O balde que ainda está
// crescendo fica de fora: ele contém só os segundos decorridos desde a fronteira e
// por isso é sistematicamente menor que o real com agg=max/sum (o disco e a rede do
// mural terminavam num ponto rebaixado) e é ruído puro com agg=avg (uma regra de
// "média de 5 minutos" chegou a disparar lendo UMA amostra solta). `includePartial`
// só é usado por checagens de presença, que precisam do balde aberto.
//
// Devolve ok=false quando a grade passaria de maxGridBuckets.
func bucketGrid(from, to time.Time, step int, includePartial bool) ([]int64, bool) {
	if step <= 0 {
		return nil, false
	}
	s := int64(step)
	start := from.Unix() / s * s // mesmo alinhamento do toStartOfInterval
	// last = maior início de balde que a resposta pode conter.
	last := to.Unix() - s // balde fechado: início + step <= to
	if includePartial {
		last = (to.Unix() - 1) / s * s // balde que contém o instante final (ts < to)
	}
	if last < start {
		return nil, true // faixa menor que um balde: nenhum ponto, e isso é correto
	}
	n := (last-start)/s + 1
	if n > maxGridBuckets {
		return nil, false
	}
	grid := make([]int64, 0, n)
	for t := start; t <= last; t += s {
		grid = append(grid, t)
	}
	return grid, true
}

// shape transforma linhas (t, labels, v) em resposta colunar {ts, series}.
//
// Duas garantias que o formato antigo não dava:
//
//  1. Nenhum balde incompleto sai daqui (ver bucketGrid).
//  2. O eixo de tempo é a grade DENSA de baldes, não a lista de instantes que
//     apareceram no resultado. Antes, um `null` só nascia quando uma série faltava
//     num instante em que OUTRA série tinha valor — logo, num gráfico de série
//     ÚNICA (qualquer painel filtrado por um host) um agente fora do ar por 40
//     minutos não gerava buraco nenhum: chegavam dois pontos e o uPlot ligava os
//     dois com uma reta, afirmando que a CPU esteve estável durante uma queda
//     total. Com a grade densa, lacuna é lacuna com uma série ou com dez.
func shape(metric, table string, rows []map[string]any, from, to time.Time, step int, includePartial bool) Response {
	type key string
	seriesMap := map[key]map[int64]float64{}
	labelsOf := map[key]map[string]string{}
	observed := map[int64]struct{}{}

	grid, dense := bucketGrid(from, to, step, includePartial)
	// cutoff: último início de balde aceito. Vale também no caminho esparso (grade
	// grande demais), para o descarte do balde aberto nunca depender do limite.
	cutoff := to.Unix() - int64(step)
	if includePartial {
		cutoff = to.Unix() - 1
	}

	for _, row := range rows {
		t := toInt64(row["t"])
		if t > cutoff {
			continue // balde ainda crescendo: o valor dele mudaria no próximo segundo
		}
		v, ok := toFloat(row["v"])
		if !ok {
			continue // NaN/Inf/nulo não é medida: fica como lacuna, não como zero
		}
		lbls := toLabels(row["labels"])
		k := key(labelKey(lbls))
		observed[t] = struct{}{}
		if seriesMap[k] == nil {
			seriesMap[k] = map[int64]float64{}
			labelsOf[k] = lbls
		}
		seriesMap[k][t] = v
	}

	var ts []int64
	if dense {
		ts = grid
	} else {
		ts = make([]int64, 0, len(observed))
		for t := range observed {
			ts = append(ts, t)
		}
		sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
	}
	if ts == nil {
		ts = []int64{} // JSON com "ts": [] e não "ts": null (o front itera direto)
	}

	resp := Response{Metric: metric, Table: table, TS: ts, Series: []Series{}}
	keys := make([]key, 0, len(seriesMap))
	for k := range seriesMap {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		vals := make([]*float64, len(ts))
		for i, t := range ts {
			if v, ok := seriesMap[k][t]; ok {
				vv := v
				vals[i] = &vv
			}
		}
		resp.Series = append(resp.Series, Series{Labels: labelsOf[k], Values: vals})
	}
	return resp
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
