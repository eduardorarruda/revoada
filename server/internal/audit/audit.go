// Package audit registra na trilha de auditoria toda alteração feita pela API:
// quem fez, quando, em qual recurso e com qual conteúdo.
//
// O registro acontece num ÚNICO middleware, aplicado nos wrappers por onde passam
// todas as rotas de escrita. Assim uma tela nova entra na auditoria de graça — não
// existe "esqueci de instrumentar este handler".
//
// Duas garantias de projeto:
//   - Nunca atrapalha a ação do usuário: a gravação é assíncrona e um erro de banco
//     só vira log, nunca um erro na resposta.
//   - Nunca guarda segredo: o corpo é redigido por nome de campo (senha, chave SSH,
//     serverkey, apikey, token...), em qualquer profundidade do JSON.
package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// maxBodyBytes limita o que a TRILHA guarda do corpo. Um payload gigante (dashboard
// grande) é truncado no registro — a trilha guarda o que importa, não um blob.
const maxBodyBytes = 64 << 10 // 64 KiB

// maxRequestBytes é o teto do corpo que o middleware aceita LER por inteiro para
// repassar ao handler. Alinhado ao client_max_body_size do nginx de produção (16m):
// acima disso a borda já recusaria, e segurar em memória o que a borda recusaria não
// serviria a ninguém.
//
// Por que os dois limites são diferentes: antes havia só um, e o middleware entregava
// ao handler o corpo TRUNCADO em 64 KiB. Todo POST/PUT maior chegava quebrado no meio
// do JSON e o handler devolvia um erro que MENTIA sobre a causa — provado em dev com
// `POST /api/query` levando um `metric` de 100 KB: a resposta era
// `400 metric obrigatório`, sendo que o campo estava lá, inteiro, no que o cliente
// mandou. Quem visse esse 400 iria depurar o cliente, e o problema estava na trilha.
// Agora o handler recebe o corpo inteiro e quem passa do teto leva 413 explícito.
const maxRequestBytes = 16 << 20 // 16 MiB

// redactedFields são os nomes de campo cujo VALOR nunca pode ir para a trilha.
// Comparação case-insensitive, em qualquer nível do JSON (inclusive dentro do
// `config` dos canais de notificação). Na dúvida, redija: perder detalhe numa
// auditoria é aceitável, vazar segredo não.
var redactedFields = map[string]bool{
	// nomes em português usados pela API (o nome em inglês sozinho deixava passar
	// a senha das conexões de banco e a da reautenticação — achado da revisão de
	// segurança da Etapa 6)
	"senha":            true,
	"codigo":           true, // TOTP e códigos de recuperação
	"segredo":          true,
	"desafio":          true, // token da 2ª etapa do login
	"enroll_token":     true,
	"chave_pkcs":       true, // chave privada
	"credencial":       true,
	"password":         true,
	"current_password": true,
	"new_password":     true,
	"secret":           true,
	"serverkey":        true,
	"server_key":       true,
	"apikey":           true,
	"api_key":          true,
	"bot_token":        true,
	"token":            true,
	"token_hash":       true,
	"authorization":    true,
	"headers":          true, // webhook: pode carregar Authorization
	"private_key":      true,
	"jwt_secret":       true,
	// O trecho procurado num expurgo É o dado vazado (token, CPF, chave). Guardá-lo
	// na trilha — que não tem poda — transformaria o procedimento de exclusão numa
	// nova cópia permanente do segredo. O handler anota no lugar uma impressão
	// digital (sha256 curto), que basta para comparar dois expurgos.
	"body_like": true,
}

const redactedMark = "••• redigido •••"

// Recorder grava entradas da trilha. Fica assíncrono para não somar latência à
// requisição do usuário.
type Recorder struct {
	st  *store.Store
	log *slog.Logger
	ch  chan store.AuditEntry
}

// NewRecorder cria o gravador e sobe o worker que consome a fila. O worker morre
// junto com ctx (shutdown).
func NewRecorder(ctx context.Context, st *store.Store, log *slog.Logger) *Recorder {
	r := &Recorder{st: st, log: log, ch: make(chan store.AuditEntry, 256)}
	go r.run(ctx)
	return r
}

func (r *Recorder) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case e := <-r.ch:
			// Contexto próprio: o da requisição já foi cancelado quando chegamos aqui.
			c, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			if err := r.st.InsertAudit(c, e); err != nil {
				r.log.Error("auditoria: falha ao gravar", "err", err, "path", e.Path, "ator", e.ActorName)
			}
			cancel()
		}
	}
}

// enqueue põe a entrada na fila sem bloquear. Se a fila encher (pico anormal),
// descarta e AVISA — melhor perder uma linha da trilha que travar a API.
func (r *Recorder) enqueue(e store.AuditEntry) {
	select {
	case r.ch <- e:
	default:
		r.log.Warn("auditoria: fila cheia, entrada descartada", "path", e.Path, "ator", e.ActorName)
	}
}

// Registrar grava uma entrada montada fora do HTTP (ex.: chamadas de ferramenta do
// MCP, que chegam todas pelo mesmo POST /mcp e precisam de uma linha cada).
func (r *Recorder) Registrar(e store.AuditEntry) {
	if r == nil {
		return
	}
	e.Payload, _ = redactValue(e.Payload).(map[string]any)
	r.enqueue(e)
}

