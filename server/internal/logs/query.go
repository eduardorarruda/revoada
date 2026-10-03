// Package logs serve a API de consulta de logs (busca, histograma, contexto,
// padrões, live tail) e as métricas derivadas de busca.
package logs

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/chquery"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// hostPred devolve o predicado de escopo de host do usuário (labels['host'] IN (...) ou
// 1=0), ou "" para admin/chamada sem escopo. Conteúdo cru de log é sensível, então TODA
// consulta de logs passa por este filtro — inclusive metadados (serviços, fontes).
func hostPred(ctx context.Context) string {
	scope, ok := authz.ScopeFrom(ctx)
	if !ok {
		return ""
	}
	return scope.HostPredicate("labels['host']")
}

// andPred devolve " AND <pred>" ou "" — açúcar para concatenar num WHERE existente.
func andPred(ctx context.Context) string {
	if p := hostPred(ctx); p != "" {
		return " AND " + p
	}
	return ""
}

type Handler struct {
	ch     *chquery.Client
	st     *store.Store
	secret string
	authz  *authz.Resolver

	// meta guarda as listas que alimentam os SELETORES da tela (serviços, fontes).
	// Elas mudam devagar — um serviço novo aparece quando alguém sobe uma aplicação —
	// e custavam caro toda vez: medido em produção, `DISTINCT service` levava 563 ms
	// lendo 67 MiB, e `DISTINCT labels['source']` levava 432 ms lendo 1,14 GiB, os dois
	// disparados juntos a cada abertura da tela. A busca que o operador realmente pediu
	// custava 126 ms. Os seletores custavam dez vezes o resultado.
	metaMu    sync.Mutex
	metaCache map[string]metaEntrada
}

// metaEntrada é uma lista de metadados com validade.
type metaEntrada struct {
	valores []string
	expira  time.Time
}

// metaTTL: curto o bastante para um serviço novo aparecer sem ninguém recarregar
// nada, longo o bastante para trocar de aba não custar outra varredura.
const metaTTL = 60 * time.Second

// metaJanela é o recorte de tempo das listas de seletor.
//
// A tabela `logs` é particionada por dia (toYYYYMMDD(ts)), então um limite de tempo
// descarta partições inteiras antes de ler qualquer coisa: com 30 dias retidos, olhar
// 24 h lê ~1/30 do que a consulta sem recorte lia. O comentário do handler já dizia
// "serviços com logs NA JANELA" — a janela é que não existia.
const metaJanela = 24 * time.Hour

func NewHandler(ch *chquery.Client, st *store.Store, secret string, resolver *authz.Resolver) *Handler {
	return &Handler{ch: ch, st: st, secret: secret, authz: resolver, metaCache: map[string]metaEntrada{}}
}

// listaMeta responde uma lista de metadados a partir do cache, ou consulta e guarda.
// A chave inclui o predicado de escopo do usuário: dois usuários com permissões
// diferentes NÃO podem compartilhar a mesma lista — seria vazar nome de serviço de
// servidor alheio pelo seletor.
func (h *Handler) listaMeta(ctx context.Context, chave, sql, coluna string) ([]string, error) {
	agora := time.Now()
	h.metaMu.Lock()
	if e, ok := h.metaCache[chave]; ok && agora.Before(e.expira) {
		h.metaMu.Unlock()
		return e.valores, nil
	}
	h.metaMu.Unlock()

	rows, err := h.ch.QueryJSON(ctx, sql)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(rows))
	for _, row := range rows {
		if s, ok := row[coluna].(string); ok && s != "" {
			out = append(out, s)
		}
	}
	h.metaMu.Lock()
	h.metaCache[chave] = metaEntrada{valores: out, expira: agora.Add(metaTTL)}
	// Teto de segurança: a chave contém o predicado de escopo, então uma frota grande
	// com muitos usuários distintos poderia fazer o mapa crescer sem limite.
	if len(h.metaCache) > 256 {
		for k, e := range h.metaCache {
			if agora.After(e.expira) {
				delete(h.metaCache, k)
			}
		}
	}
	h.metaMu.Unlock()
	return out, nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = enc(w, v)
}

// filters descreve o recorte de uma consulta de logs.
type filters struct {
	tenant    string
	q         string
	service   string
	host      string
	container string
	source    string
	sevMin    int
	// sevSoDesconhecida recorta APENAS o que não tem nível declarado. É excludente com
	// sevMin: piso e recorte respondem a perguntas diferentes.
	sevSoDesconhecida bool
	from, to          time.Time
	traceID           string
	hostPred          string // predicado de escopo do usuário (enforcement); "" = admin/sistema
}

