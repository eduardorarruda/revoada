package query

import (
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
)

// row monta uma linha como o ClickHouse devolve (t = início do balde em epoch).
func row(t int64, v any, labels map[string]any) map[string]any {
	return map[string]any{"t": float64(t), "v": v, "labels": labels}
}

func ts(s string) time.Time {
	tt, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return tt
}

// TestShapeDescartaBaldeParcial reproduz o incidente de produção de 07/08 às
// 18:05:12: uma regra "média de 5 minutos" (agg=avg, window=300) disparou lendo UMA
// amostra solta.
//
// A consulta agrupa em baldes alinhados ao relógio, mas from/to não são alinhados —
// então a faixa pedida cobria dois baldes e o último tinha só os 12 segundos
// decorridos desde a fronteira:
//
//	balde 18:00:00 -> 19 amostras -> 42,6
//	balde 18:05:00 ->  1 amostra  -> 100    <- o avaliador lia ESTE
//
// A assinatura em escala confirmou: 65 dos 67 disparos daquela regra começaram num
// minuto múltiplo de 5 (o acaso daria ~13) e 55 duraram um único ciclo de 30 s.
func TestShapeDescartaBaldeParcial(t *testing.T) {
	from, to := ts("2026-08-07T18:00:12Z"), ts("2026-08-07T18:05:12Z")
	rows := []map[string]any{
		row(ts("2026-08-07T18:00:00Z").Unix(), 42.6, map[string]any{"host": "srv1"}),
		row(ts("2026-08-07T18:05:00Z").Unix(), 100.0, map[string]any{"host": "srv1"}),
	}

	resp := shape("system.cpu.utilization", "metrics_1m", rows, from, to, 300, false)

	if len(resp.Series) != 1 {
		t.Fatalf("esperava 1 série, veio %d", len(resp.Series))
	}
	last := resp.Series[0].Values[len(resp.Series[0].Values)-1]
	if last == nil {
		t.Fatal("o último ponto deveria ser o balde FECHADO 18:00, veio nulo")
	}
	if *last == 100 {
		t.Fatal("o balde 18:05 (1 amostra, ainda crescendo) voltou para a resposta — o defeito ressuscitou")
	}
	if *last != 42.6 {
		t.Errorf("último ponto = %v; esperava 42.6 (a média real dos 5 minutos)", *last)
	}
	for _, tt := range resp.TS {
		if tt >= ts("2026-08-07T18:05:00Z").Unix() {
			t.Errorf("balde %d ainda não fechou às 18:05:12 e não pode aparecer", tt)
		}
	}
}

// TestShapeIncludePartial: a checagem de PRESENÇA (existe alguma amostra recente?)
// precisa enxergar o balde aberto. Sem isso, um container que só reportou depois da
// última fronteira de balde pareceria removido e o alerta seria resolvido por engano
// — notificação falsa de "resolvido" para um problema que continua.
func TestShapeIncludePartial(t *testing.T) {
	from, to := ts("2026-08-07T18:02:30Z"), ts("2026-08-07T18:05:12Z")
	rows := []map[string]any{
		row(ts("2026-08-07T18:05:00Z").Unix(), 1.0, map[string]any{"container": "web"}),
	}

	semParcial := shape("container.running", "metrics", rows, from, to, 30, false)
	comParcial := shape("container.running", "metrics", rows, from, to, 30, true)

	if temValor(semParcial) {
		t.Error("sem IncludePartial o balde aberto some — esperado para gráficos")
	}
	if !temValor(comParcial) {
		t.Fatal("com IncludePartial a amostra do balde aberto tem de sobreviver (presença)")
	}
}

func temValor(r Response) bool {
	for _, s := range r.Series {
		for _, v := range s.Values {
			if v != nil {
				return true
			}
		}
	}
	return false
}

