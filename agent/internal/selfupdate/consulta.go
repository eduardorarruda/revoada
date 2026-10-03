package selfupdate

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Consulta ao painel: "qual versão eu deveria estar rodando?".
//
// A pergunta e o relato da tentativa anterior viajam na MESMA chamada. Não há
// rota separada de telemetria de propósito: um endpoint, de hora em hora, é
// menos superfície para autenticar e menos tráfego por host — e o relato chega
// junto com a prova de que o agente está vivo, que é o contexto que faz o relato
// significar alguma coisa no painel.

const timeoutConsulta = 20 * time.Second

// Resposta é o que o painel devolve.
type Resposta struct {
	// Atualizar já é a DECISÃO do painel: ele conhece o pin deste agente, o
	// desligamento global e o que de fato está publicado no dist. O agente ainda
	// confere por conta própria que a versão é mais nova (ver tick), mas quem
	// decide política é o painel — é lá que o operador segura a frota.
	Atualizar bool `json:"atualizar"`
	// Versao é a versão desejada (formato MAIOR.MENOR.PATCH).
	Versao string `json:"versao"`
	// URL é de onde baixar o binário. Precisa ser do MESMO host do painel — ver
	// mesmaOrigem.
	URL string `json:"url"`
	// SHA256 do artefato, em hexadecimal. Calculado pelo painel a partir do
	// arquivo que ele realmente serve, nunca digitado à mão.
	SHA256 string `json:"sha256"`
	// Tamanho em bytes, para limitar o download antes de ele começar.
	Tamanho int64 `json:"tamanho"`
	// Motivo explica um `atualizar: false` (pin, desligado, já em dia). Vai para o
	// log e para o painel — "não atualizou" sem motivo é indistinguível de "o
	// atualizador está quebrado".
	Motivo string `json:"motivo"`
	// Desinstalar manda este agente se REMOVER da máquina: o servidor foi apagado do
	// painel e ninguém tinha SSH para tirá-lo daqui. Tem precedência sobre tudo — quem
	// vai embora não baixa versão nova. Ver o pacote selfuninstall.
	Desinstalar bool `json:"desinstalar"`
}

type pedido struct {
	VersaoAtual string  `json:"versao_atual"`
	OS          string  `json:"os"`
	Arch        string  `json:"arch"`
	Hostname    string  `json:"hostname,omitempty"`
	Ultimo      *estado `json:"ultimo,omitempty"`
}

