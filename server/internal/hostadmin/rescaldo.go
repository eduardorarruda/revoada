package hostadmin

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// VARREDURA DE RESCALDO — por que apagar uma vez não bastava.
//
// A exclusão apaga a serverkey PRIMEIRO, para cortar a ingestão antes de mexer nos
// dados. Só que o gateway guarda a autenticação em cache por REVOADA_AUTH_CACHE_TTL
// (30 s por padrão, e é o valor de produção): a chave some do Postgres e continua
// valendo no gateway por meio minuto. Como toda ingestão chama UpsertHost — um
// INSERT … ON CONFLICT —, e o agente reporta a cada 15 s, um agente ainda de pé
// recria a linha de inventário e grava métricas DEPOIS de o ALTER … DELETE já ter
// passado. O servidor "apagado" volta à lista, agora com resíduo permanente no
// ClickHouse, porque a mutation não roda de novo sozinha.
//
// Isso morde exatamente no caminho "não tenho SSH — apagar só os dados", que é
// onde a máquina tem mais chance de continuar ligada.
//
// A varredura só faz trabalho se algo de fato voltou. O agendamento é gravado no
// banco (app_settings) porque o servidor pode reiniciar dentro da janela — um deploy,
// por exemplo — e a limpeza pendente se perderia justamente quando é necessária.
//
// São DOIS passes. O primeiro limpa o que a janela do cache de autenticação do
// gateway deixou passar (medido: 27 s de janela, com TTL padrão de 30 s).
//
// O segundo existe por um rastro que só apareceu no teste ao vivo: o medidor de deriva
// de relógio do gateway tem o hostname como rótulo, e onde o /metrics do gateway é
// raspado para dentro do ClickHouse esse nome volta a ser gravado a cada raspagem
// enquanto a série não expirar. RESSALVA HONESTA, conferida em 13/08/2026: essa
// raspagem existe no ambiente de dev e NÃO em produção (`scrape_targets` vazia, zero
// métricas `revoada_*` no ClickHouse em 7 dias). Ou seja, hoje o segundo passe é seguro
// mas ocioso em produção — ele fica porque o alvo pode ser criado a qualquer momento,
// e porque um passe a mais custa seis contagens recortadas por `ts`. Quem for mexer
// aqui precisa saber que o motivo original não descreve a produção atual.
var passesRescaldo = []time.Duration{90 * time.Second, 8 * time.Minute}

// janelaRescaldo é o primeiro passe — o número que a tela mostra ao operador.
var janelaRescaldo = passesRescaldo[0]

// janelaSobras é o quanto a contagem de rastro olha para trás. Precisa cobrir o maior
// passe com folga (o rastro nasce ENTRE a exclusão e a varredura) e ser curta o
// bastante para o ClickHouse podar granules pelo `ts`.
var janelaSobras = passesRescaldo[len(passesRescaldo)-1] + 5*time.Minute

// agendarRescaldo registra a varredura no banco e dispara os timers em memória.
func (h *Handler) agendarRescaldo(hostname string) {
	ultimo := passesRescaldo[len(passesRescaldo)-1]
	// Contexto próprio: o da requisição morre quando a resposta é escrita.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Grava só o ÚLTIMO passe: é ele que fecha a limpeza. Se o servidor reiniciar no
	// meio, ResumeSweeps varre na hora e volta a agendar este prazo.
	if err := h.st.AddPendingHostSweep(ctx, hostname, time.Now().Add(ultimo)); err != nil {
		// Não é motivo para falhar a exclusão: os timers em memória ainda vão rodar.
		h.log.Warn("registrar varredura de rescaldo falhou", "host", hostname, "err", err)
	}
	for _, d := range passesRescaldo {
		h.dispararRescaldo(hostname, d, d == ultimo)
	}
}

