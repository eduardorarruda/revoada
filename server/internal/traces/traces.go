// Package traces serve a API de consulta de traces: busca, waterfall de um trace,
// service map e exemplars (trace_ids de um bucket de tempo, para P5.4).
package traces

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/audit"
	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/chquery"
	"github.com/eduardorarruda/revoada/server/internal/logs"
)

type Handler struct{ ch *chquery.Client }

func NewHandler(ch *chquery.Client) *Handler { return &Handler{ch: ch} }

// hostPred devolve o predicado de escopo de host do usuário (labels['host'] IN (...) ou
// 1=0), ou "" para admin/sem escopo. Um trace pode cruzar vários hosts: filtramos por
// SPAN, então o usuário vê apenas os spans dos servidores que pode ver (os demais somem).
func (h *Handler) hostPred(ctx context.Context) string {
	scope, ok := authz.ScopeFrom(ctx)
	if !ok {
		return ""
	}
	return scope.HostPredicate("labels['host']")
}

func andPred(pred string) string {
	if pred == "" {
		return ""
	}
	return " AND " + pred
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func quote(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "'", "\\'")
	return "'" + s + "'"
}

func parseTime(s string, def time.Time) time.Time {
	if s == "" {
		return def
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 1e12 {
			return time.UnixMilli(n)
		}
		return time.Unix(n, 0)
	}
	return def
}

// Search lista traces agregados (raiz, duração total, spans, erro) por filtro.
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from := parseTime(q.Get("from"), time.Now().Add(-time.Hour))
	to := parseTime(q.Get("to"), time.Now())
	limit := 100
	if n, err := strconv.Atoi(q.Get("limit")); err == nil && n > 0 && n <= 500 {
		limit = n
	}
	where := []string{
		"tenant_id = 'default'",
		fmt.Sprintf("ts >= toDateTime(%d)", from.Unix()),
		fmt.Sprintf("ts <= toDateTime(%d)", to.Unix()),
	}
	if svc := q.Get("service"); svc != "" {
		where = append(where, fmt.Sprintf(
			"trace_id IN (SELECT trace_id FROM spans WHERE tenant_id='default' AND service=%s AND ts >= toDateTime(%d) AND ts <= toDateTime(%d))",
			quote(svc), from.Unix(), to.Unix()))
	}
	// Filtro por servidor: um trace casa se qualquer span dele tem labels['host']=host.
	if host := q.Get("host"); host != "" {
		where = append(where, fmt.Sprintf(
			"trace_id IN (SELECT trace_id FROM spans WHERE tenant_id='default' AND labels['host']=%s AND ts >= toDateTime(%d) AND ts <= toDateTime(%d))",
			quote(host), from.Unix(), to.Unix()))
	}
	// Enforcement por usuário: agrega só os spans dos hosts permitidos. Um trace só
	// aparece se tiver ≥1 span visível ao usuário, e seus números refletem esses spans.
	if pred := h.hostPred(r.Context()); pred != "" {
		where = append(where, pred)
	}
	var having []string
	if q.Get("status") == "error" {
		having = append(having, "has_error = 1")
	}
	if md := q.Get("min_duration"); md != "" {
		if v, err := strconv.ParseFloat(md, 64); err == nil {
			having = append(having, fmt.Sprintf("duration_ms >= %g", v))
		}
	}
	havingSQL := ""
	if len(having) > 0 {
		havingSQL = "HAVING " + strings.Join(having, " AND ")
	}
	// `partial` diz que a amostragem do gateway descartou parte deste trace. Importa
	// porque as duas colunas ao lado passam a ser CHUTE quando ele é 1: `root_name` sai
	// de argMin(ts), então com a raiz descartada o painel promove um filho a raiz; e
	// `duration_ms` vira um PISO, não a duração da requisição. Medido: raiz `GET /pedido`
	// de 50 ms descartada, filho `db.insert` de 900 ms mantido → a lista dizia
	// `root_name=db.insert, spans=1, duration_ms=900` com cara de trace completo.
	sql := fmt.Sprintf(`
		SELECT trace_id,
		       toUnixTimestamp64Milli(min(ts)) AS start_ms,
		       max(toUnixTimestamp64Milli(ts) + duration_ms) - min(toUnixTimestamp64Milli(ts)) AS duration_ms,
		       argMin(service, ts) AS root_service,
		       argMin(name, ts) AS root_name,
		       count() AS spans,
		       maxIf(1, status_code = 'ERROR') AS has_error,
		       max(labels['revoada.trace_partial'] = '1') AS partial
		FROM spans WHERE %s GROUP BY trace_id %s
		ORDER BY start_ms DESC LIMIT %d`,
		strings.Join(where, " AND "), havingSQL, limit)
	rows, err := h.ch.QueryJSON(r.Context(), sql)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"traces": rows})
}