// consultar pergunta ao painel e devolve a resposta já validada.
func (a *Atualizador) consultar(ctx context.Context) (Resposta, error) {
	base, err := a.baseValida()
	if err != nil {
		return Resposta{}, err
	}
	so, arch := plataforma()
	corpo, err := json.Marshal(pedido{
		VersaoAtual: a.cfg.Versao,
		OS:          so,
		Arch:        arch,
		// O mesmo nome que vai no rótulo `host` das métricas: é ele que permite ao
		// painel dizer "este freio age NESTE servidor". Vazio (agente antigo, ou
		// configuração sem hostname) é omitido pelo `omitempty` e o painel trata
		// como "não sei" — nunca como "não casa".
		Hostname: strings.TrimSpace(a.cfg.Hostname),
		Ultimo:   a.ultimoEstado(),
	})
	if err != nil {
		return Resposta{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, timeoutConsulta)
	defer cancel()

	endereco := base + "/api/agent/update-check"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endereco, bytes.NewReader(corpo))
	if err != nil {
		return Resposta{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Mesma convenção do caminho de ingestão: a chave vai no cabeçalho, nunca na
	// URL. Chave em query string vaza para log de acesso, histórico de proxy e
	// Referer — e esta chave é a identidade do servidor.
	// A chave vai no cabeçalho, e o cliente abaixo é quem garante que ela não sai
	// desta origem: o http.DefaultClient seguiria um 302 para outro host levando este
	// cabeçalho junto (o Go só protege Authorization/Cookie). Ver httpcli.go.
	req.Header.Set("X-Revoada-Key", a.cfg.Key)

	resp, err := clientePainel(base).Do(req)
	if err != nil {
		return Resposta{}, fmt.Errorf("consultando %s: %w", endereco, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Teto na leitura do corpo de erro: uma resposta de erro é uma frase, e um
		// proxy confuso pode devolver megabytes de HTML.
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return Resposta{}, fmt.Errorf("painel respondeu %d: %s", resp.StatusCode, strings.TrimSpace(string(msg)))
	}

	var r Resposta
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64*1024)).Decode(&r); err != nil {
		return Resposta{}, fmt.Errorf("resposta do painel ilegível: %w", err)
	}
	if !r.Atualizar {
		return r, nil
	}
	if err := validar(r, base); err != nil {
		return Resposta{}, err
	}
	return r, nil
}

// validar recusa uma resposta incompleta ANTES de qualquer download.
//
// Recusar cedo importa: sem checksum não há verificação possível, e um agente
// que aceita "atualizar sem checksum" é um agente que instala o que quer que
// chegue pelo fio. Este é o ponto onde essa porta fica fechada.
func validar(r Resposta, base string) error {
	if r.Versao == "" {
		return fmt.Errorf("painel mandou atualizar sem dizer para qual versão")
	}
	if len(r.SHA256) != 64 {
		return fmt.Errorf("painel mandou atualizar sem um SHA-256 válido (%q) — nada será baixado", r.SHA256)
	}
	if !hexValido(r.SHA256) {
		return fmt.Errorf("SHA-256 anunciado não é hexadecimal")
	}
	if r.Tamanho <= 0 || r.Tamanho > tamanhoMaximo {
		return fmt.Errorf("tamanho anunciado (%d bytes) fora do aceitável", r.Tamanho)
	}
	if err := mesmaOrigem(r.URL, base); err != nil {
		return err
	}
	return nil
}

// mesmaOrigem exige que o binário venha do MESMO painel configurado neste host.
//
// A URL de download chega dentro da resposta, e resposta é dado, não ordem. Sem
// esta trava, quem conseguisse responder no lugar do painel — proxy corporativo
// que intercepta, DNS envenenado, painel comprometido — apontaria o agente para
// qualquer host da internet e escolheria o binário que vira root neste servidor.
// Amarrar ao esquema+host do `panel_url`, que veio do agent.yaml e é do
// operador, tira essa escolha de quem responde.
func mesmaOrigem(bruta, base string) error {
	u, err := url.Parse(bruta)
	if err != nil {
		return fmt.Errorf("URL do binário inválida: %w", err)
	}
	b, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("panel_url inválida: %w", err)
	}
	if !strings.EqualFold(u.Scheme, b.Scheme) || !strings.EqualFold(u.Host, b.Host) {
		return fmt.Errorf("o painel apontou o binário para outra origem (%s://%s) — só aceito baixar de %s://%s", u.Scheme, u.Host, b.Scheme, b.Host)
	}
	return nil
}

// baseValida normaliza o panel_url e recusa transporte inseguro.
//
// O que desce por este cano vira, no reinício seguinte, código executado como
// root neste servidor. Por HTTP puro, qualquer um no caminho troca o binário e o
// checksum junto — verificar o checksum que o próprio atacante mandou não prova
// nada. A exceção é o laço local, onde não há "caminho" para alguém estar no
// meio: é o que permite desenvolver sem TLS sem abrir a porta em produção.
func (a *Atualizador) baseValida() (string, error) {
	base := strings.TrimRight(strings.TrimSpace(a.cfg.PanelURL), "/")
	if base == "" {
		return "", fmt.Errorf("panel_url não configurado")
	}
	u, err := url.Parse(base)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("panel_url inválido: %q", a.cfg.PanelURL)
	}
	if !strings.EqualFold(u.Scheme, "https") && !local(u.Hostname()) {
		return "", fmt.Errorf("auto-atualização exige https no panel_url (recebi %q): por http qualquer um no caminho troca o binário e o checksum juntos", base)
	}
	return base, nil
}

func local(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func hexValido(s string) bool {
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') && (c < 'A' || c > 'F') {
			return false
		}
	}
	return true
}
