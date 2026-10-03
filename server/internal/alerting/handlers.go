package alerting

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// Handler expõe a API HTTP de alerting (regras CRUD, alertas, ack, preview).
type Handler struct {
	st   *store.Store
	eval *Evaluator
}

func NewHandler(st *store.Store, eval *Evaluator) *Handler { return &Handler{st: st, eval: eval} }

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// --- Regras ---

func (h *Handler) ListRules(w http.ResponseWriter, r *http.Request) {
	rules, err := h.st.ListAlertRules(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rules = filterRules(r.Context(), rules)
	if rules == nil {
		rules = []store.AlertRule{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"rules": rules})
}

// filterRules deixa o usuário comum ver regras globais (escopo vazio, informativas) e as
// que tocam ALGUM host que ele pode ver. Admin/sem escopo vê tudo.
func filterRules(ctx context.Context, rules []store.AlertRule) []store.AlertRule {
	scope, ok := authz.ScopeFrom(ctx)
	if !ok || scope.Admin {
		return rules
	}
	out := make([]store.AlertRule, 0, len(rules))
	for _, ru := range rules {
		alvos := hostsDaRegra(ru)
		if len(alvos) == 0 { // global de verdade: informativa
			out = append(out, ru)
			continue
		}
		for _, host := range alvos {
			if scope.CanView(host) {
				out = append(out, ru)
				break
			}
		}
	}
	return out
}

// hostsDaRegra devolve TODO servidor que a regra recorta — pelo campo `hosts` E pelo
// filtro de label `host`.
//
// Existe porque olhar só `ru.Hosts` vazava. Há dois jeitos de amarrar uma regra a um
// servidor no painel: a lista `hosts` e o filtro `filters['host']` (é o mesmo caminho que
// `ruleFilterSets` usa para montar a consulta — para o avaliador os dois são a mesma
// coisa). Uma regra escrita pelo segundo jeito tem `Hosts` vazio, era classificada como
// "global: informativa" e ia para TODO usuário autenticado.
//
// Provado com canário: um usuário com permissão de UM servidor só recebeu, na listagem
// de regras, uma regra com `filters={'host':'servidor-secreto-do-cliente-X'}` — hostname,
// métrica e limiar de outro cliente, num painel multi-cliente. Não é uma tela feia: é
// vazamento de dado entre clientes pela API de listagem.
//
// Nada de "na dúvida, mostra": se a regra nomeia um servidor por qualquer um dos dois
// caminhos, ela só aparece para quem pode ver aquele servidor.
func hostsDaRegra(ru store.AlertRule) []string {
	alvos := make([]string, 0, len(ru.Hosts)+1)
	for _, h := range ru.Hosts {
		if h != "" {
			alvos = append(alvos, h)
		}
	}
	if h := ru.Filters["host"]; h != "" {
		alvos = append(alvos, h)
	}
	return alvos
}

// filterAlerts deixa o usuário comum ver só alertas de hosts que ele pode ver. Alerta sem
// rótulo de host (ex.: métrica de aplicação) fica restrito a admin. Admin vê tudo.
func filterAlerts(ctx context.Context, alerts []store.AlertEvent) []store.AlertEvent {
	scope, ok := authz.ScopeFrom(ctx)
	if !ok || scope.Admin {
		return alerts
	}
	out := make([]store.AlertEvent, 0, len(alerts))
	for _, a := range alerts {
		host := a.Labels["host"]
		if host != "" && scope.CanView(host) {
			out = append(out, a)
		}
	}
	return out
}

func (h *Handler) CreateRule(w http.ResponseWriter, r *http.Request) {
	var req store.AlertRule
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Metric == "" {
		http.Error(w, "name e metric obrigatórios", http.StatusBadRequest)
		return
	}
	id, err := h.st.CreateAlertRule(r.Context(), req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id})
}

func (h *Handler) UpdateRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	var req store.AlertRule
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Metric == "" {
		http.Error(w, "name e metric obrigatórios", http.StatusBadRequest)
		return
	}
	// A regra de ausência é o piso do monitoramento: sem ela, um agente morto cala
	// TODAS as outras regras (valor ausente > 90 é `false`). Limiar, canais e
	// severidade seguem editáveis; desligar, não — o painel voltaria a mentir por
	// omissão. O boot religa de qualquer jeito (EnsureHeartbeatRule); recusar aqui é
	// só dizer a verdade na hora, em vez de fingir que salvou.
	if req.Metric == HeartbeatMetric && !req.Enabled {
		http.Error(w, "a regra \"servidor parou de reportar\" é de fábrica e não pode ser desligada", http.StatusConflict)
		return
	}
	if err := h.st.UpdateAlertRule(r.Context(), id, req); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "não encontrada", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Métrica/filtros podem ter mudado → fingerprints antigos ficam órfãos; limpa o estado.
	if h.eval != nil {
		h.eval.PurgeRule(id)
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": id})
}

