package provision

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// --- fakes (sem SSH real, sem exec destrutivo) ---

// fakeSession devolve saídas programadas por comando e um erro opcional por comando.
type fakeSession struct {
	fp      string
	outputs map[string]string // substring do cmd -> stdout
	errs    map[string]error  // substring do cmd -> erro
	ran     []string
	closed  bool
}

func (s *fakeSession) HostKey() string { return s.fp }
func (s *fakeSession) Close() error    { s.closed = true; return nil }
func (s *fakeSession) Run(_ context.Context, cmd string) (string, error) {
	s.ran = append(s.ran, cmd)
	for sub, err := range s.errs {
		if strings.Contains(cmd, sub) {
			return s.outputs[sub], err
		}
	}
	for sub, out := range s.outputs {
		if strings.Contains(cmd, sub) {
			return out, nil
		}
	}
	return "", nil
}

type fakeRunner struct {
	sess       *fakeSession
	connectErr error
}

func (r *fakeRunner) Connect(_ context.Context, _ Target) (Session, error) {
	if r.connectErr != nil {
		return nil, r.connectErr
	}
	return r.sess, nil
}

// fakeKeyStore registra a serverkey gerada e a apagada (rollback).
type fakeKeyStore struct {
	created  bool
	deleted  string // serverkey passada a DeleteAgent (rollback)
	key      string
	hostname string
	err      error
}

func (k *fakeKeyStore) CreateAgent(_ context.Context, serverkey, _, hostname string) error {
	if k.err != nil {
		return k.err
	}
	k.created = true
	k.key = serverkey
	k.hostname = hostname
	return nil
}

func (k *fakeKeyStore) DeleteAgent(_ context.Context, serverkey string) error {
	k.deleted = serverkey
	return nil
}

func okSession() *fakeSession {
	return &fakeSession{
		fp: "SHA256:abc123",
		outputs: map[string]string{
			// O install.sh ecoa o hostname REAL no stdout (marcador REVOADA_HOSTNAME);
			// o orquestrador o extrai daqui — fonte autoritativa do elo com `hosts`.
			"curl":                "instalando...\nREVOADA_HOSTNAME=test-host\nok",
			"systemctl is-active": "active",
		},
	}
}

func collect(steps *[]Step) func(Step) {
	return func(s Step) { *steps = append(*steps, s) }
}

func TestProvisionSuccess(t *testing.T) {
	var steps []Step
	ks := &fakeKeyStore{}
	runner := &fakeRunner{sess: okSession()}
	p := Params{Name: "web01", Target: newTestTarget(), InstallURL: "https://p/install.sh", GatewayURL: "https://gw"}

	res, err := Provision(context.Background(), ks, runner, p, collect(&steps))
	if err != nil {
		t.Fatalf("esperava sucesso, veio erro: %v", err)
	}
	want := []struct{ step, status string }{
		{"ssh", StatusOK}, {"serverkey", StatusOK}, {"install", StatusOK}, {"service", StatusOK},
	}
	if len(steps) != len(want) {
		t.Fatalf("esperava %d steps, veio %d: %+v", len(want), len(steps), steps)
	}
	for i, w := range want {
		if steps[i].Step != w.step || steps[i].Status != w.status {
			t.Fatalf("step %d = %+v, esperava %s/%s", i, steps[i], w.step, w.status)
		}
	}
	if !ks.created || res.Serverkey == "" || ks.key != res.Serverkey {
		t.Fatalf("serverkey não gerada corretamente: created=%v key=%q res=%q", ks.created, ks.key, res.Serverkey)
	}
	// A serverkey recebe o nome amigável como rótulo COSMÉTICO (o hostname real só é
	// conhecido após o install). O elo autoritativo com `hosts` é res.Hostname, extraído
	// do stdout do install — é isso que faz o "Apagar servidor" casar por hostname.
	if ks.hostname != "web01" {
		t.Fatalf("rótulo da serverkey = %q, esperava web01 (nome amigável)", ks.hostname)
	}
	if res.Hostname != "test-host" {
		t.Fatalf("res.Hostname = %q, esperava test-host (do stdout do install)", res.Hostname)
	}
	if res.HostKeyFP != "SHA256:abc123" {
		t.Fatalf("host key fp = %q", res.HostKeyFP)
	}
	// A serverkey NÃO pode aparecer em nenhum Detail do progresso.
	for _, s := range steps {
		if res.Serverkey != "" && strings.Contains(s.Detail, res.Serverkey) {
			t.Fatalf("serverkey vazou no progresso: %+v", s)
		}
	}
}

