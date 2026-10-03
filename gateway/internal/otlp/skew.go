package otlp

import (
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/metrics"
	"github.com/eduardorarruda/revoada/gateway/internal/model"
)

// idadeDoCarimbo é a IDADE DO CARIMBO de cada host: (agora do gateway) − (ts do
// agente), em segundos. Positivo = o carimbo é velho; negativo = está no futuro.
//
// # O nome mudou porque o nome anterior mentia
//
// Isto se chamava `revoada_agent_clock_skew_seconds` e prometia responder "o relógio
// deste servidor está certo?". Ele não responde, e não tem como responder: o agente
// carimba o ponto quando MONTA o lote, não quando consegue enviá-lo. Quando a rede
// volta depois de uma queda, o agente drena o buffer em disco e o gateway recebe
// carimbos velhos de um servidor com o relógio perfeito. Medido em 13/08/2026:
// gateway parado por 2 min 45 s, 11 lotes represados; em regime a leitura era
// −0,007 s e, na drenagem, −46,46 s. O buffer guarda ~20 h, então a leitura chegaria
// a dezenas de milhares de segundos — e chega exatamente na hora em que alguém está
// olhando para o painel tentando entender a queda.
//
// Separar relógio de fila EXIGE o carimbo do envio, que só o agente pode pôr. Nem o
// tamanho do lote nem a dispersão dos carimbos resolvem: o agente drena lote a lote,
// e um lote represado é indistinguível de um ciclo normal com o relógio atrasado.
// Enquanto o agente não mandar essa informação, a saída honesta é o medidor dizer o
// que ele de fato mede — a idade do que chegou, seja a causa relógio ou fila.
//
// POR QUE existe (o buraco que ele fecha): a guarda de relógio (guard.go) funciona e
// recusa o absurdo — +1 h, +10 anos, −30 dias caem nos dois transportes. Mas a deriva
// DENTRO da tolerância não era medida em lugar nenhum: a diferença entre o `ts` do
// agente e o `now` do gateway existia em memória, no meio do process(), e era jogada
// fora. E é justamente nessa faixa que o painel mente sem avisar:
//
//	um host 0–120 s ADIANTADO passa na guarda (tolerância de futuro = 2 min) e o
//	ponto é GRAVADO com ts no futuro; as consultas de último valor filtram
//	`ts <= now()`, então elas não enxergam esse ponto e o Mural mostra "sem métricas"
//	para um host que está reportando normalmente, a cada 15 s, sem um único erro em
//	lugar nenhum da tela.
//
// Sem este medidor a única saída era abrir o ClickHouse e comparar ts com o relógio do
// gateway à mão. Com ele, "o host X está 47 s adiantado" é uma leitura de /metrics.
//
// A medição é feita ANTES do filtro de aceitação, com o ts MAIS RECENTE do lote.
//
// Antes ela era feita depois, sobre os pontos aceitos — e isso invertia o medidor em
// relação ao seu propósito: o host cujo relógio está REALMENTE quebrado é o único que
// tem todos os pontos recusados pela guarda, então era o único que nunca ganhava
// série. Medido: um host mandando +1 h a cada 15 s deixou o medidor CONGELADO em
// −120 s (a última leitura aceita, de quando ele ainda estava bom) e depois a série
// expirou — enquanto ele seguia reportando, e errado. O contador de recusa não tem
// rótulo de host, então não sobrava nada no /metrics com o nome do culpado.
//
// O ts mais recente, e não a média: o lote cobre um intervalo de coleta, e o ponto
// mais novo é o que está mais perto de "agora" — a média diluiria a leitura com a
// idade natural do lote.
//
// O rótulo é o hostname, que vem do payload: por isso é um GaugeVec com TETO de séries
// e expiração (ver metrics.GaugeVec). Um emissor que inventasse um host por requisição
// não pode transformar o /metrics do gateway em vazamento de memória.
// A validade da série é CURTA (5 min) por dois motivos, e o segundo só apareceu
// quando a exclusão de servidor foi testada ao vivo:
//
//   - honestidade: idade é uma medida do AGORA. Guardando o último valor por uma
//     hora, o medidor continuava anunciando um número sobre um host que parou de
//     reportar — um número que já não descreve nada;
//
//   - o rótulo é o hostname, e o /metrics do gateway PODE ser raspado para dentro do
//     ClickHouse. Onde isso acontece, uma validade de uma hora fazia um servidor
//     APAGADO pelo painel seguir gerando linha com o próprio nome a cada raspagem por
//     mais uma hora (medido: uma linha a cada 3 s), ressuscitando o nome nas telas que
//     listam hosts a partir das métricas.
//
//     ONDE ISSO VALE, hoje: só em ambiente de desenvolvimento. Conferido em produção
//     em 13/08/2026 — `scrape_targets` está VAZIA e não existe uma única métrica
//     `revoada_*` no ClickHouse dos últimos 7 dias. Nada semeia esse alvo em código
//     (AddScrapeTarget não tem chamador fora de teste), então ele é criado à mão. O
//     motivo continua válido para quem criar o alvo um dia; o que não vale é ler este
//     comentário como descrição da produção atual.
var idadeDoCarimbo = metrics.NewGaugeVec(
	"revoada_agent_ts_age_seconds",
	"Idade do carimbo do último ponto recebido de cada host: agora do gateway menos ts do agente, em segundos. Positivo = carimbo velho (relógio atrasado OU fila represada depois de uma queda); negativo = carimbo no futuro (relógio adiantado).",
	2000, 5*time.Minute, "host")

// skewFallbackHost é usado quando o ponto não tem rótulo `host` — a medida ainda
// precisa aparecer, senão o host sem rótulo (justamente o mais difícil de diagnosticar)
// fica de fora do medidor.
const skewFallbackHost = "sem-host"

// observeClockSkew registra a idade do carimbo por host de um lote RECÉM-CHEGADO
// (antes do filtro de aceitação). bound é o hostname vinculado à serverkey, usado
// quando o ponto não traz `host`.
func observeClockSkew(points []model.Metric, now time.Time, bound string) {
	if len(points) == 0 {
		return
	}
	// ts mais recente por host (ver comentário do medidor).
	newest := make(map[string]time.Time, 4)
	for i := range points {
		h := points[i].Labels["host"]
		if h == "" {
			h = points[i].Labels["host.name"]
		}
		if h == "" {
			h = bound
		}
		if h == "" {
			h = skewFallbackHost
		}
		if cur, ok := newest[h]; !ok || points[i].TS.After(cur) {
			newest[h] = points[i].TS
		}
	}
	for h, ts := range newest {
		// now − ts: positivo é carimbo VELHO, que é como a palavra "idade" se lê.
		idadeDoCarimbo.Set(now.Sub(ts).Seconds(), h)
	}
}
