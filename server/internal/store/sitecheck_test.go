package store

import (
	"testing"
	"time"
)

// O defeito bloqueante: `if total == 0 { return 100, 0, nil }`. Ausência de
// medição virava afirmação máxima, publicada numa status page pública sem login.
func TestSemSondagemNaoEhCemPorCento(t *testing.T) {
	now := time.Now()
	st := MakeUptimeStat(UptimeCount{}, time.Time{}, now.Add(-24*time.Hour), now, "padrao")
	if st.Sufficient {
		t.Fatal("sem nenhuma sondagem o percentual não pode ser exibível")
	}
	if st.Percent != 0 || st.Coverage != 0 {
		t.Fatalf("sem medição: percent=%v coverage=%v (esperado 0/0)", st.Percent, st.Coverage)
	}
}

// Reprodução do dev: o check 7 (tier padrão = 300s) tinha 20 sondagens nas
// últimas 24 h — 288 esperadas, 6,9% de cobertura — e /api/status publicava
// uptime_day = 100.
func TestCoberturaDe7PorCentoNaoAutorizaPercentual(t *testing.T) {
	now := time.Now()
	inicio := now.Add(-26 * 24 * time.Hour) // histórico bem anterior à janela
	st := MakeUptimeStat(UptimeCount{Samples: 20, OK: 20}, inicio, now.Add(-24*time.Hour), now, "padrao")
	// 283 e não 288: o agendador marca a próxima sondagem DEPOIS de terminar a atual
	// e o laço acorda de 5 em 5 s, então a cadência real de um tier de 300 s é 305 s.
	// O denominador nominal (288) é inatingível por construção — ver MakeUptimeStat.
	if st.Expected != 283 {
		t.Fatalf("tier padrão em 24 h espera 283 sondagens na cadência real, calculou %d", st.Expected)
	}
	if st.Coverage > 7.5 || st.Coverage < 6.5 {
		t.Fatalf("cobertura = %.2f%%, esperava ~6,9%%", st.Coverage)
	}
	if st.Sufficient {
		t.Fatal("6,9%% de cobertura não pode publicar percentual algum, muito menos 100%%")
	}
}

func TestCoberturaCheiaAutorizaPercentual(t *testing.T) {
	now := time.Now()
	inicio := now.Add(-40 * 24 * time.Hour)
	// 1.440 sondagens em 24 h no tier crítico (60s) = 100% de cobertura.
	st := MakeUptimeStat(UptimeCount{Samples: 1440, OK: 1438}, inicio, now.Add(-24*time.Hour), now, "critico")
	if !st.Sufficient {
		t.Fatalf("cobertura %.1f%% deveria bastar", st.Coverage)
	}
	if st.Percent < 99.8 || st.Percent > 99.9 {
		t.Fatalf("percentual = %.3f, esperava ~99,86", st.Percent)
	}
	if !st.WindowFull {
		t.Error("histórico de 40 dias cobre a janela de 24 h")
	}
}

// Um check novo não pode ser condenado a "sem dados" para sempre: as amostras
// esperadas são contadas desde quando o check passou a existir, não desde o
// começo da janela pedida.
func TestCheckNovoNaoEhPunidoPelaJanelaLonga(t *testing.T) {
	now := time.Now()
	inicio := now.Add(-2 * time.Hour) // criado há 2 h
	st := MakeUptimeStat(UptimeCount{Samples: 24, OK: 24}, inicio, now.Add(-30*24*time.Hour), now, "padrao")
	if st.Expected != 23 {
		t.Fatalf("2 h no tier padrão (cadência real de 305 s) esperam 23 sondagens, calculou %d", st.Expected)
	}
	if !st.Sufficient {
		t.Fatalf("cobertura %.1f%% deveria bastar para o período em que o check existiu", st.Coverage)
	}
	// Mas a janela de 30 dias NÃO está coberta: quem publica "30d" precisa saber.
	if st.WindowFull {
		t.Error("2 h de histórico não cobrem 30 dias — window_full tem de ser falso")
	}
}

// O uptime_90d do dev era calculado sobre 26 dias de histórico com um buraco de
// 12 dias. WindowFull é o sinal que impede a status page de publicar "90d".
func TestJanelaDe90DiasComHistoricoDe26NaoEstaCheia(t *testing.T) {
	now := time.Now()
	inicio := now.Add(-26 * 24 * time.Hour)
	st := MakeUptimeStat(UptimeCount{Samples: 260, OK: 260}, inicio, now.Add(-90*24*time.Hour), now, "padrao")
	if st.WindowFull {
		t.Fatal("26 dias de histórico não podem declarar a janela de 90 dias coberta")
	}
	if st.Sufficient {
		t.Fatalf("260 sondagens em 26 dias (cobertura %.2f%%) não autorizam percentual", st.Coverage)
	}
}

func TestCoberturaTemTeto(t *testing.T) {
	now := time.Now()
	inicio := now.Add(-10 * 24 * time.Hour)
	// Mais amostras que o esperado (retentativas de SUSPEITO a cada 30s) não pode
	// gerar "cobertura 300%".
	st := MakeUptimeStat(UptimeCount{Samples: 5000, OK: 2500}, inicio, now.Add(-24*time.Hour), now, "padrao")
	if st.Coverage != 100 {
		t.Fatalf("cobertura = %.1f, deveria estar limitada a 100", st.Coverage)
	}
	if st.Percent != 50 {
		t.Fatalf("percentual = %.1f, esperava 50", st.Percent)
	}
}

