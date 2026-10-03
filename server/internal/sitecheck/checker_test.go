package sitecheck

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/alerting"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// fakeNotifier conta por qual caminho a notificação saiu.
type fakeNotifier struct {
	routed   int
	direct   int
	lastChan []int64
	last     alerting.Notification
}

func (f *fakeNotifier) Notify(_ context.Context, n alerting.Notification) { f.routed++; f.last = n }
func (f *fakeNotifier) NotifyChannels(_ context.Context, _ alerting.Notification, ch []int64) {
	f.direct++
	f.lastChan = ch
}

func TestEmitRoutesByChannels(t *testing.T) {
	f := &fakeNotifier{}
	c := &Checker{notifier: f}

	// Sem canais próprios: cai nas Rotas de notificação (retrocompatível).
	c.emit(context.Background(), &store.SiteCheck{}, alerting.Notification{})
	if f.routed != 1 || f.direct != 0 {
		t.Fatalf("sem canais deveria usar rotas: routed=%d direct=%d", f.routed, f.direct)
	}

	// Com canais próprios: entrega direta aos canais do site.
	c.emit(context.Background(), &store.SiteCheck{ChannelIDs: []int64{3, 7}}, alerting.Notification{})
	if f.direct != 1 || f.routed != 1 {
		t.Fatalf("com canais deveria entregar direto sem tocar rotas: routed=%d direct=%d", f.routed, f.direct)
	}
	if len(f.lastChan) != 2 || f.lastChan[0] != 3 || f.lastChan[1] != 7 {
		t.Fatalf("canais errados repassados: %v", f.lastChan)
	}
}

// TestNotifyNaoChamaOAlvoDeSite trava o vocabulário do aviso que chega ao celular.
//
// O monitor de HTTP vigia site, API e endpoint interno com o MESMO código. Enquanto
// a regra se chamava "Site DOWN: <nome>", um aviso correto sobre a API chegava
// anunciando um "site" que não existia — e a resposta de quem recebeu foi "errado".
// Perder a confiança no alerta é pior do que não ter alerta: o nome do check já
// identifica o alvo, a palavra "site" só acrescenta um palpite.
func TestNotifyNaoChamaOAlvoDeSite(t *testing.T) {
	f := &fakeNotifier{}
	c := &Checker{notifier: f}
	chk := &store.SiteCheck{Name: "Eduq — Portal", URL: "https://app.eduq.tec.br", Alerting: true}

	c.notify(context.Background(), chk, Result{Status: 0, Diagnosis: "connect_timeout"}, "firing", time.Time{})

	if f.routed != 1 {
		t.Fatalf("o alerta deveria ter saído: routed=%d", f.routed)
	}
	nome := f.last.Rule.Name
	if strings.Contains(strings.ToLower(nome), "site") {
		t.Errorf("a regra não pode afirmar que o alvo é um site: %q", nome)
	}
	if nome != "Fora do ar: Eduq — Portal" {
		t.Errorf("nome da regra inesperado: %q", nome)
	}
}

// --- Confirmação por tier e reteste durante a queda ---
//
// Em 25/09 três avisos de "fora do ar" chegaram para sites que, minutos depois,
// estavam no ar: quedas de dezenas de segundos (fila do Apache cheia) viravam
// "Ficou fora por 5 min" porque a 2ª falha em 30 s já confirmava DOWN e a
// recuperação só era vista no ciclo seguinte do tier. Estes testes fixam a regra
// nova: o tier decide quantas tentativas confirmam, e um site em queda é retestado
// a cada 30 s até voltar.

