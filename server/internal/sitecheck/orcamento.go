package sitecheck

import (
	"sync"
	"time"
)

// TETO HORÁRIO DE MENSAGENS POR CHECK.
//
// O avaliador de métricas tem esse freio desde sempre (alerting.gastaOrcamento, 20
// mensagens por regra por hora) e ele funciona — está medido no notification_log: 20
// mensagens por canal na hora do incidente, contra 65 antes de o teto existir.
//
// As sondagens de site passavam POR FORA dele. Elas chamam o roteador direto, e o
// roteador entrega na hora, sem janela de agrupamento. Um alvo oscilando em torno do
// limite — o caso mais comum de tudo: uma latência que dança em volta do tempo de
// resposta, uma página que alterna 200 e 502 — rendia uma mensagem por transição, por
// canal, indefinidamente. É o pior tipo de ruído de monitoração, porque ensina o
// destinatário a ignorar o canal, e aí o alerta que importa chega e ninguém lê.
//
// O teto NÃO é censura: o que não coube vira contagem, e a contagem viaja na próxima
// mensagem que passar (ThrottledCount, o mesmo campo que o avaliador usa). "Te avisei
// que houve mais 37" é diferente de "não te avisei".
const maxAvisosPorCheckHora = 20

// orcamentoDeAvisos é o teto por check. Guarda os instantes das mensagens entregues
// na última hora (janela deslizante, não balde que zera na virada da hora: a virada
// deixaria passar 40 mensagens em dois minutos) e quantas foram engolidas desde a
// última entrega.
type orcamentoDeAvisos struct {
	mu        sync.Mutex
	entregues map[int64][]time.Time
	engolidas map[int64]int
}

func novoOrcamento() *orcamentoDeAvisos {
	return &orcamentoDeAvisos{entregues: map[int64][]time.Time{}, engolidas: map[int64]int{}}
}

// cabe debita uma mensagem do teto do check. Devolve (pode enviar, quantas foram
// engolidas desde a última entrega). Quando devolve false, o chamador NÃO envia — mas
// o evento não some: ele já foi contado para a próxima mensagem que couber.
func (o *orcamentoDeAvisos) cabe(checkID int64, agora time.Time) (bool, int) {
	// Nil = sem teto. Um Checker montado à mão (teste) não pode perder a mensagem por
	// causa de um campo não inicializado — silenciar aviso é o defeito, não o remédio.
	if o == nil {
		return true, 0
	}
	o.mu.Lock()
	defer o.mu.Unlock()

	recentes := o.entregues[checkID][:0]
	for _, t := range o.entregues[checkID] {
		if agora.Sub(t) <= time.Hour {
			recentes = append(recentes, t)
		}
	}
	o.entregues[checkID] = recentes

	if len(recentes) >= maxAvisosPorCheckHora {
		o.engolidas[checkID]++
		return false, o.engolidas[checkID]
	}
	engolidas := o.engolidas[checkID]
	o.engolidas[checkID] = 0
	o.entregues[checkID] = append(recentes, agora)
	return true, engolidas
}