// Get devolve todos os spans de um trace (dados do waterfall).
func (h *Handler) Get(w http.ResponseWriter, r *http.Request) {
	tid := r.PathValue("trace_id")
	if !isHex(tid) {
		http.Error(w, "trace_id inválido", http.StatusBadRequest)
		return
	}
	pred := h.hostPred(r.Context())
	sql := fmt.Sprintf(`SELECT toUnixTimestamp64Milli(ts) AS start_ms, duration_ms, service, name, kind,
		span_id, parent_span_id, status_code, status_msg, labels
		FROM spans WHERE tenant_id='default' AND trace_id=%s%s ORDER BY ts ASC`, quote(tid), andPred(pred))
	rows, err := h.ch.QueryJSON(r.Context(), sql)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Escopo restrito e nenhum span visível: 404 para não vazar a existência do trace.
	if pred != "" && len(rows) == 0 {
		http.Error(w, "trace não encontrado", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"trace_id": tid, "spans": rows})
}

// Logs devolve os logs correlacionados a um trace pelo trace_id exato (aproveita
// o bloom_filter da coluna trace_id na tabela logs). Mesmo formato de linha do
// endpoint /api/logs/search, para o frontend reaproveitar a normalização.
func (h *Handler) Logs(w http.ResponseWriter, r *http.Request) {
	tid := r.PathValue("trace_id")
	if !isHex(tid) {
		http.Error(w, "trace_id inválido", http.StatusBadRequest)
		return
	}
	sql := fmt.Sprintf(`SELECT toUnixTimestamp64Milli(ts) AS t, service, severity, severity_num,
		body, labels, trace_id, span_id
		FROM logs WHERE tenant_id='default' AND trace_id=%s%s ORDER BY ts ASC LIMIT 1000`, quote(tid), andPred(h.hostPred(r.Context())))
	rows, err := h.ch.QueryJSON(r.Context(), sql)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"trace_id": tid, "rows": rows})
}

// ServiceMap deriva as arestas serviço→serviço dos vínculos pai-filho.
func (h *Handler) ServiceMap(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from := parseTime(q.Get("from"), time.Now().Add(-time.Hour))
	to := parseTime(q.Get("to"), time.Now())
	// O lado "pai" do join é um subquery restrito à mesma janela e só às colunas
	// usadas (span_id, service). Sem isso o ClickHouse monta a hash do join sobre a
	// tabela spans inteira — todas as colunas, todo o histórico — e estoura o limite
	// de memória conforme os spans acumulam (MEMORY_LIMIT_EXCEEDED → 500 → mapa vazio).
	// Pai e filho de uma mesma chamada caem na mesma janela, então nada de relevante
	// se perde ao limitar o pai ao período.
	// Enforcement: restringe ambos os lados (pai e filho) aos hosts permitidos. O pai usa
	// labels['host'] direto; o filho, prefixado com o alias c.
	pred := h.hostPred(r.Context())
	predChild := ""
	if pred != "" {
		predChild = " AND " + strings.Replace(pred, "labels['host']", "c.labels['host']", 1)
	}
	sql := fmt.Sprintf(`
		SELECT p.service AS src, c.service AS dst, count() AS calls,
		       countIf(c.status_code = 'ERROR') AS errors,
		       round(avg(c.duration_ms), 1) AS avg_ms
		FROM spans AS c
		INNER JOIN (
			SELECT span_id, service FROM spans
			WHERE tenant_id='default' AND ts >= toDateTime(%d) AND ts <= toDateTime(%d)%s
		) AS p ON c.parent_span_id = p.span_id
		WHERE c.tenant_id='default' AND c.ts >= toDateTime(%d) AND c.ts <= toDateTime(%d)
		  AND c.service != p.service%s
		GROUP BY src, dst ORDER BY calls DESC LIMIT 200`,
		from.Unix(), to.Unix(), andPred(pred), from.Unix(), to.Unix(), predChild)
	rows, err := h.ch.QueryJSON(r.Context(), sql)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"edges": rows})
}