func parseFilters(r *http.Request) filters {
	q := r.URL.Query()
	f := filters{
		tenant:    "default",
		q:         q.Get("q"),
		service:   q.Get("service"),
		host:      q.Get("host"),
		container: q.Get("container"),
		source:    q.Get("source"),
		traceID:   q.Get("trace_id"),
		hostPred:  hostPred(r.Context()),
	}
	// "unknown" não é um piso: é o RECORTE das linhas que o agente não conseguiu
	// classificar (severity_num = 0). Sem esta opção elas eram invisíveis — todo piso
	// de nível as excluía em silêncio, e num servidor típico elas são o grosso do log
	// (medido no dev: 71 de 75 linhas de arquivo). Quem filtrava para tirar ruído de
	// debug apagava a maioria dos logs e concluía que o servidor tinha ficado quieto.
	if strings.EqualFold(strings.TrimSpace(q.Get("severity")), "unknown") {
		f.sevSoDesconhecida = true
	} else {
		f.sevMin = sevNum(q.Get("severity"))
	}
	f.from = parseTime(q.Get("from"), time.Now().Add(-time.Hour))
	f.to = parseTime(q.Get("to"), time.Now())
	return f
}

// where monta a cláusula WHERE segura a partir dos filtros.
func (f filters) where() string {
	var w []string
	w = append(w, "tenant_id = "+quote(f.tenant))
	w = append(w, fmt.Sprintf("ts >= toDateTime(%d)", f.from.Unix()))
	w = append(w, fmt.Sprintf("ts <= toDateTime(%d)", f.to.Unix()))
	if f.service != "" {
		w = append(w, "service = "+quote(f.service))
	}
	if f.host != "" {
		w = append(w, "labels['host'] = "+quote(f.host))
	}
	if f.container != "" {
		w = append(w, "labels['container'] = "+quote(f.container))
	}
	if f.source != "" {
		w = append(w, "labels['source'] = "+quote(f.source))
	}
	switch {
	case f.sevSoDesconhecida:
		w = append(w, "severity_num = 0")
	case f.sevMin > 0:
		w = append(w, fmt.Sprintf("severity_num >= %d", f.sevMin))
	}
	if f.q != "" {
		w = append(w, "body ILIKE "+quote("%"+f.q+"%"))
	}
	if f.traceID != "" {
		w = append(w, "trace_id = "+quote(f.traceID))
	}
	if f.hostPred != "" {
		w = append(w, f.hostPred)
	}
	return strings.Join(w, " AND ")
}

// noLog devolve o SETTINGS que impede o ClickHouse de guardar ESTA consulta em
// system.query_log.
//
// Caçar um vazamento criava uma segunda cópia dele: o termo buscado entra no SQL e
// o ClickHouse grava a consulta inteira em system.query_log (3 dias em produção),
// fora do alcance de qualquer expurgo do produto. Medido em dev: 4 s depois de
// buscar um canário, o termo estava legível em 2 linhas de system.query_log; com
// este SETTINGS, zero.
//
// Só se aplica quando a consulta carrega texto livre do usuário (`q`) — o resto das
// consultas de log continua registrado, para o operador seguir enxergando custo e
// erro no banco. Ideal seria mandar o termo como PARÂMETRO do ClickHouse
// (param_q + {q:String}), o que também reduziria a superfície de injeção; isso exige
// mudar o cliente em internal/chquery, que está fora do escopo desta frente.
func (f filters) noLog() string {
	if f.q == "" {
		return ""
	}
	return noLogSuffix
}

// Search devolve as linhas mais recentes que casam com os filtros.
func (h *Handler) Search(w http.ResponseWriter, r *http.Request) {
	f := parseFilters(r)
	limit := clampInt(r.URL.Query().Get("limit"), 200, 1000)
	sql := fmt.Sprintf(`SELECT toUnixTimestamp64Milli(ts) AS t, service, severity, severity_num,
		body, labels, trace_id, span_id FROM logs WHERE %s ORDER BY ts DESC LIMIT %d%s`, f.where(), limit, f.noLog())
	rows, err := h.ch.QueryJSON(r.Context(), sql)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

// Histogram devolve contagem por bucket de tempo, quebrada por servidor (host):
// cada linha é (t, host, total, erros). A UI empilha as séries por host e monta a
// legenda. Buckets sem rótulo de host caem em host="" (exibido como "sem servidor").
func (h *Handler) Histogram(w http.ResponseWriter, r *http.Request) {
	f := parseFilters(r)
	step := clampInt(r.URL.Query().Get("step"), bucketFor(f.from, f.to), 86400)
	sql := fmt.Sprintf(`SELECT toUnixTimestamp(toStartOfInterval(ts, INTERVAL %d SECOND)) AS t,
		labels['host'] AS host,
		count() AS c, countIf(severity_num >= 17) AS errors
		FROM logs WHERE %s GROUP BY t, host ORDER BY t, host%s`, step, f.where(), f.noLog())
	rows, err := h.ch.QueryJSON(r.Context(), sql)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"buckets": rows, "step": step})
}