func TestChildStatesUp(t *testing.T) {
	c := ChildStates{Total: 10, Down: 3, Degraded: 2}
	if c.Up() != 5 {
		t.Fatalf("páginas no ar = %d, esperava 5", c.Up())
	}
}

// TestAlvoPenduradoNaoPerdeOUptime é o caso perverso que a cadência nominal criava.
//
// Um alvo que estoura o tempo limite (20 s) a cada ciclo tem período real de
// 60 + 20 + 5 = 85 s, e não 60 s. Contra o denominador nominal a cobertura dava 70%,
// cruzava MinUptimeCoverage e o painel PARAVA de publicar o uptime — justamente do
// site mais quebrado do parque, cujas amostras existiam e diziam todas 0%. O cliente
// que fosse pedir crédito de SLA não tinha número.
func TestAlvoPenduradoNaoPerdeOUptime(t *testing.T) {
	now := time.Now()
	inicio := now.Add(-40 * 24 * time.Hour)
	// 24 h de sondagens que expiram: 86400/85 ≈ 1016 amostras, todas com falha.
	c := UptimeCount{Samples: 1016, OK: 0, AvgMs: 20000}
	st := MakeUptimeStat(c, inicio, now.Add(-24*time.Hour), now, "critico")
	if !st.Sufficient {
		t.Fatalf("cobertura %.1f%% (esperadas %d) deveria bastar: as amostras existem e todas dizem 0%%",
			st.Coverage, st.Expected)
	}
	if st.Percent != 0 {
		t.Fatalf("percentual = %v, esperava 0 (todas as sondagens falharam)", st.Percent)
	}
}

// TestSondagemRapidaContinuaComCoberturaCheia: a correção não pode inflar a
// cobertura de quem responde rápido — o denominador tem de continuar apertado ali.
func TestSondagemRapidaContinuaComCoberturaCheia(t *testing.T) {
	now := time.Now()
	inicio := now.Add(-40 * 24 * time.Hour)
	// Tier crítico com sondagem de 150 ms: cadência real ≈ 65,15 s → ~1326 amostras.
	st := MakeUptimeStat(UptimeCount{Samples: 1326, OK: 1326, AvgMs: 150}, inicio, now.Add(-24*time.Hour), now, "critico")
	if st.Coverage < 99 {
		t.Fatalf("cobertura = %.2f%%, esperava ~100%% (esperadas %d)", st.Coverage, st.Expected)
	}
	// Metade das sondagens faltando continua sendo cobertura insuficiente.
	meia := MakeUptimeStat(UptimeCount{Samples: 600, OK: 600, AvgMs: 150}, inicio, now.Add(-24*time.Hour), now, "critico")
	if meia.Sufficient {
		t.Fatalf("45%% de cobertura (%.1f%%) não pode autorizar percentual", meia.Coverage)
	}
}

// Uptime ponderado pelo TEMPO. Com o reteste de 30 s durante a queda, contar
// amostras superestima a falha: um site padrão (5 min) que cai 10 min num dia gera
// ~288 amostras boas e ~20 ruins, e a conta por amostra publicaria 93,5% quando o
// site ficou fora 0,7% do dia. Cada sondagem passa a valer o tempo até a próxima.
func TestUptimePonderadoPeloTempoNaoPuneOReteste(t *testing.T) {
	now := time.Now()
	inicio := now.Add(-48 * time.Hour)
	dia := 24 * time.Hour.Seconds()
	c := UptimeCount{Samples: 308, OK: 288, AvgMs: 300, SegTotal: dia, SegOK: dia - 600}
	st := MakeUptimeStat(c, inicio, now.Add(-24*time.Hour), now, "padrao")
	if st.Percent < 99.30 || st.Percent > 99.31 {
		t.Fatalf("10 min fora em 24 h = 99,31%%; obtive %.2f%%", st.Percent)
	}
	if st.Coverage < 99.9 || !st.Sufficient {
		t.Fatalf("o dia inteiro foi medido: cobertura %.1f%% suficiente=%v", st.Coverage, st.Sufficient)
	}
}

// A cobertura por tempo enxerga o buraco que a contagem de amostras escondia: 20 h
// sem sondagem não são "cobertas" por uma rajada de retestes nas outras 4 h.
func TestCoberturaPorTempoEnxergaOBuraco(t *testing.T) {
	now := time.Now()
	inicio := now.Add(-48 * time.Hour)
	quatroHoras := 4 * time.Hour.Seconds()
	c := UptimeCount{Samples: 600, OK: 600, SegTotal: quatroHoras, SegOK: quatroHoras}
	st := MakeUptimeStat(c, inicio, now.Add(-24*time.Hour), now, "padrao")
	if st.Coverage > 17 || st.Sufficient {
		t.Fatalf("4 h medidas de 24 h = ~16,7%%; obtive %.1f%% suficiente=%v", st.Coverage, st.Sufficient)
	}
}

// Sem os pesos (consulta antiga, histórico sem LEAD), a conta por amostra continua.
func TestSemPesoCaiNaContaPorAmostra(t *testing.T) {
	now := time.Now()
	st := MakeUptimeStat(UptimeCount{Samples: 100, OK: 99}, now.Add(-48*time.Hour), now.Add(-24*time.Hour), now, "padrao")
	if st.Percent != 99 {
		t.Fatalf("fallback por amostra: %.2f", st.Percent)
	}
}