// TestShapeGradeDensaSerieUnica é o defeito "lacuna vira linha reta": num painel
// filtrado por um host há UMA série, então nunca existia outra série para criar o
// null. O agente ficava 40 minutos fora, chegavam dois pontos e o uPlot ligava os
// dois com uma reta — o gráfico afirmava que a CPU esteve estável durante uma queda
// total.
func TestShapeGradeDensaSerieUnica(t *testing.T) {
	from, to := ts("2026-08-07T10:00:00Z"), ts("2026-08-07T11:00:00Z")
	rows := []map[string]any{
		row(ts("2026-08-07T10:00:00Z").Unix(), 30.0, map[string]any{"host": "srv1"}),
		row(ts("2026-08-07T10:50:00Z").Unix(), 35.0, map[string]any{"host": "srv1"}),
	}

	resp := shape("system.cpu.utilization", "metrics_1m", rows, from, to, 300, false)

	// Grade densa de 10:00 a 10:55 (o balde 11:00 fecharia depois de `to`).
	if len(resp.TS) != 12 {
		t.Fatalf("esperava 12 baldes de 5 min na grade, veio %d: %v", len(resp.TS), resp.TS)
	}
	vals := resp.Series[0].Values
	if len(vals) != len(resp.TS) {
		t.Fatalf("valores (%d) e ts (%d) precisam ter o mesmo tamanho", len(vals), len(resp.TS))
	}
	nulos := 0
	for _, v := range vals {
		if v == nil {
			nulos++
		}
	}
	if nulos != 10 {
		t.Errorf("a queda de 50 min deveria virar 10 lacunas, veio %d — o gráfico voltaria a desenhar reta", nulos)
	}
	if vals[0] == nil || *vals[0] != 30 || vals[10] == nil || *vals[10] != 35 {
		t.Errorf("os dois pontos reais deveriam ficar nas pontas da lacuna, veio %v", vals)
	}
}

// TestBucketGridAlinhamentoELimite trava as duas propriedades da grade: alinhamento
// idêntico ao toStartOfInterval do ClickHouse e teto de memória.
func TestBucketGridAlinhamentoELimite(t *testing.T) {
	// from desalinhado (18:00:12) tem de cair no balde 18:00:00 — é lá que o
	// ClickHouse põe a linha, e uma grade desalinhada não casaria com ponto nenhum.
	grid, ok := bucketGrid(ts("2026-08-07T18:00:12Z"), ts("2026-08-07T18:10:00Z"), 300, false)
	if !ok || len(grid) == 0 {
		t.Fatalf("grade inesperada: %v ok=%v", grid, ok)
	}
	if grid[0] != ts("2026-08-07T18:00:00Z").Unix() {
		t.Errorf("primeiro balde = %d; esperava 18:00:00 (alinhado ao relógio)", grid[0])
	}
	if last := grid[len(grid)-1]; last != ts("2026-08-07T18:05:00Z").Unix() {
		t.Errorf("último balde = %d; esperava 18:05:00 (o de 18:10 fecha em 18:15 > to)", last)
	}

	// Faixa menor que um balde: nenhum balde fechado, e isso é a resposta certa.
	if grid, ok := bucketGrid(ts("2026-08-07T18:00:00Z"), ts("2026-08-07T18:01:00Z"), 300, false); !ok || len(grid) != 0 {
		t.Errorf("faixa < step deveria render grade vazia, veio %v ok=%v", grid, ok)
	}

	// Teto de memória: 2 anos com step de 1 s desiste da grade (ok=false) em vez de
	// alocar dezenas de milhões de pontos.
	if _, ok := bucketGrid(ts("2024-01-01T00:00:00Z"), ts("2026-01-01T00:00:00Z"), 1, false); ok {
		t.Error("grade absurda deveria ser recusada (proteção de memória)")
	}
}

// TestShapeValorNaoFinito: NaN/Inf/null viram LACUNA, nunca zero. Zero numa série de
// utilização é a afirmação "o servidor estava ocioso", que o dado não sustenta.
func TestShapeValorNaoFinito(t *testing.T) {
	from, to := ts("2026-08-07T10:00:00Z"), ts("2026-08-07T10:15:00Z")
	rows := []map[string]any{
		row(ts("2026-08-07T10:00:00Z").Unix(), 30.0, map[string]any{"host": "srv1"}),
		row(ts("2026-08-07T10:05:00Z").Unix(), math.NaN(), map[string]any{"host": "srv1"}),
		row(ts("2026-08-07T10:10:00Z").Unix(), nil, map[string]any{"host": "srv1"}),
	}
	resp := shape("system.cpu.utilization", "metrics_1m", rows, from, to, 300, false)
	vals := resp.Series[0].Values
	for i, v := range vals {
		if i == 0 {
			continue
		}
		if v != nil {
			t.Errorf("posição %d deveria ser lacuna (valor não-finito), veio %v", i, *v)
		}
	}
}