// Exemplars devolve trace_ids de um serviço numa janela (link métrica→trace, P5.4).
func (h *Handler) Exemplars(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	from := parseTime(q.Get("from"), time.Now().Add(-5*time.Minute))
	to := parseTime(q.Get("to"), time.Now())
	where := []string{
		"tenant_id='default'",
		fmt.Sprintf("ts >= toDateTime(%d)", from.Unix()),
		fmt.Sprintf("ts <= toDateTime(%d)", to.Unix()),
	}
	if svc := q.Get("service"); svc != "" {
		where = append(where, "service = "+quote(svc))
	}
	if md := q.Get("min_duration"); md != "" {
		if v, err := strconv.ParseFloat(md, 64); err == nil {
			where = append(where, fmt.Sprintf("duration_ms >= %g", v))
		}
	}
	if pred := h.hostPred(r.Context()); pred != "" {
		where = append(where, pred)
	}
	sql := fmt.Sprintf(`SELECT trace_id, service, name, duration_ms, status_code,
		toUnixTimestamp64Milli(ts) AS start_ms
		FROM spans WHERE %s ORDER BY duration_ms DESC LIMIT 20`, strings.Join(where, " AND "))
	rows, err := h.ch.QueryJSON(r.Context(), sql)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"exemplars": rows})
}

// Expurgo de traces. Ação destrutiva e irreversível, restrita a admin (imposto no
// httpapi).
//
// O que existia antes: só TRUNCATE TABLE spans. Para tirar UM segredo de dentro de
// um span, o preço era todos os traces, de todos os hosts, dos 15 dias de retenção.
// E span é dos lugares mais prováveis para um token aparecer: a instrumentação
// automática guarda headers, query strings e URLs inteiras dentro de labels.
//
// Agora o pedido aceita recorte {host, no_host, from, to, body_like} e vira uma
// mutation presa a esse recorte. O TRUNCATE continua existindo — para quando o
// operador realmente quer zerar a tabela — mas exige a frase de confirmação NO
// CORPO da requisição: até aqui a confirmação vivia só na tela, e uma tela não
// protege a rota.

// confirmTruncate é a frase exigida no corpo para zerar a tabela inteira.
const confirmTruncate = "APAGAR TODOS OS TRACES"

// clockSkew tolera relógio ligeiramente adiantado ao validar o fim da janela.
const clockSkew = 5 * time.Minute

// maxBodyLike limita o trecho procurado (mesmo motivo do expurgo de logs).
const maxBodyLike = 512

// noLogSuffix impede que o trecho procurado seja copiado para system.query_log
// pela própria consulta que o caça.
const noLogSuffix = " SETTINGS log_queries=0"

type purgeReq struct {
	Host     string `json:"host"`      // servidor alvo (labels['host'])
	NoHost   bool   `json:"no_host"`   // alvo = spans SEM rótulo de host
	From     string `json:"from"`      // início da janela (opcional)
	To       string `json:"to"`        // fim da janela (opcional; default = agora)
	BodyLike string `json:"body_like"` // trecho em nome/serviço/status/atributos
	DryRun   bool   `json:"dry_run"`   // só conta, não apaga (preview)
	Confirm  string `json:"confirm"`   // obrigatório APENAS para o TRUNCATE
}

// hasScope diz se o pedido tem algum recorte. Sem nenhum, o pedido é "apagar tudo".
func (p purgeReq) hasScope() bool {
	return strings.TrimSpace(p.Host) != "" || p.NoHost ||
		p.From != "" || p.To != "" || strings.TrimSpace(p.BodyLike) != ""
}

// escapeLike neutraliza os curingas do ILIKE: um token com `%` ou `_` casaria muito
// mais do que o operador viu no preview.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}

