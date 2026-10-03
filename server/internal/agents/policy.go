package agents

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// atorDe nomeia quem gravou a política (a trilha de auditoria guarda a requisição;
// isto guarda o autor DENTRO do próprio registro, que é o que a tela mostra).
func atorDe(r *http.Request) string {
	if c, ok := auth.ClaimsFrom(r.Context()); ok {
		return c.Name
	}
	return "?"
}

// Ligação do freio da auto-atualização ao banco + as rotas admin que o operam.
//
// Até aqui o `main` construía o UpdateHandler com FonteDePolitica = nil, e nil quer
// dizer "atualize sempre que houver versão nova". Como o deploy do painel é
// automático, o gatilho para trocar o binário de TODA a frota era um `git push`: em
// ~1h todo host com panel_url baixava a versão nova, sem canário e sem botão de
// pânico. Isto é o botão.

// PoliticaStore implementa FonteDePolitica sobre o Postgres.
type PoliticaStore struct{ st *store.Store }

// NewPoliticaStore liga a política ao banco. Passado ao NewUpdateHandler no main.
func NewPoliticaStore(st *store.Store) *PoliticaStore { return &PoliticaStore{st: st} }

// PoliticaDeAtualizacao resolve global + por chave numa resposta só. A ordem importa:
// o hold do host vem ANTES do pin da frota, porque "segure este servidor" é uma
// decisão mais específica e não pode ser apagada por uma configuração global.
//
// Erro de banco NÃO é engolido: o UpdateHandler trata erro segurando a frota por
// precaução (ver resolverPolitica). Um Postgres fora do ar não pode virar "atualiza
// todo mundo porque não consegui ler o pin".
func (p *PoliticaStore) PoliticaDeAtualizacao(ctx context.Context, serverkey string) (PoliticaAuto, error) {
	pol, err := p.st.AgentUpdatePolicyFor(ctx, serverkey)
	if err != nil {
		return PoliticaAuto{}, err
	}
	if pol.Hold {
		motivo := pol.HoldReason
		if motivo == "" {
			motivo = "atualização deste servidor segurada no painel"
		}
		return PoliticaAuto{Desligada: true, Motivo: motivo}, nil
	}
	g, err := p.st.GetAgentUpdateSettings(ctx)
	if err != nil {
		return PoliticaAuto{}, err
	}
	if g.Off {
		motivo := g.Motivo
		if motivo == "" {
			motivo = "auto-atualização desligada no painel"
		}
		return PoliticaAuto{Desligada: true, Motivo: motivo}, nil
	}
	return PoliticaAuto{Pin: g.Pin, Motivo: g.Motivo}, nil
}

// RegistrarRelato guarda o que o agente contou sobre a tentativa anterior.
func (p *PoliticaStore) RegistrarRelato(ctx context.Context, serverkey string, r RelatoAgente) error {
	return p.st.SaveAgentUpdateReport(ctx, serverkey, store.AgentUpdateReport{
		VersaoAtual: r.VersaoAtual, OS: r.OS, Arch: r.Arch, Hostname: r.Hostname,
		Estado: r.Estado, Motivo: r.Motivo, Erro: r.Erro,
		VersaoDesejada: r.VersaoDesejada, Quando: r.Quando,
	})
}

// --- rotas admin ---

// UpdatePolicy devolve a política GLOBAL (GET /api/agent/update-policy).
func (h *Handler) UpdatePolicy(w http.ResponseWriter, r *http.Request) {
	g, err := h.st.GetAgentUpdateSettings(r.Context())
	if err != nil {
		http.Error(w, "erro ao ler a política de atualização", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, g)
}

// SetUpdatePolicy grava a política GLOBAL (PUT /api/agent/update-policy).
func (h *Handler) SetUpdatePolicy(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Off    bool   `json:"off"`
		Pin    string `json:"pin"`
		Motivo string `json:"motivo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	pin := strings.TrimSpace(req.Pin)
	if pin != "" && !versaoPlausivel(pin) {
		// Pin com lixo dentro é pior que pin nenhum: o painel responderia "frota fixada
		// em <lixo>" para sempre e a frota pararia de atualizar sem ninguém entender.
		http.Error(w, "versão fixada inválida: use o formato 0.9.1", http.StatusBadRequest)
		return
	}
	if err := h.st.SetAgentUpdateSettings(r.Context(),
		store.AgentUpdateSettings{Off: req.Off, Pin: pin, Motivo: req.Motivo}, atorDe(r)); err != nil {
		http.Error(w, "erro ao gravar a política de atualização", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// SetUpdateHold segura (ou solta) a atualização de UM servidor
// (POST /api/agents/update-hold). Identifica a chave pelo `id` público da listagem —
// a serverkey não sai mais mascarada à toa, então a UI nem a tem em mãos.
func (h *Handler) SetUpdateHold(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID        string `json:"id"`
		Serverkey string `json:"serverkey"`
		Hold      bool   `json:"hold"`
		Motivo    string `json:"motivo"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	key, err := h.resolverChave(r.Context(), req.ID, req.Serverkey)
	if errors.Is(err, errChaveNaoEncontrada) {
		http.Error(w, "chave não encontrada", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "erro ao consultar chaves", http.StatusInternalServerError)
		return
	}
	if err := h.st.SetAgentUpdateHold(r.Context(), key, req.Hold, req.Motivo); err != nil {
		http.Error(w, "erro ao gravar o freio deste servidor", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// versaoPlausivel aceita "1.2.3" e "1.2.3-rc1" (o formato que o dist publica).
func versaoPlausivel(v string) bool {
	v = strings.TrimPrefix(v, "v")
	nucleo, sufixo, temSufixo := strings.Cut(v, "-")
	if temSufixo && sufixo == "" {
		return false // "1.2.3-" é digitação pela metade, não um pré-lançamento
	}
	partes := strings.Split(nucleo, ".")
	if len(partes) < 2 || len(partes) > 3 {
		return false
	}
	for _, p := range partes {
		if p == "" {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}