func checkerDeTeste(f *fakeNotifier) *Checker {
	return &Checker{notifier: f, orcamento: novoOrcamento(), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
}

func TestFalhasParaConfirmarPorTier(t *testing.T) {
	casos := map[string]int{"critico": 2, "padrao": 3, "basico": 4, "": 3, "outro": 3}
	for tier, quer := range casos {
		if got := falhasParaConfirmar(tier); got != quer {
			t.Errorf("falhasParaConfirmar(%q) = %d, quero %d", tier, got, quer)
		}
	}
}

func TestPadraoSoConfirmaNaTerceiraFalhaERetestaEm30s(t *testing.T) {
	f := &fakeNotifier{}
	c := checkerDeTeste(f)
	ctx := context.Background()
	chk := &store.SiteCheck{Name: "Site cursos", URL: "https://x", Tier: "padrao", Alerting: true, State: "UP"}
	res := Result{Diagnosis: DiagConnTimeout, TotalMs: 20000}
	t0 := time.Date(2026, 9, 25, 20, 7, 0, 0, time.UTC)
	intervalo := 5 * time.Minute

	c.onFailure(ctx, chk, res, t0, intervalo)
	if chk.State != "SUSPEITO" || f.routed != 0 || chk.NextCheckAt.Sub(t0) != 30*time.Second {
		t.Fatalf("1ª falha: estado=%s avisos=%d próximo=%v", chk.State, f.routed, chk.NextCheckAt.Sub(t0))
	}
	c.onFailure(ctx, chk, res, t0.Add(50*time.Second), intervalo)
	if chk.State != "SUSPEITO" || f.routed != 0 {
		t.Fatalf("2ª falha no tier padrão ainda não confirma: estado=%s avisos=%d", chk.State, f.routed)
	}
	t3 := t0.Add(100 * time.Second)
	c.onFailure(ctx, chk, res, t3, intervalo)
	if chk.State != "DOWN" || f.routed != 1 || f.last.State != "firing" {
		t.Fatalf("3ª falha confirma DOWN com um aviso: estado=%s avisos=%d", chk.State, f.routed)
	}
	// A queda começa na PRIMEIRA falha: é dela que "Começou" e "Ficou fora por" contam.
	if chk.DownSince == nil || !chk.DownSince.Equal(t0) {
		t.Fatalf("DownSince deveria ser a hora da 1ª falha (%v): %v", t0, chk.DownSince)
	}
	if !f.last.Since.Equal(t0) {
		t.Fatalf("o aviso diz que começou na 1ª falha: since=%v", f.last.Since)
	}
	// Em queda, o reteste é em 30 s (não nos 5 min do tier): é o que faz o
	// "RESOLVIDO" chegar na hora e a duração no aviso ser a real.
	if chk.NextCheckAt.Sub(t3) != 30*time.Second {
		t.Fatalf("em DOWN o próximo teste deveria ser em 30 s, foi %v", chk.NextCheckAt.Sub(t3))
	}
	c.onFailure(ctx, chk, res, t3.Add(35*time.Second), intervalo)
	if f.routed != 1 || chk.State != "DOWN" {
		t.Fatalf("seguir em DOWN não repete o aviso: avisos=%d", f.routed)
	}
	if !strings.Contains(f.last.Evidence, "3 tentativas seguidas") {
		t.Errorf("a evidência diz quantas tentativas falharam: %q", f.last.Evidence)
	}
}

func TestCriticoConfirmaNaSegundaFalha(t *testing.T) {
	f := &fakeNotifier{}
	c := checkerDeTeste(f)
	chk := &store.SiteCheck{Name: "Portal", URL: "https://x", Tier: "critico", Alerting: true, State: "UP"}
	res := Result{Diagnosis: DiagConnTimeout, TotalMs: 20000}
	t0 := time.Now()
	c.onFailure(context.Background(), chk, res, t0, time.Minute)
	c.onFailure(context.Background(), chk, res, t0.Add(30*time.Second), time.Minute)
	if chk.State != "DOWN" || f.routed != 1 {
		t.Fatalf("crítico: 2ª falha confirma: estado=%s avisos=%d", chk.State, f.routed)
	}
}

func TestSuspeitoQueVoltaNaoAvisaNinguem(t *testing.T) {
	f := &fakeNotifier{}
	c := checkerDeTeste(f)
	ctx := context.Background()
	chk := &store.SiteCheck{Name: "Site", URL: "https://x", Tier: "padrao", Alerting: true, State: "UP"}
	t0 := time.Now()
	c.onFailure(ctx, chk, Result{Diagnosis: DiagConnTimeout}, t0, 5*time.Minute)
	c.onFailure(ctx, chk, Result{Diagnosis: DiagConnTimeout}, t0.Add(40*time.Second), 5*time.Minute)
	c.onSuccess(ctx, chk, Result{OK: true, Status: 200, TotalMs: 300}, t0.Add(80*time.Second), 5*time.Minute)
	if chk.State != "UP" || chk.ConsecutiveFails != 0 || f.routed != 0 || chk.DownSince != nil {
		t.Fatalf("instabilidade curta não vira mensagem nem deixa marca: estado=%s falhas=%d avisos=%d downSince=%v",
			chk.State, chk.ConsecutiveFails, f.routed, chk.DownSince)
	}
	if chk.NextCheckAt.Sub(t0.Add(80*time.Second)) != 5*time.Minute {
		t.Errorf("de volta ao normal, o ritmo é o do tier: %v", chk.NextCheckAt)
	}
}

func TestRecuperacaoDizADuracaoRealDaQueda(t *testing.T) {
	f := &fakeNotifier{}
	c := checkerDeTeste(f)
	t0 := time.Date(2026, 9, 25, 20, 42, 0, 0, time.UTC)
	chk := &store.SiteCheck{Name: "Central", URL: "https://x", Tier: "basico", Alerting: true,
		State: "DOWN", DownSince: &t0, ConsecutiveFails: 4}
	c.onSuccess(context.Background(), chk, Result{OK: true, Status: 200, TotalMs: 900}, t0.Add(95*time.Second), 10*time.Minute)
	if f.routed != 1 || f.last.State != "resolved" || !f.last.Since.Equal(t0) {
		t.Fatalf("recuperação: avisos=%d estado=%s since=%v", f.routed, f.last.State, f.last.Since)
	}
	if chk.State != "UP" || chk.DownSince != nil {
		t.Fatalf("depois de voltar: estado=%s downSince=%v", chk.State, chk.DownSince)
	}
}

func TestSitemapEmProblemaReavaliaEm30s(t *testing.T) {
	now := time.Now()
	for estado, quer := range map[string]time.Duration{"DOWN": 30 * time.Second, "DEGRADADO": 30 * time.Second, "UP": 10 * time.Minute} {
		prox := now.Add(10 * time.Minute)
		chk := &store.SiteCheck{State: estado, NextCheckAt: &prox}
		ritmoDoSitemap(chk, now)
		if got := chk.NextCheckAt.Sub(now); got != quer {
			t.Errorf("sitemap %s: próximo em %v, quero %v", estado, got, quer)
		}
	}
}