// purgeWhere monta a cláusula do recorte. FONTE ÚNICA: a contagem (o número que a
// tela mostra) e o DELETE usam esta mesma função.
func (p purgeReq) purgeWhere(to time.Time, from time.Time) string {
	w := []string{"tenant_id='default'"}
	if p.NoHost {
		w = append(w, "labels['host']=''")
	} else if h := strings.TrimSpace(p.Host); h != "" {
		w = append(w, "labels['host']="+quote(h))
	}
	if !from.IsZero() {
		w = append(w, fmt.Sprintf("ts >= toDateTime(%d)", from.Unix()))
	}
	w = append(w, fmt.Sprintf("ts < toDateTime(%d)", to.Unix()))
	if pat := strings.TrimSpace(p.BodyLike); pat != "" {
		// Um segredo num span pode estar no nome da operação, na mensagem de status ou
		// — o caso mais comum — dentro dos atributos (header, query string, URL).
		lit := quote("%" + escapeLike(pat) + "%")
		w = append(w, "(name ILIKE "+lit+" OR status_msg ILIKE "+lit+" OR service ILIKE "+lit+
			" OR arrayExists(v -> v ILIKE "+lit+", mapValues(labels))"+
			" OR arrayExists(k -> k ILIKE "+lit+", mapKeys(labels)))")
	}
	return strings.Join(w, " AND ")
}

// notPurgedTraces declara o que o expurgo de traces NÃO alcança. Expurgo que só
// limpa a origem é parcial, e o relatório tem de dizer isso.
func notPurgedTraces() []map[string]string {
	return []map[string]string{
		{"store": "logs", "retention": "30 dias",
			"note": "o mesmo trecho pode estar na linha de log correlacionada; use o expurgo de logs."},
		{"store": "metrics_1m / metrics_1h", "retention": "90 e 730 dias",
			"note": "métricas derivadas não são apagadas aqui."},
		{"store": "backup diário no MinIO (ClickHouse + pg_dump)", "retention": "sem expiração automática configurada",
			"note": "cópia SEM cifra; o expurgo não a alcança, precisa ser tratada fora do painel."},
		{"store": "system.query_log do ClickHouse", "retention": "3 dias",
			"note": "consultas de trace antigas podem citar o trecho; o expurgo de logs por conteúdo faz essa limpeza."},
		{"store": "system.mutations do ClickHouse", "retention": "até sair pelo limite de mutations guardadas (100 por tabela)",
			"note": "o comando do expurgo cita o trecho procurado; não é apagável por SQL."},
	}
}

