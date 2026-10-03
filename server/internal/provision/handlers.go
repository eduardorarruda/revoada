package provision

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/eduardorarruda/revoada/server/internal/sse"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// provisionTimeout limita a duração total de um (re)provisionamento — sem isso um
// install travado (lock de apt, prompt) deixaria o handler pendurado para sempre.
const provisionTimeout = 10 * time.Minute

// EmitEventFunc grava um evento de ciclo de vida na timeline (tabela events do
// ClickHouse). Injetada pelo main.go como um fecho sobre o chquery — assim o pacote
// provision não precisa conhecer o ClickHouse nem carregar a dependência. Best-effort:
// a implementação apenas loga em caso de falha e NUNCA aborta o provisionamento.
type EmitEventFunc func(ctx context.Context, host, kind, title string)

// Handler expõe as rotas admin de provisionamento SSH (Fase G). Todas registradas
// atrás do middleware admin no httpapi.
type Handler struct {
	st         *store.Store
	runner     Runner
	log        *slog.Logger
	installURL string
	gatewayURL string
	// emitEvent registra provisionamento/atualização bem-sucedidos na timeline do host.
	// Pode ser nil (ex.: testes) — os call sites checam antes de usar.
	emitEvent EmitEventFunc
}

func NewHandler(st *store.Store, runner Runner, installURL, gatewayURL string, log *slog.Logger, emitEvent EmitEventFunc) *Handler {
	return &Handler{st: st, runner: runner, log: log, installURL: installURL, gatewayURL: gatewayURL, emitEvent: emitEvent}
}

// recordLifecycleEvent grava, best-effort, um evento de ciclo de vida na timeline do
// agentFlags busca os limites de recurso configurados (ou os defaults) e os formata
// como flags do install.sh. Best-effort: em erro devolve "" (o install.sh cai nos
// próprios defaults embutidos), nunca aborta o provisionamento por causa disto.
func (h *Handler) agentFlags(ctx context.Context) string {
	l, err := h.st.GetAgentResourceLimits(ctx)
	if err != nil {
		h.log.Warn("limites de recurso do agente indisponíveis, usando defaults do install.sh", "err", err)
		return ""
	}
	return AgentResourceFlags(l.MemoryMaxMB, l.MemoryHighMB, l.CPUQuotaPct, l.Nice, l.TasksMax, l.MemSoftMB, l.MaxProcs)
}

// ResourceLimits (GET /api/agent/resource-limits) devolve os limites configurados.
func (h *Handler) ResourceLimits(w http.ResponseWriter, r *http.Request) {
	l, err := h.st.GetAgentResourceLimits(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, l)
}

// SetResourceLimits (PUT /api/agent/resource-limits) grava os limites. Aplicam-se a
// (re)provisionamentos futuros e ao comando de instalação manual exibido no painel —
// agentes já instalados só mudam ao reinstalar/reprovisionar ou editar a unit.
func (h *Handler) SetResourceLimits(w http.ResponseWriter, r *http.Request) {
	var l store.AgentResourceLimits
	if err := json.NewDecoder(r.Body).Decode(&l); err != nil {
		http.Error(w, "corpo inválido", http.StatusBadRequest)
		return
	}
	if err := h.st.SetAgentResourceLimits(r.Context(), l); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// Devolve o efetivo (após saneamento dos <=0 no store) para a UI refletir.
	saved, err := h.st.GetAgentResourceLimits(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, saved)
}

// host REAL. Só grava com hostname não-vazio (install antigo pode devolver "" — gravar
// deixaria um evento órfão sem host, invisível na timeline). Usa persistCtx (sem
// cancelamento do request) para não perder o registro se o admin fechar a aba no fim.
func (h *Handler) recordLifecycleEvent(ctx context.Context, hostname, kind, title string) {
	if h.emitEvent == nil || hostname == "" {
		return
	}
	h.emitEvent(ctx, hostname, kind, title)
}

