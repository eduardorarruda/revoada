package discover

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// socketDockerFalso sobe um servidor HTTP num socket unix descartável que responde
// como o `GET /containers/json` do dockerd, e aponta o pacote para ele.
func socketDockerFalso(t *testing.T, corpo any) {
	t.Helper()
	// O caminho do socket unix tem limite de ~104 bytes (sun_path); t.TempDir()
	// em alguns runners é longo demais, então usamos um nome curto dentro dele.
	caminho := filepath.Join(t.TempDir(), "d.sock")
	ln, err := net.Listen("unix", caminho)
	if err != nil {
		t.Skipf("sem socket unix neste ambiente: %v", err)
	}
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(corpo)
	}))
	srv.Listener = ln
	srv.Start()

	original := dockerSocket
	dockerSocket = caminho
	t.Cleanup(func() {
		dockerSocket = original
		srv.Close()
		// Fecha as conexões ociosas que o client de pacote guardou apontando para
		// ESTE socket — senão elas ficariam penduradas nos testes seguintes.
		dockerHTTP.CloseIdleConnections()
	})
}

// TestDockerContainersNaoVazaGoroutine é a prova do defeito corrigido: antes,
// dockerContainers criava um http.Transport NOVO por chamada e nunca o fechava, e
// cada chamada deixava para trás as goroutines readLoop/writeLoop do pool. Com
// sendDiscovery rodando a cada 5 min, isso era +2 goroutines e +1 fd a cada 5 min
// — 90→126 goroutines em 100 min medidos em produção, e OOM contra o MemoryMax=256M
// da unit em ~30 dias.
//
// Com o Transport único de pacote, N chamadas custam o mesmo que uma.
func TestDockerContainersNaoVazaGoroutine(t *testing.T) {
	socketDockerFalso(t, []map[string]any{
		{"Names": []string{"/api"}, "Image": "app:1.0"},
	})

	// Aquece: a primeira chamada é a que legitimamente cria o par de goroutines do
	// pool. O que não pode é a segunda, a terceira e a centésima criarem mais.
	if got := dockerContainers(t.Context()); len(got) != 1 || got[0] != "api" {
		t.Fatalf("dockerContainers = %v, quero [api]", got)
	}
	antes := goroutinesEstaveis()

	const n = 60
	for i := 0; i < n; i++ {
		if got := dockerContainers(t.Context()); len(got) != 1 {
			t.Fatalf("chamada %d devolveu %v", i, got)
		}
	}
	depois := goroutinesEstaveis()

	// O vazamento antigo era LINEAR: 60 chamadas somariam ~120 goroutines. Uma
	// folga de 8 cobre ruído do runtime/servidor de teste sem deixar passar o
	// crescimento por chamada.
	if delta := depois - antes; delta > 8 {
		t.Fatalf("goroutines cresceram %d em %d chamadas (antes=%d depois=%d): o Transport está sendo recriado e vazando o pool",
			delta, n, antes, depois)
	}
}

// goroutinesEstaveis espera as goroutines transitórias (handler do servidor de
// teste, fechamento de conexão) assentarem antes de contar.
func goroutinesEstaveis() int {
	anterior := -1
	for i := 0; i < 50; i++ {
		runtime.Gosched()
		time.Sleep(10 * time.Millisecond)
		n := runtime.NumGoroutine()
		if n == anterior {
			return n
		}
		anterior = n
	}
	return runtime.NumGoroutine()
}

// TestDockerContainersUsaClientDePacote garante que ninguém volte a montar um
// Transport por chamada: o client é o mesmo objeto em toda invocação.
func TestDockerContainersUsaClientDePacote(t *testing.T) {
	if dockerHTTP == nil || dockerHTTP.Transport == nil {
		t.Fatal("dockerHTTP precisa ser um client de pacote com Transport próprio")
	}
	tr, ok := dockerHTTP.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport inesperado: %T", dockerHTTP.Transport)
	}
	if tr.IdleConnTimeout == 0 {
		t.Fatal("sem IdleConnTimeout a conexão ociosa fica presa contra o docker.sock para sempre")
	}
	if dockerHTTP.Timeout == 0 {
		t.Fatal("sem Timeout uma resposta travada do dockerd seguraria a descoberta")
	}
}

// --- Apache e PHP-FPM ---
//
// Um servidor WHM (cPanel) típico roda as aplicações e o site em httpd + php-fpm, e o painel o
// mostrava com "1 serviço": só o MariaDB. Os testes abaixo fixam o reconhecimento
// dos dois SEM depender da máquina em que rodam — classificar recebe a lista de
// processos pronta.

func TestKindDoProcesso(t *testing.T) {
	casos := map[string]string{
		"httpd":      "apache",  // cPanel / RHEL
		"apache2":    "apache",  // Debian / Ubuntu
		"php-fpm":    "php-fpm", // cPanel (ea-php*), Docker oficial
		"php-fpm8.2": "php-fpm", // Debian / Ubuntu
		"php-fpm83":  "php-fpm", // Alpine
		"mariadbd":   "mysql",   // o mapa antigo continua valendo
		"nginx":      "nginx",
		"php":        "", // CLI do PHP não é servidor de aplicação
		"php-cgi":    "",
		"httpd-foo":  "",
		"bash":       "",
	}
	for nome, quer := range casos {
		if got := kindDoProcesso(nome); got != quer {
			t.Errorf("kindDoProcesso(%q) = %q, quero %q", nome, got, quer)
		}
	}
}