// Purge apaga traces. Com recorte → mutation ALTER … DELETE presa ao recorte. Sem
// recorte → TRUNCATE, e só mediante a frase de confirmação no corpo.
func (h *Handler) Purge(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var req purgeReq
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // filtro ignorado em silêncio mente sobre o alcance
	if err := dec.Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		msg := err.Error()
		if i := strings.Index(msg, "unknown field "); i >= 0 {
			http.Error(w, "campo não reconhecido: "+strings.TrimPrefix(msg[i:], "unknown field "),
				http.StatusBadRequest)
			return
		}
		http.Error(w, "corpo inválido", http.StatusBadRequest)
		return
	}

	// Caminho "apagar tudo": exige a frase exata NO CORPO.
	if !req.hasScope() {
		if req.Confirm != confirmTruncate {
			http.Error(w, "para apagar TODOS os traces, envie confirm=\""+confirmTruncate+
				"\" no corpo; ou informe um recorte (host, janela, trecho)", http.StatusBadRequest)
			return
		}
		var deleted int64
		if rows, err := h.ch.QueryJSON(ctx, "SELECT count() AS c FROM spans"); err == nil && len(rows) > 0 {
			deleted = asCount(rows[0]["c"])
		}
		if req.DryRun {
			writeJSON(w, http.StatusOK, map[string]any{
				"dry_run": true, "rows": deleted, "scope": "TODOS os traces", "not_purged": notPurgedTraces()})
			return
		}
		if err := h.ch.Exec(ctx, "TRUNCATE TABLE spans"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		audit.Annotate(ctx, "deleted_rows", deleted)
		audit.Annotate(ctx, "escopo", "TODOS os traces (TRUNCATE)")
		writeJSON(w, http.StatusOK, map[string]any{
			"done": true, "deleted_rows": deleted, "scope": "TODOS os traces",
			"not_purged": notPurgedTraces()})
		return
	}

	// Caminho cirúrgico: recorte → mutation.
	to := parseTime(req.To, time.Now())
	from := parseTime(req.From, time.Time{})
	if to.After(time.Now().Add(clockSkew)) {
		http.Error(w, "a data de corte não pode estar no futuro", http.StatusBadRequest)
		return
	}
	if !from.IsZero() && !from.Before(to) {
		http.Error(w, "o início da janela precisa ser anterior ao fim", http.StatusBadRequest)
		return
	}
	if len(req.BodyLike) > maxBodyLike {
		http.Error(w, fmt.Sprintf("o trecho é longo demais (máx. %d caracteres)", maxBodyLike),
			http.StatusBadRequest)
		return
	}
	where := req.purgeWhere(to, from)
	suffix := ""
	if strings.TrimSpace(req.BodyLike) != "" {
		suffix = noLogSuffix
	}

	var deleted int64
	if rows, err := h.ch.QueryJSON(ctx, "SELECT count() AS c FROM spans WHERE "+where+suffix); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if len(rows) > 0 {
		deleted = asCount(rows[0]["c"])
	}
	scope := describeScope(req, from, to)
	if req.DryRun {
		writeJSON(w, http.StatusOK, map[string]any{
			"dry_run": true, "rows": deleted, "scope": scope, "not_purged": notPurgedTraces()})
		return
	}
	if err := h.ch.Exec(ctx, "ALTER TABLE spans DELETE WHERE "+where+suffix); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	audit.Annotate(ctx, "deleted_rows", deleted)
	audit.Annotate(ctx, "escopo", scope)
	if pat := strings.TrimSpace(req.BodyLike); pat != "" {
		// O trecho procurado É o dado vazado: a trilha guarda só a impressão digital.
		sum := sha256.Sum256([]byte(pat))
		audit.Annotate(ctx, "trecho_sha256", hex.EncodeToString(sum[:6]))
	}

	resp := map[string]any{"deleted_rows": deleted, "scope": scope, "not_purged": notPurgedTraces()}
	// Comando aceito não é dado apagado: acompanha a mutation por ~5s e diz a verdade
	// sobre o que ficou pendente (mesmo helper do expurgo de logs).
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := logs.MutationStatus(ctx, h.ch, "spans", "")
		if err != nil {
			resp["done"] = false
			resp["mutation_error"] = err.Error()
			writeJSON(w, http.StatusOK, resp)
			return
		}
		switch {
		case st.FailReason != "":
			resp["done"], resp["mutation_id"], resp["fail_reason"] = false, st.MutationID, st.FailReason
			audit.Annotate(ctx, "mutation_falhou", st.FailReason)
			writeJSON(w, http.StatusOK, resp)
			return
		case st.Done:
			resp["done"] = true
			writeJSON(w, http.StatusOK, resp)
			return
		case time.Now().After(deadline):
			resp["done"], resp["mutation_id"] = false, st.MutationID
			audit.Annotate(ctx, "mutation_pendente", st.MutationID)
			writeJSON(w, http.StatusOK, resp)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// describeScope devolve o recorte em português, para a tela repetir ao operador o
// que vai sair e a trilha guardar a mesma frase.
func describeScope(p purgeReq, from, to time.Time) string {
	var b strings.Builder
	switch {
	case p.NoHost:
		b.WriteString("spans SEM rótulo de servidor")
	case strings.TrimSpace(p.Host) != "":
		b.WriteString("servidor " + strings.TrimSpace(p.Host))
	default:
		b.WriteString("todos os servidores")
	}
	if !from.IsZero() {
		b.WriteString(", de " + from.UTC().Format(time.RFC3339))
	}
	b.WriteString(", até " + to.UTC().Format(time.RFC3339))
	if strings.TrimSpace(p.BodyLike) != "" {
		b.WriteString(", só spans que contêm o trecho informado (nome, serviço, status ou atributos)")
	}
	return b.String()
}

// asCount extrai a contagem do count() do ClickHouse (UInt64 vem como string no JSON).
func asCount(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	case json.Number:
		n, _ := t.Int64()
		return n
	}
	return 0
}

func isHex(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		//nolint:staticcheck // QF1001: a negação direta ("não é dígito hex") é mais legível que De Morgan
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