// TestTableForLast prova que `agg=last` parou de virar média EM SILÊNCIO — e que,
// quando a janela não permite calculá-lo, ele degrada de forma declarada em vez de
// derrubar o painel.
func TestTableForLast(t *testing.T) {
	// Dentro da janela do last: atendido pela tabela crua, mesmo com step>=60 (que
	// normalmente levaria a um rollup), porque só lá existe a linha individual.
	tabela, efetiva := tableFor(3600, 300, aggLast)
	if tabela != "metrics" || efetiva != aggLast {
		t.Fatalf("last em janela curta deveria usar a tabela crua e continuar last, veio %q/%q", tabela, efetiva)
	}
	if expr := aggExpr(tabela, aggLast); expr != "argMax(value, ts)" {
		t.Errorf("last na tabela crua deveria ser argMax(value, ts), veio %q", expr)
	}

	// Além da janela, o rollup só tem média/mín/máx. Degrada para média E AVISA:
	// recusar apagaria da tela um painel salvo com "Último" em 30 dias, que
	// funcionava antes e cujo dono não mexeu em nada.
	tabela, efetiva = tableFor(30*86400, 3600, aggLast)
	if efetiva != "avg" {
		t.Errorf("last em janela longa deveria cair para média, veio %q", efetiva)
	}
	if tabela != "metrics_1h" {
		t.Errorf("janela de 30 dias com step 3600 deveria usar metrics_1h, veio %q", tabela)
	}

	// O limite é a janela do last, não a retenção crua.
	if _, efetiva := tableFor(maxLastWindowSeconds+1, 300, aggLast); efetiva != "avg" {
		t.Errorf("um segundo além do teto já deveria degradar, veio %q", efetiva)
	}
	if _, efetiva := tableFor(maxLastWindowSeconds, 300, aggLast); efetiva != aggLast {
		t.Errorf("exatamente no teto o last ainda vale, veio %q", efetiva)
	}

	// As outras agregações não mudam nem de tabela nem de nome.
	if tabela, efetiva := tableFor(3600, 300, "avg"); tabela != "metrics_1m" || efetiva != "avg" {
		t.Errorf("avg com step 300 deveria seguir em metrics_1m como avg, veio %q/%q", tabela, efetiva)
	}
}

func TestPickTable(t *testing.T) {
	cases := []struct {
		name               string
		rangeSeconds, step int
		want               string
	}{
		{"janela curta, step fino -> bruto", 3600, 10, "metrics"},
		{"step 60s -> 1m", 3600, 60, "metrics_1m"},
		{"janela > 7d -> 1m", 10 * 86400, 30, "metrics_1m"},
		{"step 1h -> 1h", 86400, 3600, "metrics_1h"},
		{"janela > 90d -> 1h", 120 * 86400, 30, "metrics_1h"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := pickTable(c.rangeSeconds, c.step); got != c.want {
				t.Errorf("pickTable(%d,%d)=%s, quer %s", c.rangeSeconds, c.step, got, c.want)
			}
		})
	}
}

func TestSafeLabelAndQuote(t *testing.T) {
	if safeLabel("host'; DROP") {
		t.Error("label com injeção passou")
	}
	if !safeLabel("host.name_1") {
		t.Error("label válido rejeitado")
	}
	if quote("a'b") != `'a\'b'` {
		t.Errorf("quote escapou errado: %s", quote("a'b"))
	}
}

