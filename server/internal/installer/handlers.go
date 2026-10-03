package installer

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// Handler serve o download do instalador. Rota admin-only (ver httpapi).
type Handler struct {
	st         *store.Store
	dist       fs.FS
	gatewayURL string
	panelURL   string
	log        *slog.Logger
}

// NewHandler monta o handler. `distDir` é o diretório publicado pelo deploy (o
// mesmo que o nginx serve); vazio desliga a geração com uma mensagem clara em vez
// de um erro obscuro na hora do download.
func NewHandler(st *store.Store, distDir, gatewayURL, panelURL string, log *slog.Logger) *Handler {
	var dist fs.FS
	if d := strings.TrimSpace(distDir); d != "" {
		dist = os.DirFS(d)
	}
	return &Handler{st: st, dist: dist, gatewayURL: gatewayURL, panelURL: panelURL, log: log}
}

type baixarReq struct {
	// OS é o sistema operacional de destino: linux, windows ou macos.
	OS string `json:"os"`
	// Hostname é a identificação do novo servidor: gera uma chave nova.
	Hostname string `json:"hostname"`
	// Serverkey reaproveita uma chave já existente (botão "Baixar instalador" na
	// lista). Tem precedência sobre Hostname — reinstalar um host não deve
	// espalhar chaves órfãs.
	Serverkey string `json:"serverkey"`
	// AgentID é o identificador PÚBLICO da chave (store.AgentID), o caminho novo do
	// botão "Baixar instalador". Existe porque a listagem `GET /api/agents` passou a
	// devolver a serverkey MASCARADA — antes ia a frota inteira em claro. Com o id, o
	// navegador não precisa mais ter o segredo em mãos para pedir o instalador: quem
	// o resolve é o servidor.
	AgentID string `json:"agent_id"`
	// EnrollToken gera o instalador UNIVERSAL a partir de um token já existente.
	EnrollToken string `json:"enroll_token"`
	Probe       bool   `json:"probe"`
}

// Baixar gera e devolve o instalador. É POST porque cria a chave de ingestão
// quando vem um hostname — uma escrita, que a trilha de auditoria registra.
func (h *Handler) Baixar(w http.ResponseWriter, r *http.Request) {
	var req baixarReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "payload inválido", http.StatusBadRequest)
		return
	}
	so, err := ParseSO(req.OS)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	var key, token, identificacao string
	if sk := strings.TrimSpace(req.EnrollToken); sk != "" {
		token, identificacao, err = h.resolverToken(ctx, sk)
	} else {
		key, identificacao, err = h.resolverChave(ctx, req)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	limites, err := h.st.GetAgentResourceLimits(ctx)
	if err != nil {
		// Cerca indisponível não impede instalar: cai nos defaults, que são os
		// mesmos embutidos no install.sh.
		h.log.Warn("instalador: limites de recurso indisponíveis, usando defaults", "err", err)
		limites = store.DefaultAgentResourceLimits()
	}

	arq, err := Gerar(h.dist, Opts{
		SO:            so,
		Identificacao: identificacao,
		Key:           key,
		EnrollToken:   token,
		GatewayURL:    h.gatewayURL,
		PanelURL:      h.panelURL,
		Probe:         req.Probe,
		Limites:       limites,
	})
	if err != nil {
		if errors.Is(err, ErrArtefatoAusente) {
			h.log.Error("instalador: artefato ausente no dist", "so", so, "err", err)
			http.Error(w, "o painel não encontrou o instalador deste sistema operacional entre os arquivos publicados. Rode o deploy de novo (ele publica os binários do agente) ou use o comando de instalação de uma linha.", http.StatusServiceUnavailable)
			return
		}
		h.log.Error("instalador: falha ao gerar", "so", so, "err", err)
		http.Error(w, "não foi possível gerar o instalador", http.StatusInternalServerError)
		return
	}

	// O arquivo carrega a chave de ingestão em claro: nada de cache em proxy,
	// navegador ou histórico de disco.
	w.Header().Set("Content-Type", arq.Tipo)
	w.Header().Set("Content-Disposition", `attachment; filename="`+arq.Nome+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(arq.Bytes)))
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(arq.Bytes); err != nil {
		h.log.Warn("instalador: download interrompido", "err", err)
	}
}

// resolverToken confere que o token de inscrição existe e vale, e devolve o nome
// do lote (que batiza o arquivo). Gerar um instalador universal a partir de um
// token revogado produziria um arquivo que falha em toda máquina — melhor recusar
// aqui, onde há quem ler o motivo.
func (h *Handler) resolverToken(ctx context.Context, token string) (string, string, error) {
	lista, err := h.st.ListEnrollTokens(ctx)
	if err != nil {
		return "", "", errors.New("não foi possível consultar os tokens")
	}
	for _, t := range lista {
		if t.Token == token {
			if t.Revoked {
				return "", "", errors.New("este instalador universal está revogado: reative-o ou crie outro")
			}
			return t.Token, t.Label, nil
		}
	}
	return "", "", errors.New("instalador universal não encontrado")
}

// resolverChave devolve a chave de ingestão a usar e o nome que batiza o arquivo:
// a chave existente quando o admin pediu reinstalação, ou uma nova quando é um
// servidor novo.
func (h *Handler) resolverChave(ctx context.Context, req baixarReq) (key, identificacao string, err error) {
	sk := strings.TrimSpace(req.Serverkey)
	id := strings.TrimSpace(req.AgentID)
	if sk != "" || id != "" {
		lista, err := h.st.ListAgents(ctx)
		if err != nil {
			return "", "", errors.New("não foi possível consultar as chaves")
		}
		for _, a := range lista {
			if (sk != "" && a.Serverkey == sk) || (id != "" && store.AgentID(a.Serverkey) == id) {
				if a.Revoked {
					return "", "", errors.New("esta chave está revogada: reative-a ou gere outra antes de baixar o instalador")
				}
				return a.Serverkey, a.Hostname, nil
			}
		}
		return "", "", errors.New("chave não encontrada")
	}

	nome := strings.TrimSpace(req.Hostname)
	if nome == "" {
		return "", "", errors.New("informe a identificação do servidor")
	}
	novo, err := auth.RandomToken()
	if err != nil {
		return "", "", errors.New("erro ao gerar a chave")
	}
	if err := h.st.CreateAgent(ctx, novo, "default", nome); err != nil {
		return "", "", errors.New("erro ao criar a chave")
	}
	return novo, nome, nil
}
