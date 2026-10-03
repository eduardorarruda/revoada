package sitecheck

import (
	"testing"
	"time"
)

// PROVA: um site oscilando não vira enxurrada, e o que não coube é CONTADO.
//
// As sondagens eram o único caminho de notificação do painel sem teto de oscilação —
// o avaliador de métricas tem o dele desde sempre. Uma latência dançando em volta do
// limite rendia uma mensagem por transição, por canal, sem fim; e canal que vira ruído
// é canal que ninguém lê quando o alerta importante chega.
func TestTetoHorarioPorCheckContaOQueEngoliu(t *testing.T) {
	o := novoOrcamento()
	base := time.Date(2026, 8, 13, 10, 0, 0, 0, time.UTC)

	for i := 0; i < maxAvisosPorCheckHora; i++ {
		if ok, _ := o.cabe(7, base.Add(time.Duration(i)*time.Second)); !ok {
			t.Fatalf("as %d primeiras têm de passar; a %da não passou", maxAvisosPorCheckHora, i+1)
		}
	}
	for i := 0; i < 37; i++ {
		if ok, _ := o.cabe(7, base.Add(time.Minute)); ok {
			t.Fatal("passado o teto, a mensagem não pode sair")
		}
	}

	// Outro check não paga pelo vizinho: o teto é POR site.
	if ok, _ := o.cabe(8, base.Add(time.Minute)); !ok {
		t.Fatal("o teto de um check não pode calar outro")
	}

	// Passada a hora, libera — e a primeira mensagem carrega quantas foram engolidas.
	ok, engolidas := o.cabe(7, base.Add(time.Hour+time.Second))
	if !ok {
		t.Fatal("depois de uma hora o teto tem de liberar")
	}
	if engolidas != 37 {
		t.Fatalf("engolidas = %d, esperava 37 — teto sem contagem é censura silenciosa", engolidas)
	}
	// E o contador zera depois de viajar numa mensagem.
	if _, e := o.cabe(7, base.Add(time.Hour+2*time.Second)); e != 0 {
		t.Fatalf("a contagem tinha de zerar depois de entregue; veio %d", e)
	}
}

// A janela é DESLIZANTE, não um balde que zera na virada da hora — senão passariam
// 40 mensagens em dois minutos, uma no fim de uma hora e outra no início da seguinte.
func TestJanelaEhDeslizante(t *testing.T) {
	o := novoOrcamento()
	base := time.Date(2026, 8, 13, 10, 59, 0, 0, time.UTC)
	for i := 0; i < maxAvisosPorCheckHora; i++ {
		o.cabe(9, base)
	}
	if ok, _ := o.cabe(9, base.Add(90*time.Second)); ok {
		t.Fatal("virar a hora do relógio não pode reabrir o teto")
	}
}