func TestBuildSQLGroupBy(t *testing.T) {
	from := time.Unix(1000, 0).UTC()
	to := time.Unix(2000, 0).UTC()
	base := Request{Metric: "system.cpu.utilization", Tenant: "default", Step: 60, Agg: "avg"}

	t.Run("sem group_by mantém comportamento atual (agrupa por labels inteiro)", func(t *testing.T) {
		sql, err := buildSQL("metrics", base, from, to, "")
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if !strings.Contains(sql, "AS t, labels, avg(value) AS v") {
			t.Errorf("projeção antiga esperada, veio: %s", sql)
		}
		if !strings.Contains(sql, "GROUP BY t, labels ORDER BY t") {
			t.Errorf("GROUP BY antigo esperado, veio: %s", sql)
		}
		if strings.Contains(sql, "map(") {
			t.Errorf("não deveria projetar map() sem group_by: %s", sql)
		}
	})

	t.Run("group_by por host: subquery com alias escalar (SQL válido no ClickHouse)", func(t *testing.T) {
		req := base
		req.GroupBy = []string{"host"}
		sql, err := buildSQL("metrics", req, from, to, "")
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		// Novo formato: agrupa por alias escalar g0 no subquery, monta o Map fora.
		if !strings.Contains(sql, "labels['host'] AS g0") {
			t.Errorf("deveria projetar o label como alias escalar g0, veio: %s", sql)
		}
		if !strings.Contains(sql, "GROUP BY t, g0") {
			t.Errorf("deveria agrupar pelo alias g0, veio: %s", sql)
		}
		if !strings.Contains(sql, "map('host', g0) AS labels") {
			t.Errorf("o Map externo deveria referenciar o alias g0, veio: %s", sql)
		}
		// Anti-regressão: o formato antigo (map com labels['host']) era recusado pelo
		// ClickHouse 24.8 (Code 215) — não pode reaparecer.
		if strings.Contains(sql, "map('host', labels['host'])") {
			t.Errorf("formato antigo (bug ClickHouse 215) reintroduzido: %s", sql)
		}
	})

	t.Run("host predicate entra no WHERE (enforcement por usuário)", func(t *testing.T) {
		pred := "labels['host'] IN ('host-a')"
		sql, err := buildSQL("metrics", base, from, to, pred)
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if !strings.Contains(sql, pred) {
			t.Errorf("predicado de host deveria estar no WHERE, veio: %s", sql)
		}
		// Também no caminho group_by (o WHERE do subquery interno).
		req := base
		req.GroupBy = []string{"host"}
		sql2, err := buildSQL("metrics", req, from, to, pred)
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if !strings.Contains(sql2, pred) {
			t.Errorf("predicado de host deveria estar no subquery group_by, veio: %s", sql2)
		}
	})

	t.Run("group_by com várias chaves: um alias por chave", func(t *testing.T) {
		req := base
		req.GroupBy = []string{"host", "mountpoint"}
		sql, err := buildSQL("metrics", req, from, to, "")
		if err != nil {
			t.Fatalf("erro inesperado: %v", err)
		}
		if !strings.Contains(sql, "labels['host'] AS g0, labels['mountpoint'] AS g1") {
			t.Errorf("deveria projetar as duas chaves como g0/g1, veio: %s", sql)
		}
		if !strings.Contains(sql, "GROUP BY t, g0, g1") {
			t.Errorf("deveria agrupar por g0, g1, veio: %s", sql)
		}
		if !strings.Contains(sql, "map('host', g0, 'mountpoint', g1) AS labels") {
			t.Errorf("o Map externo deveria mapear as duas chaves aos aliases, veio: %s", sql)
		}
	})

	t.Run("chave de group_by com injeção é rejeitada", func(t *testing.T) {
		req := base
		req.GroupBy = []string{"host'; DROP"}
		if _, err := buildSQL("metrics", req, from, to, ""); err == nil {
			t.Error("group_by com injeção deveria falhar")
		}
	})
}