// startReq é o corpo de POST /api/provision/start. secret = senha OU chave privada
// PEM. NUNCA é logado nem devolvido.
type startReq struct {
	Name     string `json:"name"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	AuthType string `json:"auth_type"`
	Secret   string `json:"secret"`
}

// Start provisiona um NOVO alvo via SSH, faz streaming do progresso via SSE e, ao
// terminar OK, cifra a credencial e persiste o alvo. Nunca devolve o secret.
func (h *Handler) Start(w http.ResponseWriter, r *http.Request) {
	var req startReq
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	req.Host = strings.TrimSpace(req.Host)
	req.User = strings.TrimSpace(req.User)
	at := AuthType(strings.TrimSpace(req.AuthType))
	if req.Name == "" || req.Host == "" || req.User == "" {
		http.Error(w, "name, host e user são obrigatórios", http.StatusBadRequest)
		return
	}
	if at != AuthPassword && at != AuthKey {
		http.Error(w, `auth_type deve ser "password" ou "key"`, http.StatusBadRequest)
		return
	}
	if req.Secret == "" {
		http.Error(w, "secret (senha ou chave privada) é obrigatório", http.StatusBadRequest)
		return
	}
	port := req.Port
	if port == 0 {
		port = 22
	}

	secret := []byte(req.Secret)
	target := NewTarget(req.Host, port, req.User, at, secret, "")

	flush, emit, ok := sseEmitter(w)
	if !ok {
		http.Error(w, "streaming não suportado", http.StatusInternalServerError)
		return
	}

	// Timeout global do provisionamento; as escritas finais usam contexto sem
	// cancelamento para não perder o registro se o admin fechar a aba no fim.
	provCtx, cancel := context.WithTimeout(r.Context(), provisionTimeout)
	defer cancel()
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer persistCancel()

	params := Params{
		Name:       req.Name,
		Target:     target,
		InstallURL: h.installURL,
		GatewayURL: h.gatewayURL,
		AgentFlags: h.agentFlags(provCtx),
	}
	res, err := Provision(provCtx, h.st, h.runner, params, emit)
	if err != nil {
		h.log.Warn("provisionamento falhou", "name", req.Name, "host", req.Host, "err", err)
		emitDone(emit, false, "")
		flush()
		return
	}

	// Sucesso: cifra a credencial e persiste o alvo para reprovisionar depois.
	blob, encErr := EncryptToBase64(secret)
	if encErr != nil {
		h.log.Error("cifrar credencial do cofre falhou", "err", encErr)
		emit(Step{Step: "persist", Status: StatusErr, Detail: "provisionado, mas falha ao guardar credencial no cofre"})
		emitDone(emit, true, res.Serverkey)
		flush()
		return
	}
	now := time.Now()
	_, dbErr := h.st.UpsertProvisionTarget(persistCtx, store.ProvisionTarget{
		Name:              req.Name,
		Host:              req.Host,
		Hostname:          res.Hostname,
		SSHPort:           port,
		SSHUser:           req.User,
		AuthType:          string(at),
		SecretBlob:        blob,
		HostKeyFP:         res.HostKeyFP,
		Serverkey:         res.Serverkey,
		Status:            "ok",
		LastProvisionedAt: &now,
	})
	if dbErr != nil {
		h.log.Error("persistir alvo de provisionamento falhou", "err", dbErr)
		emit(Step{Step: "persist", Status: StatusErr, Detail: "provisionado, mas falha ao registrar o alvo"})
	} else {
		// Grava o nome amigável digitado como alias do host REAL, para o painel
		// exibir "Servidor Principal" em vez do hostname técnico. Best-effort.
		if err := h.st.SetHostDisplayName(persistCtx, "default", res.Hostname, req.Name); err != nil {
			h.log.Error("gravar display_name do host falhou", "hostname", res.Hostname, "err", err)
		}
		// Corrige agents.hostname para o hostname REAL (era o nome amigável) — deixa a
		// chave pronta para a amarração de host (anti-spoofing) quando ligada. Best-effort.
		if err := h.st.SetAgentHostname(persistCtx, res.Serverkey, res.Hostname); err != nil {
			h.log.Error("corrigir hostname da serverkey falhou", "hostname", res.Hostname, "err", err)
		}
		emit(Step{Step: "persist", Status: StatusOK, Detail: "alvo salvo no cofre (host: " + res.Hostname + ")"})
	}
	// Timeline: marca o provisionamento na linha do tempo do host recém-adicionado.
	h.recordLifecycleEvent(persistCtx, res.Hostname, "provision", "Servidor provisionado, agente instalado")
	emitDone(emit, true, res.Serverkey)
	flush()
}

// Targets lista os alvos (sem secret; serverkey mascarada).
func (h *Handler) Targets(w http.ResponseWriter, r *http.Request) {
	list, err := h.st.ListProvisionTargets(r.Context())
	if err != nil {
		http.Error(w, "erro ao listar alvos", http.StatusInternalServerError)
		return
	}
	out := make([]map[string]any, 0, len(list))
	for _, t := range list {
		out = append(out, map[string]any{
			"id":                  t.ID,
			"name":                t.Name,
			"host":                t.Host,
			"hostname":            t.Hostname,
			"ssh_port":            t.SSHPort,
			"user":                t.SSHUser,
			"auth_type":           t.AuthType,
			"host_key_fp":         t.HostKeyFP,
			"status":              t.Status,
			"serverkey":           maskKey(t.Serverkey),
			"last_provisioned_at": t.LastProvisionedAt,
			"created_at":          t.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"targets": out})
}

// Update reexecuta o instalador num alvo existente usando a credencial do cofre
// (decrypt em memória) e reaproveitando a serverkey. Também via SSE.
func (h *Handler) Update(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	t, err := h.st.GetProvisionTarget(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			http.Error(w, "alvo não encontrado", http.StatusNotFound)
		} else {
			h.log.Error("buscar alvo de provisionamento falhou", "id", id, "err", err)
			http.Error(w, "erro ao buscar o alvo", http.StatusInternalServerError)
		}
		return
	}
	secret, err := DecryptFromBase64(t.SecretBlob)
	if err != nil {
		h.log.Error("decifrar credencial do cofre falhou", "id", id, "err", err)
		http.Error(w, "falha ao abrir a credencial do cofre", http.StatusInternalServerError)
		return
	}
	// A serverkey guardada pode ter sido revogada/apagada em /api/agents desde o
	// provisionamento — reinstalar com ela deixaria o host mudo com aparência de
	// sucesso. Valida antes; ausente/revogada => o Provision gera uma nova.
	reuseKey := t.Serverkey
	if reuseKey != "" {
		if active, aerr := h.st.AgentKeyActive(r.Context(), reuseKey); aerr == nil && !active {
			h.log.Warn("serverkey do alvo revogada/inexistente, será gerada uma nova", "id", id)
			reuseKey = ""
		}
	}

	target := NewTarget(t.Host, t.SSHPort, t.SSHUser, AuthType(t.AuthType), secret, t.HostKeyFP)

	flush, emit, ok := sseEmitter(w)
	if !ok {
		http.Error(w, "streaming não suportado", http.StatusInternalServerError)
		return
	}

	provCtx, cancel := context.WithTimeout(r.Context(), provisionTimeout)
	defer cancel()
	persistCtx, persistCancel := context.WithTimeout(context.WithoutCancel(r.Context()), 10*time.Second)
	defer persistCancel()

	params := Params{
		Name:       t.Name,
		Tenant:     t.TenantID,
		Target:     target,
		InstallURL: h.installURL,
		GatewayURL: h.gatewayURL,
		AgentFlags: h.agentFlags(provCtx),
		Serverkey:  reuseKey, // vazia => Provision gera e registra uma nova
	}
	res, provErr := Provision(provCtx, h.st, h.runner, params, emit)
	status := "ok"
	if provErr != nil {
		status = "erro"
		h.log.Warn("reprovisionamento falhou", "id", id, "host", t.Host, "err", provErr)
	}
	// Atualiza o registro (host key pode ter sido pinado na 1ª vez). Persiste a
	// chave EFETIVAMENTE instalada (res) — se uma nova foi gerada, é ela que vale.
	fp := res.HostKeyFP
	if fp == "" {
		fp = t.HostKeyFP
	}
	key := res.Serverkey
	if key == "" {
		key = t.Serverkey
	}
	// Persiste também o hostname REAL captado agora (o alvo pode ter sido criado
	// antes desta feature, com hostname vazio). UpsertProvisionTarget deduplica por
	// (tenant_id, host) e atualiza a MESMA linha — sem inventar outro método.
	hostname := res.Hostname
	if hostname == "" {
		hostname = t.Hostname
	}
	now := time.Now()
	t.Hostname = hostname
	t.HostKeyFP = fp
	t.Serverkey = key
	t.Status = status
	t.LastProvisionedAt = &now
	if _, dbErr := h.st.UpsertProvisionTarget(persistCtx, t); dbErr != nil {
		h.log.Error("atualizar alvo falhou", "id", id, "err", dbErr)
	}
	if err := h.st.SetHostDisplayName(persistCtx, t.TenantID, hostname, t.Name); err != nil {
		h.log.Error("gravar display_name do host falhou", "hostname", hostname, "err", err)
	}
	// Timeline: só marca "Agente atualizado" quando a atualização de fato deu certo
	// (provErr == nil). Falha de reprovisionamento não vira evento de sucesso.
	if provErr == nil {
		h.recordLifecycleEvent(persistCtx, hostname, "update", "Agente atualizado")
	}
	emitDone(emit, provErr == nil, "")
	flush()
}

// --- SSE helpers ---

// sseEmitter prepara o stream (pacote sse: headers, heartbeat de 15 s e escritas
// serializadas) e devolve (flush, emit, ok). flush() também para o heartbeat.
func sseEmitter(w http.ResponseWriter) (flush func(), emit func(Step), ok bool) {
	e, ok := sse.Abrir(w)
	if !ok {
		return nil, nil, false
	}
	emit = func(s Step) { _ = e.Emitir("", s) }
	return e.Fechar, emit, true
}

// emitDone fecha o stream com um evento final (a serverkey em claro só vai no
// caso de criação, uma única vez, como as demais rotas de serverkey do painel).
func emitDone(emit func(Step), success bool, serverkey string) {
	detail := "provisionamento concluído"
	status := StatusOK
	if !success {
		status = StatusErr
		detail = "provisionamento interrompido por erro"
	} else if serverkey != "" {
		detail = "provisionamento concluído (serverkey: " + serverkey + ")"
	}
	emit(Step{Step: "done", Status: status, Detail: detail})
}

func maskKey(k string) string {
	if len(k) <= 6 {
		if k == "" {
			return ""
		}
		return "***"
	}
	return k[:4] + "…" + k[len(k)-2:]
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
