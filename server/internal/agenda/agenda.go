// Package agenda reúne o que os agendadores sintéticos (sitecheck e journey)
// precisam para cumprir o intervalo que prometem na tela.
//
// Medido na validação de 02/10/2026 contra um servidor que registrava cada
// requisição: o tier `critico` ("a cada 60 s") sondava a cada 65 s, o reteste de
// falha ("30 s") a cada 35 s, e as jornadas de 30 s a cada 35 s. Com um único site
// lento no ar (resposta em 20 s), TODOS os outros atrasavam junto: 75 s no lugar de
// 60 e 40 s no lugar de 30. Duas causas, uma em cada função daqui:
//
//  1. a próxima execução era marcada a partir do FIM da sondagem, e o agendador
//     acorda em degraus de 5 s: qualquer atraso, por menor que seja, empurrava a
//     execução para o degrau seguinte (+5 s). Reancorar e Folga corrigem;
//  2. o tick esperava TODAS as sondagens do lote terminarem antes de voltar a olhar
//     a agenda (wg.Wait): um site travado até o timeout segurava os demais.
//     EmVoo dispara sem esperar e impede que um check em andamento seja disparado
//     de novo.
package agenda

import (
	"context"
	"sync"
	"time"
)

// Folga é quanto antes do instante marcado uma execução já conta como devida. O
// agendador acorda a cada 5 s; sem folga, uma execução marcada para alguns
// milissegundos depois do tick só seria vista no tick seguinte.
const Folga = time.Second

// Reancorar desloca a próxima execução, calculada a partir do FIM de uma execução
// (fim), para o INÍCIO dela (inicio): a cadência passa a ser de início a início,
// que é o que "a cada 60 s" quer dizer. Nunca devolve um instante antes de fim —
// uma execução mais longa que o próprio intervalo roda de novo logo em seguida,
// sem marcar no passado. nil continua nil.
func Reancorar(proxima *time.Time, inicio, fim time.Time) *time.Time {
	if proxima == nil {
		return nil
	}
	dur := fim.Sub(inicio)
	if dur <= 0 {
		return proxima
	}
	n := proxima.Add(-dur)
	if n.Before(fim) {
		n = fim
	}
	return &n
}

// EmVoo dispara execuções sem bloquear quem dispara, com no máximo `max`
// simultâneas, e recusa disparar de novo um id que ainda está rodando.
type EmVoo struct {
	mu  sync.Mutex
	ids map[int64]bool
	sem chan struct{}
	wg  sync.WaitGroup
}

// NovoEmVoo cria o despachante com teto de max execuções simultâneas (mín. 1).
func NovoEmVoo(max int) *EmVoo {
	if max < 1 {
		max = 1
	}
	return &EmVoo{ids: map[int64]bool{}, sem: make(chan struct{}, max)}
}

// Disparar roda fn numa goroutine, a menos que o id já esteja em voo (devolve
// false). Não espera vaga: quem não cabe no teto aguarda na própria goroutine,
// e o tick segue livre para olhar a agenda.
func (e *EmVoo) Disparar(ctx context.Context, id int64, fn func()) bool {
	e.mu.Lock()
	if e.ids[id] {
		e.mu.Unlock()
		return false
	}
	e.ids[id] = true
	e.wg.Add(1) // ainda sob o lock: Esperar nunca vê um disparo pela metade
	e.mu.Unlock()
	go func() {
		defer e.wg.Done()
		defer func() {
			e.mu.Lock()
			delete(e.ids, id)
			e.mu.Unlock()
		}()
		select {
		case e.sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		defer func() { <-e.sem }()
		fn()
	}()
	return true
}

// Esperar bloqueia até todas as execuções disparadas terminarem (desligamento e
// testes).
func (e *EmVoo) Esperar() { e.wg.Wait() }