// Contador acumulado plotado direto é uma reta subindo até 10¹²: correta e inútil.
//
// A taxa é medida ENTRE baldes, não dentro de um. A versão anterior fazia
// `greatest(max−min,0)/step` confinado ao balde e jogava fora o que o contador andou
// da última amostra de um balde até a primeira do seguinte: viés para baixo em 12 de
// 12 baldes medidos (−0,5% a −24,5%) e, com UMA amostra por balde — o estado
// permanente de um host em modo cron —, zero absoluto num servidor que trafegava
// 190 KB/s. Este teste existe para essa conta não voltar.
func TestAggRateMedeEntreBaldes(t *testing.T) {
	from := time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC)
	to := from.Add(time.Hour)
	req := Request{Tenant: "default", Metric: "system.network.io.bytes_recv", Agg: aggRate, Step: 60}

	sql, err := buildSQL("metrics_1m", req, from, to, "")
	if err != nil {
		t.Fatalf("buildSQL: %v", err)
	}
	// A comparação com o balde anterior é o coração da correção.
	if !strings.Contains(sql, "lagInFrame(v) OVER") || !strings.Contains(sql, "lagInFrame(t) OVER") {
		t.Errorf("a taxa precisa comparar com o balde ANTERIOR (lagInFrame), veio: %s", sql)
	}
	// Dividir pelo tempo real entre os dois baldes, não pelo passo presumido: com um
	// buraco na série, `/step` inventaria uma taxa que não foi medida.
	if !strings.Contains(sql, "(cv - pv) / (t - pt)") {
		t.Errorf("a taxa tem de dividir pelo intervalo real entre os baldes, veio: %s", sql)
	}
	// Contador que anda para trás é reinício de máquina/interface — não é tráfego
	// negativo e não é zero. O balde sai da resposta e a grade o desenha como buraco.
	if !strings.Contains(sql, "cv >= pv") {
		t.Errorf("reinício de contador precisa virar LACUNA, não valor, veio: %s", sql)
	}
	// O primeiro balde da grade não tem antecessor dentro da janela pedida: buildSQL
	// lê um passo a mais à esquerda e o descarta na publicação.
	if !strings.Contains(sql, "AND t >= ") {
		t.Errorf("o balde extra lido à esquerda tem de ser descartado na saída, veio: %s", sql)
	}

	// A agregação interna é `max`: num contador acumulado, o maior valor do balde É o
	// último dele. Se isto virar `avg` algum dia, a taxa passa a comparar médias de
	// baldes e o número deixa de ser a taxa.
	if !strings.Contains(sql, "max(max_val)") {
		t.Errorf("no rollup, o valor do balde para taxa é max(max_val), veio: %s", sql)
	}
	if bruta, err := buildSQL("metrics", req, from, to, ""); err != nil {
		t.Fatalf("buildSQL bruta: %v", err)
	} else if !strings.Contains(bruta, "max(value)") {
		t.Errorf("na tabela crua, o valor do balde para taxa é max(value), veio: %s", bruta)
	}
}

// O `to` vem do relógio de QUEM PERGUNTA. Um kiosk de TV adiantado 90 s pedia um
// futuro e recebia o balde ainda em formação como se fosse medida fechada — 4.886 B/s
// no lugar de 11.142 B/s. O relógio do observador não decide o que já foi medido.
func TestToNaoPassaDoRelogioDoServidor(t *testing.T) {
	futuro := time.Now().Add(90 * time.Second)
	if got := naoFuturo(futuro); !got.Before(futuro) {
		t.Errorf("to no futuro deveria ser travado no agora, veio %v", got)
	}
	passado := time.Now().Add(-10 * time.Minute)
	if got := naoFuturo(passado); !got.Equal(passado) {
		t.Errorf("to no passado não pode ser mexido, veio %v", got)
	}
}

// --- Tetos de custo (DoS autenticado) -------------------------------------------

// TestGroupByTemTeto: o group_by era ilimitado e cada chave vira uma coluna de
// GROUP BY. Medido antes da correção: 1500 chaves × janela de 39 dias × 5 requisições
// paralelas = 2,70 GiB de RSS e 256% de CPU no ClickHouse; 2000 chaves estouravam o
// prazo de 20 s de uma requisição só. A recusa tem de ser BARATA — antes de tocar no
// banco — e a mensagem tem de dizer o teto.
func TestGroupByTemTeto(t *testing.T) {
	h := &Handler{} // sem cliente: se o teto falhar, o teste explode em nil pointer
	gb := make([]string, maxGroupByKeys+1)
	for i := range gb {
		gb[i] = "k"
	}
	_, err := h.QuerySeries(t.Context(), Request{
		Metric: "system.cpu.utilization", GroupBy: gb, Step: 60,
		From: "2026-08-07T18:00:00Z", To: "2026-08-07T19:00:00Z",
	})
	if err == nil {
		t.Fatal("group_by acima do teto deveria ser recusado antes de consultar o banco")
	}
	if !strings.Contains(err.Error(), "group_by") {
		t.Errorf("a mensagem precisa dizer qual limite foi atingido, veio %q", err)
	}
	if maxGroupByKeys < 3 {
		t.Error("o teto não pode ficar abaixo de host+mount+container, que é uso real")
	}
}