func (h *Handler) DeleteRule(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	// Mesma razão do UpdateRule: a regra de ausência não é opcional. Apagá-la seria
	// só adiar — o próximo boot a recria — deixando o intervalo sem cobertura.
	if rules, err := h.st.ListAlertRules(r.Context()); err == nil {
		for _, ru := range rules {
			if ru.ID == id && ru.Metric == HeartbeatMetric {
				http.Error(w, "a regra \"servidor parou de reportar\" é de fábrica e não pode ser apagada", http.StatusConflict)
				return
			}
		}
	}
	if err := h.st.DeleteAlertRule(r.Context(), id); err != nil {
		http.Error(w, "não encontrado", http.StatusNotFound)
		return
	}
	if h.eval != nil {
		h.eval.PurgeRule(id) // esquece o estado em memória da regra removida
	}
	w.WriteHeader(http.StatusNoContent)
}

// Preview conta quantas vezes a regra teria disparado nos últimos N dias (default 7),
// sem persistir nada — usado para calibrar o threshold antes de salvar.
func (h *Handler) Preview(w http.ResponseWriter, r *http.Request) {
	var req store.AlertRule
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Metric == "" {
		http.Error(w, "metric obrigatório", http.StatusBadRequest)
		return
	}
	days := 7
	if d, err := strconv.Atoi(r.URL.Query().Get("days")); err == nil && d > 0 && d <= 90 {
		days = d
	}
	count, err := h.eval.Preview(r.Context(), req, days)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"fires": count, "days": days})
}

// --- Alertas (disparos) ---

func (h *Handler) ListAlerts(w http.ResponseWriter, r *http.Request) {
	onlyActive := r.URL.Query().Get("active") == "1"
	alerts, err := h.st.ListAlerts(r.Context(), onlyActive)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	alerts = filterAlerts(r.Context(), alerts)
	if alerts == nil {
		alerts = []store.AlertEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"alerts": alerts})
}

func (h *Handler) Ack(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	by := "desconhecido"
	if c, ok := auth.ClaimsFrom(r.Context()); ok {
		by = c.Name
	}
	if err := h.st.AckAlert(r.Context(), id, by); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "não encontrado ou já resolvido", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"acked_by": by})
}

// Resolve encerra um alerta ativo à mão (admin). Serve para o alerta que não vai
// se resolver sozinho porque o mundo mudou — um container removido de propósito,
// um servidor desativado. NÃO silencia a regra: se a condição voltar a valer, um
// alerta novo dispara.
func (h *Handler) Resolve(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	by := "desconhecido"
	if c, ok := auth.ClaimsFrom(r.Context()); ok {
		by = c.Name
	}
	if err := h.st.ResolveAlertManually(r.Context(), id, by); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "não encontrado ou já resolvido", http.StatusNotFound)
			return
		}
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"resolved_by": by})
}

// --- Containers ignorados ---

// ListIgnored: GET /api/alerts/ignored-containers (admin).
func (h *Handler) ListIgnored(w http.ResponseWriter, r *http.Request) {
	lista, err := h.st.ListIgnoredContainers(r.Context())
	if err != nil {
		http.Error(w, "não foi possível listar os containers ignorados", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"containers": lista})
}

// maxCorpoIgnorado: o corpo é {host, container}; nada legítimo passa de 1 KiB.
const maxCorpoIgnorado = 1 << 10

// Ignore: POST /api/alerts/ignored-containers {host, container} (admin). Para de
// alertar sobre o container e encerra, calado, os alertas abertos dele. Nada muda no
// servidor monitorado.
func (h *Handler) Ignore(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Host      string `json:"host"`
		Container string `json:"container"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxCorpoIgnorado)).Decode(&req); err != nil {
		http.Error(w, "corpo inválido: envie {host, container}", http.StatusBadRequest)
		return
	}
	host, ctr, err := validarIgnorado(req.Host, req.Container)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	by := "desconhecido"
	if c, ok := auth.ClaimsFrom(r.Context()); ok {
		by = c.Name
	}
	fechados, err := h.st.IgnoreContainer(r.Context(), host, ctr, by)
	if err != nil {
		http.Error(w, "não foi possível ignorar o container", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"host": host, "container": ctr, "alerts_closed": fechados})
}

// Unignore: DELETE /api/alerts/ignored-containers/{host}/{container} (admin). Volta a
// vigiar: se o container seguir parado, o alerta abre de novo em cerca de 1 minuto.
// O par vai no CAMINHO (não na query): a trilha de auditoria grava o caminho, e assim
// registra QUAL container voltou a ser vigiado.
func (h *Handler) Unignore(w http.ResponseWriter, r *http.Request) {
	host, ctr, err := validarIgnorado(r.PathValue("host"), r.PathValue("container"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.st.UnignoreContainer(r.Context(), host, ctr); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "este container não estava ignorado", http.StatusNotFound)
			return
		}
		http.Error(w, "não foi possível voltar a vigiar o container", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