// dispararRescaldo espera o prazo e executa a varredura. `ultimo` diz se este é o
// passe que encerra a pendência gravada no banco.
func (h *Handler) dispararRescaldo(hostname string, espera time.Duration, ultimo bool) {
	go func() {
		if espera > 0 {
			time.Sleep(espera)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		h.varrerRescaldo(ctx, hostname, ultimo)
	}()
}

// ResumeSweeps retoma, no boot, as varreduras que ficaram devendo. Chamado uma vez
// pelo main. Varre JÁ (o reinício pode ter engolido os passes intermediários) e
// reagenda o passe final.
func (h *Handler) ResumeSweeps(ctx context.Context) {
	pend, err := h.st.ListPendingHostSweeps(ctx)
	if err != nil {
		h.log.Warn("listar varreduras de rescaldo pendentes falhou", "err", err)
		return
	}
	// As varreduras imediatas saem ESPAÇADAS. Cada uma consulta seis tabelas do
	// ClickHouse, e disparar todas as pendências no mesmo instante em que o servidor
	// está subindo põe o banco para atender uma rajada justo quando ele também está
	// recebendo a ingestão represada do reinício. Alguns segundos entre elas não
	// atrasam nada que importe: o rastro que elas caçam vive minutos.
	const passoEntreVarreduras = 3 * time.Second
	for i, p := range pend {
		atraso := time.Duration(i) * passoEntreVarreduras
		espera := time.Until(p.DueAt)
		if espera <= 0 {
			h.log.Info("varredura de rescaldo vencida; rodando agora", "host", p.Hostname)
			h.dispararRescaldo(p.Hostname, atraso, true)
			continue
		}
		h.log.Info("retomando varredura de rescaldo", "host", p.Hostname, "em", espera.Round(time.Second))
		h.dispararRescaldo(p.Hostname, atraso, false) // limpeza imediata do que já voltou
		h.dispararRescaldo(p.Hostname, espera, true)
	}
}

// varrerRescaldo confere se o host voltou e, se voltou, apaga de novo.
func (h *Handler) varrerRescaldo(ctx context.Context, hostname string, ultimo bool) {
	const tenant = "default"

	voltou, err := h.st.HostExists(ctx, tenant, hostname)
	if err != nil {
		h.log.Warn("rescaldo: conferir inventário falhou", "host", hostname, "err", err)
	}
	if voltou {
		// A chave já não existe (foi apagada na exclusão) e a janela do cache fechou,
		// então esta linha é o rastro da janela — não um servidor vivo.
		if derr := h.st.DeleteHost(ctx, tenant, hostname); derr != nil {
			h.log.Error("rescaldo: apagar host que ressuscitou falhou", "host", hostname, "err", derr)
		} else {
			h.log.Warn("rescaldo: o host tinha voltado pela janela do cache e foi apagado de novo", "host", hostname)
		}
	}

	sobras, mediuTudo := h.contarSobrasCH(ctx, hostname)
	if sobras > 0 {
		h.log.Warn("rescaldo: métricas gravadas depois da exclusão; apagando de novo",
			"host", hostname, "linhas", sobras)
		if _, _, falhas := h.purgeClickHouse(ctx, hostname); len(falhas) > 0 {
			h.log.Error("rescaldo: nova tentativa de expurgo falhou", "host", hostname, "tabelas", falhas)
		}
	}

	// A pendência só é dada por encerrada quando NÓS OLHAMOS de fato.
	//
	// Sem `mediuTudo`, uma consulta que falhou (ClickHouse fora do ar) devolvia zero
	// sobras, e o código concluía "nada voltou" e apagava a pendência — para sempre.
	// O caso não é hipotético: ResumeSweeps roda no boot do servidor, que sobe antes
	// do ClickHouse num `compose up` ou num reboot da máquina. Provado ao vivo: com o
	// ClickHouse parado, a varredura registrou "nada voltou" e a linha do host apagado
	// continuou no banco. Erro de leitura tem de manter a pendência para a próxima
	// tentativa — falta de resposta não é resposta.
	if ultimo && mediuTudo {
		if err := h.st.ClearPendingHostSweep(ctx, hostname); err != nil {
			h.log.Warn("rescaldo: limpar pendência falhou", "host", hostname, "err", err)
		}
	} else if ultimo {
		h.log.Warn("rescaldo: o ClickHouse não respondeu; mantendo a varredura pendente para o próximo boot",
			"host", hostname)
	}
	if !voltou && sobras == 0 && mediuTudo {
		h.log.Info("rescaldo: nada voltou depois da exclusão", "host", hostname)
	}
}

// contarSobrasCH soma o que restou daquele host nas tabelas do ClickHouse. Zero
// significa que a exclusão pegou tudo e a varredura não tem trabalho a fazer.
//
// O segundo retorno diz se TODAS as tabelas responderam. Ele existe porque zero por
// "não achei nada" e zero por "não consegui perguntar" são a mesma soma e conclusões
// opostas — e o chamador usa essa diferença para decidir se pode encerrar a varredura.
func (h *Handler) contarSobrasCH(ctx context.Context, hostname string) (int64, bool) {
	// O recorte de tempo é o que faz esta contagem caber num painel com histórico.
	// `labels['host']` NÃO está na chave de ordenação de nenhuma dessas tabelas, então
	// contar sem recorte lê a tabela inteira: medido em dev, 2,17 M linhas e 85 MiB
	// por tabela para um host que não existe mais — e a rodada acontece três vezes por
	// exclusão. `ts` é a primeira coluna útil da ordenação, e o que esta varredura
	// procura é, por definição, RECENTE: só interessa o que foi gravado depois do
	// DELETE. A janela cobre com folga o maior passe (8 min).
	pred := "labels['host']=" + quote(hostname) +
		" AND ts > now() - INTERVAL " + strconv.Itoa(int(janelaSobras.Seconds())) + " SECOND"
	var total int64
	mediuTudo := true
	for _, tbl := range chTables {
		r, err := h.ch.QueryJSON(ctx, fmt.Sprintf("SELECT count() AS c FROM %s WHERE %s", tbl, pred))
		if err != nil {
			h.log.Warn("rescaldo: contagem falhou", "table", tbl, "host", hostname, "err", err)
			mediuTudo = false
			continue
		}
		if len(r) > 0 {
			total += asInt64(r[0]["c"])
		}
	}
	return total, mediuTudo
}