// Context devolve ±N linhas ao redor de um instante para um serviço.
func (h *Handler) Context(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	service := q.Get("service")
	tsMs, _ := strconv.ParseInt(q.Get("ts"), 10, 64)
	if service == "" || tsMs == 0 {
		http.Error(w, "service e ts obrigatórios", http.StatusBadRequest)
		return
	}
	n := clampInt(q.Get("around"), 10, 200)
	tsExpr := fmt.Sprintf("fromUnixTimestamp64Milli(%d)", tsMs)
	cols := `toUnixTimestamp64Milli(ts) AS t, service, severity, severity_num, body, labels, trace_id, span_id`
	hp := andPred(r.Context()) // enforcement: contexto só de hosts permitidos
	sql := fmt.Sprintf(`
		SELECT * FROM (
			SELECT %[1]s FROM logs WHERE service=%[2]s%[5]s AND ts <= %[3]s ORDER BY ts DESC LIMIT %[4]d
			UNION ALL
			SELECT %[1]s FROM logs WHERE service=%[2]s%[5]s AND ts > %[3]s ORDER BY ts ASC LIMIT %[4]d
		) ORDER BY t ASC`, cols, quote(service), tsExpr, n, hp)
	rows, err := h.ch.QueryJSON(r.Context(), sql)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"rows": rows})
}

// Patterns agrupa linhas semelhantes: numeros e hashes viram placeholders e as
// linhas são contadas por template (drain simplificado em SQL).
func (h *Handler) Patterns(w http.ResponseWriter, r *http.Request) {
	f := parseFilters(r)
	// hex longo → <hex>; UUID → <uuid>; números → <n>
	tmpl := `replaceRegexpAll(replaceRegexpAll(replaceRegexpAll(body,
		'[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}', '<uuid>'),
		'[0-9a-fA-F]{16,}', '<hex>'),
		'[0-9]+', '<n>')`
	sql := fmt.Sprintf(`SELECT %[1]s AS pattern, count() AS c, any(body) AS sample,
		max(severity_num) AS sev FROM logs WHERE %[2]s GROUP BY pattern ORDER BY c DESC LIMIT 100%[3]s`,
		tmpl, f.where(), f.noLog())
	rows, err := h.ch.QueryJSON(r.Context(), sql)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"patterns": rows})
}

// Services lista os serviços com logs na janela recente (para o seletor da UI).
func (h *Handler) Services(w http.ResponseWriter, r *http.Request) {
	pred := andPred(r.Context())
	desde := time.Now().Add(-metaJanela).Unix()
	sql := fmt.Sprintf(`SELECT DISTINCT service FROM logs
		WHERE tenant_id='default' AND ts > toDateTime(%d)%s
		ORDER BY service LIMIT %d`, desde, pred, maxMetaValores)
	out, err := h.listaMeta(r.Context(), "services|"+pred, sql, "service")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"services": out})
}

// maxMetaValores é o teto de itens de um seletor. Um seletor com mais de mil entradas
// não é usável de qualquer forma, e sem teto uma explosão de cardinalidade (nome de
// serviço derivado de id de requisição, por exemplo) traria a lista inteira para a
// memória do servidor e para o navegador.
const maxMetaValores = 1000

// Sources lista as fontes de log distintas (journald/docker/syslog/kernel/file)
// presentes na janela recente, para alimentar o filtro "Fonte" da UI.
func (h *Handler) Sources(w http.ResponseWriter, r *http.Request) {
	// A janela caiu de 7 dias para 24 h: `labels['source']` obriga a ler a coluna Map
	// inteira das partições selecionadas, e eram 1,14 GiB por chamada. Fonte de log é
	// um conjunto praticamente fixo (journald, docker, file, syslog, kernel) — sete
	// dias nunca revelaram nada que um dia não revele.
	pred := andPred(r.Context())
	desde := time.Now().Add(-metaJanela).Unix()
	sql := fmt.Sprintf(`SELECT DISTINCT labels['source'] AS source FROM logs
		WHERE tenant_id='default' AND source != '' AND ts > toDateTime(%d)%s
		ORDER BY source LIMIT %d`, desde, pred, maxMetaValores)
	out, err := h.listaMeta(r.Context(), "sources|"+pred, sql, "source")
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sources": out})
}