func TestProvisionReuseServerkey(t *testing.T) {
	var steps []Step
	ks := &fakeKeyStore{}
	runner := &fakeRunner{sess: okSession()}
	p := Params{Name: "web01", Target: newTestTarget(), Serverkey: "chave-existente"}

	res, err := Provision(context.Background(), ks, runner, p, collect(&steps))
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if ks.created {
		t.Fatal("não deveria gerar nova serverkey no update")
	}
	if res.Serverkey != "chave-existente" {
		t.Fatalf("serverkey = %q, esperava reaproveitar a existente", res.Serverkey)
	}
	if steps[1].Step != "serverkey" || !strings.Contains(steps[1].Detail, "reaproveitada") {
		t.Fatalf("step serverkey inesperado: %+v", steps[1])
	}
}

func TestProvisionFailAtSSH(t *testing.T) {
	var steps []Step
	runner := &fakeRunner{connectErr: errors.New("connection refused")}
	_, err := Provision(context.Background(), &fakeKeyStore{}, runner, Params{Name: "x", Target: newTestTarget()}, collect(&steps))
	if err == nil {
		t.Fatal("esperava erro de SSH")
	}
	if len(steps) != 1 || steps[0].Step != "ssh" || steps[0].Status != StatusErr {
		t.Fatalf("esperava 1 step ssh/erro, veio: %+v", steps)
	}
}

func TestProvisionFailAtInstall(t *testing.T) {
	var steps []Step
	sess := okSession()
	sess.errs = map[string]error{"curl": errors.New("exit status 1")}
	runner := &fakeRunner{sess: sess}
	ks := &fakeKeyStore{}
	res, err := Provision(context.Background(), ks, runner, Params{Name: "x", Target: newTestTarget()}, collect(&steps))
	if err == nil {
		t.Fatal("esperava erro no install")
	}
	// ssh ok, serverkey ok, install erro → 3 steps, para no install.
	if len(steps) != 3 || steps[2].Step != "install" || steps[2].Status != StatusErr {
		t.Fatalf("sequência inesperada: %+v", steps)
	}
	// Rollback: a chave gerada nesta execução é apagada (sem órfã) e Result esvaziado.
	if ks.deleted != ks.key || ks.key == "" {
		t.Fatalf("esperava rollback da serverkey criada: created=%q deleted=%q", ks.key, ks.deleted)
	}
	if res.Serverkey != "" {
		t.Fatalf("Result.Serverkey deveria ser limpo após rollback, veio %q", res.Serverkey)
	}
}

// Reprovisionamento (chave reaproveitada) NÃO deve apagar a chave no rollback.
func TestProvisionReusedKeyNotRolledBack(t *testing.T) {
	var steps []Step
	sess := okSession()
	sess.errs = map[string]error{"curl": errors.New("exit status 1")}
	runner := &fakeRunner{sess: sess}
	ks := &fakeKeyStore{}
	_, err := Provision(context.Background(), ks, runner,
		Params{Name: "x", Target: newTestTarget(), Serverkey: "chave-existente"}, collect(&steps))
	if err == nil {
		t.Fatal("esperava erro no install")
	}
	if ks.created || ks.deleted != "" {
		t.Fatalf("chave reaproveitada não deveria ser criada nem apagada: created=%v deleted=%q", ks.created, ks.deleted)
	}
}

func TestProvisionFailAtService(t *testing.T) {
	var steps []Step
	sess := okSession()
	sess.outputs["systemctl is-active"] = "failed" // serviço não confirma ativo
	runner := &fakeRunner{sess: sess}
	ks := &fakeKeyStore{}
	_, err := Provision(context.Background(), ks, runner, Params{Name: "x", Target: newTestTarget()}, collect(&steps))
	if err == nil {
		t.Fatal("esperava erro no service")
	}
	if len(steps) != 4 || steps[3].Step != "service" || steps[3].Status != StatusErr {
		t.Fatalf("sequência inesperada: %+v", steps)
	}
	if !strings.Contains(steps[3].Detail, "failed") {
		t.Fatalf("detalhe do service deveria citar o estado: %+v", steps[3])
	}
	// CRÍTICO: falha no passo do serviço NÃO pode apagar a serverkey — o agent.yaml já
	// está no host e o agente pode estar no ar (Restart=always); apagar => 401.
	if ks.deleted != "" {
		t.Fatalf("serviço não deve fazer rollback da serverkey: deleted=%q", ks.deleted)
	}
}

