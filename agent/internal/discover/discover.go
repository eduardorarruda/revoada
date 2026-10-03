// Package discover detecta serviços conhecidos (bancos, web, cache), units e Docker
// no host, para o server sugerir starter packs (P6.2).
package discover

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/process"
)

func decodeJSON(resp *http.Response, v any) error { return json.NewDecoder(resp.Body).Decode(v) }

// Send envia o Report ao gateway (POST /ingest/discovery, auth por chave).
func Send(ctx context.Context, gatewayURL, key string, r Report) error {
	body, _ := json.Marshal(r)
	url := strings.TrimRight(gatewayURL, "/") + "/ingest/discovery"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Revoada-Key", key)
	resp, err := (&http.Client{Timeout: 8 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gateway discovery status %d", resp.StatusCode)
	}
	return nil
}

// Service é um serviço conhecido detectado no host.
type Service struct {
	Kind   string `json:"kind"`   // mysql | postgres | redis | nginx | apache | php-fpm | mongodb | clickhouse | docker
	Detail string `json:"detail"` // nome do processo; no php-fpm, as versões ("8.1, 8.2")
	Source string `json:"source"` // process | port | docker
}

// Report é o inventário de descoberta enviado ao gateway.
type Report struct {
	Host             string    `json:"host"`
	Services         []Service `json:"services"`
	DockerPresent    bool      `json:"docker_present"`
	DockerContainers []string  `json:"docker_containers"`
}

// procNames mapeia nomes de processo (o `comm` do kernel, até 15 caracteres) para
// o tipo de serviço. Apache entra pelos dois nomes que ele tem no mundo: `httpd`
// (cPanel, RHEL) e `apache2` (Debian, Ubuntu). O PHP-FPM tem um terceiro caso —
// `php-fpm8.2`, `php-fpm83` — que não cabe num mapa; ver kindDoProcesso.
var procNames = map[string]string{
	"mysqld":            "mysql",
	"mariadbd":          "mysql",
	"postgres":          "postgres",
	"redis-server":      "redis",
	"nginx":             "nginx",
	"httpd":             "apache",
	"apache2":           "apache",
	"php-fpm":           "php-fpm",
	"mongod":            "mongodb",
	"clickhouse-server": "clickhouse",
}

// kindDoProcesso devolve o tipo de serviço de um nome de processo, ou "" quando o
// processo não é um serviço conhecido. É deliberadamente estreito: `php` (CLI) e
// `php-cgi` não são o servidor de aplicação, e não entram.
func kindDoProcesso(nome string) string {
	n := strings.ToLower(strings.TrimSpace(nome))
	if kind, ok := procNames[n]; ok {
		return kind
	}
	// Debian/Ubuntu instalam o binário como php-fpm8.2 e o Alpine como php-fpm83:
	// o sufixo é só a versão (dígitos e ponto). "php-fpm-qualquercoisa" não conta.
	if resto, ok := strings.CutPrefix(n, "php-fpm"); ok && resto != "" && soVersao(resto) {
		return "php-fpm"
	}
	return ""
}

func soVersao(s string) bool {
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return true
}

// As versões do PHP saem do cmdline do processo MESTRE do php-fpm, que traz o
// caminho do php-fpm.conf — e esse caminho carrega a versão em todas as
// distribuições que conhecemos:
//
//	cPanel:  php-fpm: master process (/opt/cpanel/ea-php82/root/etc/php-fpm.conf)
//	Debian:  php-fpm8.1: master process (/etc/php/8.1/fpm/php-fpm.conf)
//	Alpine:  php-fpm83: master process (/etc/php83/php-fpm.conf)
//
// As duas expressões são ANCORADAS: a do caminho exige que "php82" / "php/8.2" /
// "ea-php82" seja um segmento inteiro entre barras, e a do binário exige que o
// número venha colado a "php-fpm" no início da linha. Sem isso, uma conta de
// hospedagem chamada "php80site" (/home/php80site/etc/php-fpm.conf) viraria
// "PHP 8.0" no painel — e ninguém teria como perceber o engano.
//
// A imagem oficial do Docker (/usr/local/etc/php-fpm.conf) não traz versão nenhuma,
// e aí o serviço sai sem ela — melhor que inventar. Ler o cmdline é uma leitura
// de /proc, sem exec: o agente roda como revoada num servidor de produção e não vai
// disparar `php-fpm -v` a cada 5 minutos para descobrir um número.
var (
	regexVersaoNoCaminho = regexp.MustCompile(`(?:^|/)(?:ea-)?php[/-]?(\d)\.?(\d{1,2})(?:/|$)`)
	regexVersaoNoBinario = regexp.MustCompile(`^php-fpm(\d)\.?(\d{1,2})(?:$|[^0-9.])`)
	regexCaminhoDoConf   = regexp.MustCompile(`master process \((.*?)\)`)
)

// versaoPHP extrai "8.2" do cmdline de um processo mestre do php-fpm. Os workers
// ("php-fpm: pool www") não dizem a versão e devolvem ok=false.
func versaoPHP(cmdline string) (string, bool) {
	if !strings.Contains(cmdline, "master process") {
		return "", false
	}
	if m := regexVersaoNoBinario.FindStringSubmatch(cmdline); m != nil {
		return m[1] + "." + m[2], true
	}
	conf := regexCaminhoDoConf.FindStringSubmatch(cmdline)
	if conf == nil {
		return "", false
	}
	if m := regexVersaoNoCaminho.FindStringSubmatch(conf[1]); m != nil {
		return m[1] + "." + m[2], true
	}
	return "", false
}

// processo é o que classificar precisa saber de cada processo. Cmdline é uma
// função, não um valor: ler /proc/<pid>/cmdline de CADA processo do servidor a
// cada descoberta seria custo à toa — só os php-fpm pagam, porque só neles a
// linha de comando diz algo (a versão).
type processo struct {
	Nome    string
	Cmdline func() string
}

// classificar transforma a lista de processos nos serviços do host: um por tipo,
// em ordem estável. É pura de propósito — é o que os testes exercitam sem
// depender da máquina em que rodam.
func classificar(procs []processo) []Service {
	detalhe := map[string]string{}
	versoes := map[string]bool{}
	for _, p := range procs {
		kind := kindDoProcesso(p.Nome)
		if kind == "" {
			continue
		}
		if _, visto := detalhe[kind]; !visto {
			detalhe[kind] = strings.ToLower(strings.TrimSpace(p.Nome))
		}
		if kind == "php-fpm" && p.Cmdline != nil {
			if v, ok := versaoPHP(p.Cmdline()); ok {
				versoes[v] = true
			}
		}
	}
	// Várias versões de PHP convivem no mesmo servidor (o cPanel instala uma por
	// conta): todas aparecem, ordenadas, para o painel dizer "PHP 8.1, 8.2 e 8.4".
	if len(versoes) > 0 {
		detalhe["php-fpm"] = strings.Join(slices.Sorted(maps.Keys(versoes)), ", ")
	}
	out := make([]Service, 0, len(detalhe))
	for _, kind := range slices.Sorted(maps.Keys(detalhe)) {
		out = append(out, Service{Kind: kind, Detail: detalhe[kind], Source: "process"})
	}
	return out
}

// Run coleta um Report do host.
func Run(ctx context.Context, host string) Report {
	r := Report{Host: host}

	if procs, err := process.ProcessesWithContext(ctx); err == nil {
		lista := make([]processo, 0, len(procs))
		for _, p := range procs {
			name, _ := p.NameWithContext(ctx)
			lista = append(lista, processo{
				Nome: name,
				Cmdline: func() string {
					c, _ := p.CmdlineWithContext(ctx)
					return c
				},
			})
		}
		r.Services = classificar(lista)
	}

	// Docker: presença do socket + (best-effort) lista de containers rodando.
	if _, err := os.Stat(dockerSocket); err == nil {
		r.DockerPresent = true
		r.DockerContainers = dockerContainers(ctx)
	}
	return r
}

// dockerSocket é o socket unix padrão do Docker. É `var` (e não const) só para o
// teste conseguir apontar para um socket descartável e PROVAR que o número de
// goroutines não cresce a cada chamada.
var dockerSocket = "/var/run/docker.sock"

// dockerHTTP é um client de NÍVEL DE PACOTE, criado uma vez. Antes, dockerContainers
// montava um http.Transport NOVO a cada chamada e nunca chamava CloseIdleConnections():
// as goroutines de leitura/escrita do pool (readLoop/writeLoop) mantinham o Transport
// vivo, então o GC não recolhia nada e o fd contra o docker.sock ficava aberto.
//
// Não é hipótese: sendDiscovery roda no boot e a cada 5 min, ou seja +2 goroutines e
// +1 fd a cada 5 minutos. Medido na própria série do agente: 90 → 126 goroutines em
// 100 min, crescimento linear. Em 30 dias isso é ≈17.300 goroutines ≈276 MB — acima
// do MemoryMax=256M da unit, o que dá OOM, Restart=always e ciclo. O agente que
// deveria denunciar o problema do host vira o problema do host.
//
// Este é o MESMO padrão já aplicado em collect/containers.go: um Transport único,
// com pool pequeno e IdleConnTimeout, reaproveitado por todas as chamadas.
var dockerHTTP = &http.Client{
	Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", dockerSocket)
		},
		MaxIdleConns:    2,
		IdleConnTimeout: 30 * time.Second,
	},
	Timeout: 3 * time.Second,
}

// dockerContainers consulta o socket do Docker (GET /containers/json). Best-effort.
func dockerContainers(ctx context.Context) []string {
	client := dockerHTTP
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/json", nil)
	if err != nil {
		return nil
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil
	}
	var containers []struct {
		Names []string `json:"Names"`
		Image string   `json:"Image"`
	}
	if err := decodeJSON(resp, &containers); err != nil {
		return nil
	}
	var out []string
	for _, c := range containers {
		name := c.Image
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, name)
	}
	return out
}
