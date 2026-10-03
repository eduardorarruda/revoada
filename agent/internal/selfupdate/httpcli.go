package selfupdate

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

// Cliente HTTP do atualizador.
//
// # Por que não o http.DefaultClient
//
// As duas chamadas deste pacote (a consulta e o download) mandam a SERVERKEY deste
// host no cabeçalho `X-Revoada-Key`. O http.DefaultClient segue redirects, e o Go só
// remove cabeçalhos SENSÍVEIS conhecidos (`Authorization`, `Cookie`, `WWW-Authenticate`)
// ao saltar para outro host — um cabeçalho próprio como o nosso é reenviado íntegro
// para onde o redirect apontar.
//
// A trava `mesmaOrigem` (consulta.go) valida só a URL que veio no JSON da resposta.
// Um 302 acontece DEPOIS dessa validação: bastava o painel (ou qualquer coisa no
// caminho que consiga responder no lugar dele — proxy corporativo, CDN mal
// configurada, painel comprometido) devolver `Location: https://outro-host/...` para
// o agente entregar a chave de ingestão em claro a esse outro host. Provado com um
// servidor isolado: a outra origem recebeu a serverkey no primeiro salto.
//
// A chave é a identidade do servidor no painel — quem a tem escreve métricas e logs
// como se fosse ele, e consulta a atualização como se fosse ele. Vazá-la é pior do
// que não atualizar.
//
// Por isso: cliente próprio, com CheckRedirect reaplicando `mesmaOrigem` a CADA salto.
// Redirect dentro da mesma origem continua funcionando (é rotina: `/dist` → `/dist/`,
// normalização de barra final); redirect para fora simplesmente não é seguido, e o
// erro diz por quê.
//
// # Prazos
//
// `timeoutDownload` (15 min) é prazo TOTAL, e prazo total sozinho não protege de
// conexão travada: um servidor que aceita a conexão e nunca mais manda um byte segura
// o atualizador os 15 minutos inteiros com um `.parcial` de até 256 MB ocupando o
// disco do cliente. ResponseHeaderTimeout cobre o silêncio ANTES da resposta; o
// silêncio DEPOIS (corpo que para no meio) é coberto pelo vigia de progresso em
// estagio.go, que este Transport não consegue ver.
const (
	// maxRedirects é um teto de sanidade sobre o padrão do Go (10). Um painel
	// saudável não precisa de mais de um ou dois saltos dentro da própria origem.
	maxRedirects = 5
	// timeoutCabecalho é quanto esperamos pela LINHA DE STATUS depois de mandar a
	// requisição. Servidor que aceita a conexão TCP e não responde é o caso comum de
	// firewall/proxy no meio; sem este prazo, quem o pega é só o teto total.
	timeoutCabecalho = 30 * time.Second
)

// transporteAtualizador é de nível de pacote e reusado pelas duas chamadas: um
// Transport novo por requisição (o defeito já corrigido em collect/containers.go e
// discover/discover.go) deixa conexões ociosas com goroutines de leitura/escrita
// penduradas até o outro lado fechar.
var transporteAtualizador = &http.Transport{
	Proxy: http.ProxyFromEnvironment,
	DialContext: (&net.Dialer{
		Timeout:   10 * time.Second,
		KeepAlive: 30 * time.Second,
	}).DialContext,
	TLSHandshakeTimeout:   10 * time.Second,
	ResponseHeaderTimeout: timeoutCabecalho,
	ExpectContinueTimeout: time.Second,
	// O atualizador fala com UM host, uma vez por hora: mais que duas conexões
	// ociosas guardadas seria memória e fd parados num agente que existe para não
	// pesar no host do cliente.
	MaxIdleConns:    2,
	IdleConnTimeout: 90 * time.Second,
}

// clientePainel devolve um cliente que só segue redirects DENTRO da origem `base`
// (esquema+host do panel_url, que veio do agent.yaml e é do operador).
func clientePainel(base string) *http.Client {
	return &http.Client{
		Transport: transporteAtualizador,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("redirects demais (%d) — desisto em vez de seguir uma cadeia que não entendo", len(via))
			}
			if err := mesmaOrigem(req.URL.String(), base); err != nil {
				// A mensagem nomeia a chave de propósito: quem lê este log precisa
				// entender que o risco não era baixar o arquivo errado, era ENTREGAR a
				// credencial do servidor a quem respondeu.
				return fmt.Errorf("redirect recusado (o cabeçalho X-Revoada-Key iria junto): %w", err)
			}
			return nil
		},
	}
}

// ─── vigia de progresso do corpo da resposta ─────────────────────────────────

// vigiaProgresso cancela a requisição quando o corpo para de andar.
//
// É a metade que o Transport não cobre: ResponseHeaderTimeout vigia o silêncio ANTES
// da resposta, e depois disso o único prazo era o total de 15 minutos. Uma conexão
// que morre calada no meio do download (rota que sumiu, NAT que esqueceu a sessão)
// não devolve erro — ela simplesmente não entrega mais bytes —, então o `io.Copy`
// fica bloqueado e o `.parcial` de até 256 MB fica ocupando o disco do cliente pelo
// prazo inteiro. O vigia transforma isso num erro em 60s.
type vigiaProgresso struct {
	ultimo  atomic.Int64 // unix nano da última leitura com bytes
	travado atomic.Bool
	parar1  sync.Once
	fim     chan struct{}
}

func novoVigia(cancel context.CancelFunc) *vigiaProgresso {
	// A granularidade é a do menor prazo dividido por 4: acordar mais vezes que isso
	// só gastaria CPU num agente que existe para não pesar no host.
	return novoVigiaCom(cancel, timeoutSemProgresso, timeoutSemProgresso/4)
}

func novoVigiaCom(cancel context.CancelFunc, limite, intervalo time.Duration) *vigiaProgresso {
	v := &vigiaProgresso{fim: make(chan struct{})}
	v.ultimo.Store(time.Now().UnixNano())
	go func() {
		t := time.NewTicker(intervalo)
		defer t.Stop()
		for {
			select {
			case <-v.fim:
				return
			case agora := <-t.C:
				if agora.Sub(time.Unix(0, v.ultimo.Load())) < limite {
					continue
				}
				// Marca ANTES de cancelar: quem lê o erro do io.Copy precisa conseguir
				// distinguir "a conexão travou" de "o contexto do agente foi cancelado
				// porque o serviço está parando" — as duas coisas chegam como o mesmo
				// erro de contexto.
				v.travado.Store(true)
				cancel()
				return
			}
		}
	}()
	return v
}

// envolver devolve um leitor que marca o instante de cada leitura produtiva.
func (v *vigiaProgresso) envolver(r io.Reader) io.Reader { return &leitorVigiado{r: r, v: v} }

// parar encerra a goroutine do vigia. Idempotente: é chamada por defer e o download
// pode terminar por vários caminhos.
func (v *vigiaProgresso) parar() { v.parar1.Do(func() { close(v.fim) }) }

// travou diz se foi o vigia quem abortou (e não o encerramento do agente).
func (v *vigiaProgresso) travou() bool { return v.travado.Load() }

type leitorVigiado struct {
	r io.Reader
	v *vigiaProgresso
}

func (l *leitorVigiado) Read(p []byte) (int, error) {
	n, err := l.r.Read(p)
	if n > 0 {
		l.v.ultimo.Store(time.Now().UnixNano())
	}
	return n, err
}