// TestJanelaTemTetoPorFonte: pedir mais do que a tabela RETÉM não devolve dado
// nenhum a mais — só manda o ClickHouse varrer um intervalo que ele sabe vazio e
// monta uma grade proporcional ao absurdo pedido.
func TestJanelaTemTetoPorFonte(t *testing.T) {
	// A crua raramente é alcançada com janela longa (pickTable já desvia para os
	// rollups acima de 7 dias), mas o teto fica como rede: `agg=last` força a crua.
	if maxWindowFor("metrics") != 7*86400 {
		t.Errorf("a tabela crua tem TTL de 7 dias; o teto tem de ser esse, veio %d", maxWindowFor("metrics"))
	}
	if maxWindowFor("metrics_1m") != 90*86400 {
		t.Errorf("metrics_1m tem TTL de 90 dias, veio %d", maxWindowFor("metrics_1m"))
	}
	if maxWindowFor("metrics_1h") != 730*86400 {
		t.Errorf("metrics_1h tem TTL de 730 dias, veio %d", maxWindowFor("metrics_1h"))
	}
	h := &Handler{} // sem cliente: se o teto falhar, o teste explode em nil pointer
	// Janela de 26 anos. Antes da correção isto respondia 200 sem piscar: o
	// pickTable mandava para metrics_1h e a consulta varria um intervalo 13 vezes
	// maior que tudo que a tabela guarda.
	_, err := h.QuerySeries(t.Context(), Request{
		Metric: "system.cpu.utilization", Step: 3600,
		From: "2000-01-01T00:00:00Z", To: "2026-08-09T00:00:00Z",
	})
	if err == nil {
		t.Fatal("janela de 26 anos (retenção de metrics_1h é 730 dias) deveria ser recusada")
	}
	if !strings.Contains(err.Error(), "janela") {
		t.Errorf("a mensagem precisa explicar que o problema é a janela, veio %q", err)
	}
}

// TestJanelasReaisPassam trava o outro lado do teto: as faixas que o produto oferece
// (até "1 ano" no dashboard) e o preview de alerta de 90 dias NÃO podem ser barrados.
func TestJanelasReaisPassam(t *testing.T) {
	casos := []struct {
		nome   string
		janela int
		step   int
	}{
		{"dashboard 24h", 86400, 60},
		{"dashboard 7d", 604800, 300},
		{"dashboard 90d", 7776000, 3600},
		{"dashboard 1 ano", 31536000, 21600},
		{"preview de alerta 90d", 90 * 86400, 300},
	}
	for _, c := range casos {
		tabela, _ := tableFor(c.janela, c.step, "avg")
		if teto := maxWindowFor(tabela); c.janela > teto {
			t.Errorf("%s: janela %ds cai em %s cujo teto é %ds — o produto quebraria", c.nome, c.janela, tabela, teto)
		}
	}
}

// TestBuildSQLTemLimit: sem LIMIT, o número de linhas devolvido é séries × baldes,
// que não tem teto nenhum no SQL. O LIMIT existe para o resultado não crescer sem
// fim; quando ele é ATINGIDO, o QuerySeries erra em vez de devolver gráfico truncado.
func TestBuildSQLTemLimit(t *testing.T) {
	from, to := ts("2026-08-07T18:00:00Z"), ts("2026-08-07T19:00:00Z")
	comGroup, err := buildSQL("metrics_1m", Request{Metric: "m", Tenant: "default", GroupBy: []string{"host"}, Step: 60, Agg: "avg"}, from, to, "")
	if err != nil {
		t.Fatal(err)
	}
	semGroup, err := buildSQL("metrics_1m", Request{Metric: "m", Tenant: "default", Step: 60, Agg: "avg"}, from, to, "")
	if err != nil {
		t.Fatal(err)
	}
	quero := fmt.Sprintf("LIMIT %d", maxSeriesRows)
	for nome, sql := range map[string]string{"com group_by": comGroup, "sem group_by": semGroup} {
		if !strings.HasSuffix(sql, quero) {
			t.Errorf("%s: o SQL de séries precisa terminar em %q, veio:\n%s", nome, quero, sql)
		}
	}
}