func TestVersaoPHP(t *testing.T) {
	casos := []struct {
		cmdline string
		quer    string
		ok      bool
	}{
		{"php-fpm: master process (/opt/cpanel/ea-php82/root/etc/php-fpm.conf)", "8.2", true}, // cPanel
		{"php-fpm: master process (/etc/php/8.1/fpm/php-fpm.conf)", "8.1", true},              // Debian
		{"php-fpm83: master process (/etc/php83/php-fpm.conf)", "8.3", true},                  // Alpine
		{"php-fpm8.4: master process (/etc/php/8.4/fpm/php-fpm.conf)", "8.4", true},
		{"php-fpm: master process (/etc/php/7.4/fpm/php-fpm.conf)", "7.4", true},
		{"php-fpm: master process (/usr/local/etc/php-fpm.conf)", "", false}, // imagem oficial: sem versão no caminho
		// Conta de hospedagem com "php80" no nome NÃO é versão: o número precisa ser
		// um segmento inteiro do caminho (ou estar colado ao nome do binário).
		{"php-fpm: master process (/home/php80site/etc/php-fpm.conf)", "", false},
		{"php-fpm: master process (/var/www/php7-legado/php-fpm.conf)", "", false},
		{"php-fpm: master process (/etc/php82/php-fpm.conf)", "8.2", true},
		{"php-fpm7.4: master process (/etc/php/7.4/fpm/php-fpm.conf)", "7.4", true},
		{"php-fpm: pool exemplo_com_br", "", false}, // pool não diz a versão
		{"", "", false},
	}
	for _, c := range casos {
		got, ok := versaoPHP(c.cmdline)
		if ok != c.ok || got != c.quer {
			t.Errorf("versaoPHP(%q) = (%q, %v), quero (%q, %v)", c.cmdline, got, ok, c.quer, c.ok)
		}
	}
}

// cmd devolve um processo cujo cmdline é lido sob demanda; `lido` conta as leituras
// para provar que só os php-fpm pagam esse custo.
func TestClassificarApacheEPHPFPM(t *testing.T) {
	lidos := 0
	proc := func(nome, cmdline string) processo {
		return processo{Nome: nome, Cmdline: func() string { lidos++; return cmdline }}
	}
	procs := []processo{
		proc("httpd", "/usr/sbin/httpd -k start"),
		proc("httpd", "/usr/sbin/httpd -k start"),
		proc("mariadbd", "mariadbd"),
		proc("php-fpm", "php-fpm: master process (/opt/cpanel/ea-php84/root/etc/php-fpm.conf)"),
		proc("php-fpm", "php-fpm: master process (/opt/cpanel/ea-php81/root/etc/php-fpm.conf)"),
		proc("php-fpm", "php-fpm: master process (/opt/cpanel/ea-php82/root/etc/php-fpm.conf)"),
		proc("php-fpm", "php-fpm: master process (/opt/cpanel/ea-php82/root/etc/php-fpm.conf)"), // repetida
		proc("php-fpm", "php-fpm: pool exemplo_com_br"),
		proc("sshd", "sshd: /usr/sbin/sshd"),
	}
	got := classificar(procs)
	quer := []Service{
		{Kind: "apache", Detail: "httpd", Source: "process"},
		{Kind: "mysql", Detail: "mariadbd", Source: "process"},
		{Kind: "php-fpm", Detail: "8.1, 8.2, 8.4", Source: "process"},
	}
	if len(got) != len(quer) {
		t.Fatalf("classificar devolveu %v, quero %v", got, quer)
	}
	for i := range quer {
		if got[i] != quer[i] {
			t.Errorf("serviço %d = %+v, quero %+v", i, got[i], quer[i])
		}
	}
	// Só os 5 php-fpm tiveram o cmdline lido: httpd, mariadbd e sshd não pagam.
	if lidos != 5 {
		t.Errorf("cmdline lido %d vezes, quero 5 (só os php-fpm)", lidos)
	}
}

func TestClassificarPHPFPMSemVersaoNoCaminho(t *testing.T) {
	procs := []processo{
		{Nome: "php-fpm", Cmdline: func() string { return "php-fpm: master process (/usr/local/etc/php-fpm.conf)" }},
		{Nome: "php-fpm", Cmdline: func() string { return "php-fpm: pool www" }},
	}
	got := classificar(procs)
	if len(got) != 1 || got[0].Kind != "php-fpm" || got[0].Detail != "php-fpm" {
		t.Fatalf("sem versão no caminho o detalhe volta ao nome do processo; got %+v", got)
	}
}

func TestClassificarVazio(t *testing.T) {
	if got := classificar(nil); len(got) != 0 {
		t.Fatalf("sem processos, sem serviços; got %+v", got)
	}
}