// Middleware embrulha um handler e registra a alteração depois de executá-la.
// Só audita métodos de escrita — GET/HEAD/OPTIONS passam direto, sem custo.
func (r *Recorder) Middleware(next http.Handler) http.Handler {
	if r == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !isWrite(req.Method) {
			next.ServeHTTP(w, req)
			return
		}
		// Lê o corpo para auditar e o devolve INTACTO ao handler. Lemos um byte a mais
		// que o teto justamente para saber se estourou: sem isso, "leu exatamente o
		// limite" e "foi cortado" seriam indistinguíveis, que era o bug.
		var body []byte
		if req.Body != nil {
			var err error
			body, err = io.ReadAll(io.LimitReader(req.Body, maxRequestBytes+1))
			_ = req.Body.Close()
			if err != nil {
				http.Error(w, "não foi possível ler o corpo da requisição", http.StatusBadRequest)
				return
			}
			if len(body) > maxRequestBytes {
				// 413 explícito: o cliente precisa saber que o corpo é grande demais, e
				// não receber um erro de validação sobre um campo que ele mandou certo.
				http.Error(w, "corpo grande demais (máximo 16 MiB)", http.StatusRequestEntityTooLarge)
				return
			}
			req.Body = io.NopCloser(bytes.NewReader(body))
		}
		// O que vai para a TRILHA continua limitado a 64 KiB.
		auditado := body
		if len(auditado) > maxBodyBytes {
			auditado = auditado[:maxBodyBytes]
		}

		// Abre o bloco de anotações do RESULTADO antes de chamar o handler: é por
		// onde o handler devolve à trilha o que só ele sabe (quantas linhas saíram
		// num expurgo, quais mutations ficaram pendentes). Ver Annotate.
		req = req.WithContext(withNotes(req.Context()))

		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, req)

		resource, target := splitResource(req.URL.Path)
		e := store.AuditEntry{
			Method:   req.Method,
			Path:     req.URL.Path,
			Resource: resource,
			Target:   target,
			Status:   rec.status,
			Payload:  mergeNotes(req.Context(), redactBody(auditado)),
			IP:       clientIP(req),
		}
		// O ator sai das claims que o RequireAuth já colocou no contexto. Sem claims
		// (ex.: primeiro cadastro, login) registramos como anônimo — a ação em si
		// continua auditada.
		if claims, ok := auth.ClaimsFrom(req.Context()); ok {
			id := claims.Sub
			e.ActorID = &id
			e.ActorName = claims.Name
			e.ActorRole = claims.Role
		} else {
			e.ActorName = "(não autenticado)"
			e.ActorRole = "anônimo"
		}
		r.enqueue(e)
	})
}

func isWrite(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

// statusRecorder captura o código HTTP para a trilha distinguir o que funcionou
// do que foi barrado (403/404) — tentativa negada também é informação de auditoria.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (s *statusRecorder) WriteHeader(code int) {
	if !s.wroteHeader {
		s.status = code
		s.wroteHeader = true
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Write(b []byte) (int, error) {
	s.wroteHeader = true // 200 implícito
	return s.ResponseWriter.Write(b)
}

// Flush/Unwrap mantêm streaming (SSE/WS) e http.ResponseController funcionando.
func (s *statusRecorder) Flush() {
	if f, ok := s.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// splitResource extrai de "/api/site-checks/12" o recurso ("site-checks") e o alvo
// ("12"), para a tela filtrar por recurso e mostrar em quem a ação bateu.
func splitResource(path string) (resource, target string) {
	p := strings.TrimPrefix(strings.TrimPrefix(path, "/api/"), "/")
	parts := strings.Split(p, "/")
	if len(parts) == 0 || parts[0] == "" {
		return "", ""
	}
	resource = parts[0]
	if len(parts) > 1 {
		// O alvo é o 2º segmento (id/hostname/uid); segmentos além dele são a
		// sub-ação (ex.: hosts/<host>/delete) e entram junto para dar contexto.
		target = strings.Join(parts[1:], "/")
	}
	return resource, target
}

// redactBody decodifica o corpo JSON e devolve uma cópia com os segredos trocados
// por um marcador. Corpo vazio, não-JSON ou inválido nunca vira erro: a auditoria
// registra a ação de qualquer jeito, com uma nota do que houve.
func redactBody(body []byte) map[string]any {
	if len(bytes.TrimSpace(body)) == 0 {
		return map[string]any{}
	}
	var v any
	if err := json.Unmarshal(body, &v); err != nil {
		return map[string]any{"_nota": "corpo não-JSON ou truncado", "_bytes": len(body)}
	}
	switch red := redactValue(v).(type) {
	case map[string]any:
		return red
	default:
		// Corpo JSON que não é objeto (array, string...) — embrulha para caber no JSONB.
		return map[string]any{"_valor": red}
	}
}

// redactValue percorre o JSON recursivamente trocando os valores dos campos
// sensíveis. Além da lista exata, qualquer campo cujo nome CONTENHA uma das
// partes abaixo é redigido (defesa em profundidade para campos novos).
var partesSensiveis = []string{"senha", "password", "secret", "segredo", "token", "privad", "private"}

func sensivel(campo string) bool {
	k := strings.ToLower(campo)
	if redactedFields[k] {
		return true
	}
	for _, p := range partesSensiveis {
		if strings.Contains(k, p) {
			return true
		}
	}
	return false
}

// redactValue: objetos e arrays aninhados são cobertos (ex.: notify.config.apikey,
// provision.ssh.secret).
func redactValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, val := range t {
			if sensivel(k) {
				out[k] = redactedMark
				continue
			}
			out[k] = redactValue(val)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, val := range t {
			out[i] = redactValue(val)
		}
		return out
	default:
		return v
	}
}

// clientIP devolve o IP de origem, respeitando X-Forwarded-For (estamos atrás do
// Traefik) e caindo no RemoteAddr quando não há proxy.
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if first, _, ok := strings.Cut(xff, ","); ok {
			return strings.TrimSpace(first)
		}
		return strings.TrimSpace(xff)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