func TestBuildInstallCommand(t *testing.T) {
	flags := AgentResourceFlags(256, 200, 40, 10, 128, 220, 2)
	cmd := BuildInstallCommand("https://painel/install.sh", "SERVERKEY123", "https://gateway:8090", flags)
	for _, sub := range []string{
		"curl -fsSL", "https://painel/install.sh", "sudo sh -s --",
		"--key", "SERVERKEY123", "--gateway", "https://gateway:8090",
		// sem --agent-url o install.sh não sabe de onde baixar o binário e aborta.
		"--agent-url", "https://painel/revoada-agent",
		// flags de limite de recurso injetadas (cerca da unit + soft caps).
		"--mem-max 256M", "--cpu-quota 40%", "--nice 10", "--tasks-max 128", "--mem-soft 220", "--max-procs 2",
	} {
		if !strings.Contains(cmd, sub) {
			t.Fatalf("comando não contém %q: %s", sub, cmd)
		}
	}
	// Sem flags extras, o comando continua válido (install.sh usa seus defaults).
	if bare := BuildInstallCommand("https://painel/install.sh", "K", "https://g", ""); strings.Contains(bare, "--mem-max") {
		t.Fatalf("comando sem flags não deveria conter limites: %s", bare)
	}
}

func TestBuildUninstallCommand(t *testing.T) {
	cmd := BuildUninstallCommand()
	for _, sub := range []string{
		"systemctl disable --now revoada-agent",
		"rm -f /etc/systemd/system/revoada-agent.service",
		"rm -rf /etc/revoada /var/lib/revoada-agent",
		"userdel revoada",
		"UNINSTALL_OK",
	} {
		if !strings.Contains(cmd, sub) {
			t.Fatalf("comando de uninstall não contém %q: %s", sub, cmd)
		}
	}
	// Autossuficiente: NÃO pode depender de curl/install.sh/--agent-url.
	for _, forbidden := range []string{"curl", "install.sh", "--agent-url"} {
		if strings.Contains(cmd, forbidden) {
			t.Errorf("uninstall não deveria conter %q (deve ser inline): %s", forbidden, cmd)
		}
	}
}

func TestDeriveAgentURL(t *testing.T) {
	cases := map[string]string{
		"https://painel/install.sh":        "https://painel/revoada-agent",
		"https://p.ex/sub/install.sh":      "https://p.ex/sub/revoada-agent",
		"http://127.0.0.1:5173/install.sh": "http://127.0.0.1:5173/revoada-agent",
		"install.sh":                       "revoada-agent",
	}
	for in, want := range cases {
		if got := deriveAgentURL(in); got != want {
			t.Errorf("deriveAgentURL(%q) = %q; quero %q", in, got, want)
		}
	}
}

func TestSecretNotEchoedOnInstallError(t *testing.T) {
	// stdout do install ecoa a serverkey; o Detail deve redigi-la.
	var steps []Step
	sess := okSession()
	sess.outputs["curl"] = "erro: falha com chave LEAKKEY visível"
	sess.errs = map[string]error{"curl": errors.New("boom")}
	runner := &fakeRunner{sess: sess}
	p := Params{Name: "x", Target: newTestTarget(), Serverkey: "LEAKKEY"}
	_, _ = Provision(context.Background(), &fakeKeyStore{}, runner, p, collect(&steps))
	last := steps[len(steps)-1]
	if strings.Contains(last.Detail, "LEAKKEY") {
		t.Fatalf("serverkey vazou no detalhe de erro: %+v", last)
	}
	if !strings.Contains(last.Detail, "***") {
		t.Fatalf("esperava redação (***) no detalhe: %+v", last)
	}
}

func newTestTarget() Target {
	return NewTarget("10.0.0.1", 22, "root", AuthPassword, []byte("senha"), "")
}

// TestProvisionHostnameFromInstall garante o novo elo: o hostname vem do stdout do
// install (marcador REVOADA_HOSTNAME), e SEM marcador res.Hostname fica "" — o handler
// então não grava alias/hostname-de-alvo com palpite (evita o host fantasma).
func TestProvisionHostnameMarkerAbsent(t *testing.T) {
	sess := &fakeSession{
		fp: "SHA256:abc",
		outputs: map[string]string{
			"curl":                "instalando... ok (sem marcador)",
			"systemctl is-active": "active",
		},
	}
	var steps []Step
	res, err := Provision(context.Background(), &fakeKeyStore{}, &fakeRunner{sess: sess},
		Params{Name: "Servidor Principal", Target: newTestTarget(), InstallURL: "https://p/install.sh"}, collect(&steps))
	if err != nil {
		t.Fatalf("esperava sucesso: %v", err)
	}
	if res.Hostname != "" {
		t.Fatalf("res.Hostname = %q, esperava \"\" (sem marcador => sem palpite/fantasma)", res.Hostname)
	}
}

func TestParseInstalledHostname(t *testing.T) {
	cases := map[string]string{
		"a\nREVOADA_HOSTNAME=srv-03\nb": "srv-03",
		"REVOADA_HOSTNAME= srv-x \n":    "srv-x",
		"nada aqui":                     "",
		"":                              "",
	}
	for in, want := range cases {
		if got := parseInstalledHostname(in); got != want {
			t.Errorf("parseInstalledHostname(%q) = %q, esperava %q", in, got, want)
		}
	}
}
