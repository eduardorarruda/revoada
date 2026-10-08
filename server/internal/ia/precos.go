package ia

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// ModeloSemPreco é um modelo que apareceu nas chamadas sem linha na tabela de preços.
type ModeloSemPreco struct {
	Provedor string `json:"provedor"`
	Modelo   string `json:"modelo"`
	Chamadas int64  `json:"chamadas"`
}

const janelaModelosVistos = 30 * 24 * time.Hour

// PrecosHTTP atende GET /api/ia/precos.
func (h *Handler) PrecosHTTP(w http.ResponseWriter, r *http.Request) {
	tab, ps, err := h.tabela(r.Context())
	if err != nil {
		erroInterno(w, err)
		return
	}
	agora := h.agora()
	f := Filtros{De: agora.Add(-janelaModelosVistos), Ate: agora}
	rows, err := h.ch.QueryJSON(r.Context(), fmt.Sprintf(`SELECT provedor, modelo, sum(chamadas) AS chamadas
		FROM genai_1m WHERE %s AND operacao IN %s AND modelo != '' GROUP BY provedor, modelo ORDER BY chamadas DESC`,
		f.ondeAgregado(), opsDeModelo))
	if err != nil {
		erroInterno(w, err)
		return
	}
	sem := []ModeloSemPreco{}
	for _, r := range rows {
		if _, ok := tab.Achar(texto(r["provedor"]), texto(r["modelo"]), agora); !ok {
			sem = append(sem, ModeloSemPreco{texto(r["provedor"]), texto(r["modelo"]), inteiro(r["chamadas"])})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"precos": ps, "modelos_sem_preco": sem, "referencia": infoReferencia(ps, agora)})
}

type novoPreco struct {
	Provedor          string     `json:"provedor"`
	Modelo            string     `json:"modelo"`
	EntradaPor1M      *float64   `json:"entrada_por_1m"`
	SaidaPor1M        *float64   `json:"saida_por_1m"`
	CacheLeituraPor1M *float64   `json:"cache_leitura_por_1m"`
	CacheEscritaPor1M *float64   `json:"cache_escrita_por_1m"`
	VigenteDesde      *time.Time `json:"vigente_desde"`
}

// validar devolve a mensagem de erro para a pessoa, ou "".
func (n novoPreco) validar() string {
	switch {
	case strings.TrimSpace(n.Modelo) == "":
		return "informe o modelo (ex.: gpt-4o-mini* — o * no fim cobre as versões com data)"
	case strings.Contains(strings.TrimSuffix(n.Modelo, "*"), "*"):
		return "o * só vale no fim do modelo"
	case n.EntradaPor1M == nil || n.SaidaPor1M == nil:
		return "informe o preço de entrada e o de saída (US$ por 1 milhão de tokens)"
	}
	for _, v := range []*float64{n.EntradaPor1M, n.SaidaPor1M, n.CacheLeituraPor1M, n.CacheEscritaPor1M} {
		if v != nil && (*v < 0 || *v > 10_000) {
			return "preço fora da faixa: use US$ por 1 milhão de tokens, entre 0 e 10.000"
		}
	}
	return ""
}

// CriarPrecoHTTP atende POST /api/ia/precos (admin; o middleware audita).
func (h *Handler) CriarPrecoHTTP(w http.ResponseWriter, r *http.Request) {
	var n novoPreco
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14)).Decode(&n); err != nil {
		http.Error(w, "corpo inválido: "+err.Error(), http.StatusBadRequest)
		return
	}
	if msg := n.validar(); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	// provedor/modelo vão crus: o store normaliza (minúsculas), num lugar só.
	p := store.PrecoLLM{Provedor: n.Provedor, Modelo: n.Modelo,
		EntradaPor1M: *n.EntradaPor1M, SaidaPor1M: *n.SaidaPor1M, CacheLeituraPor1M: n.CacheLeituraPor1M,
		CacheEscritaPor1M: n.CacheEscritaPor1M, VigenteDesde: h.agora().UTC().Truncate(time.Second), Origem: "manual"}
	if n.VigenteDesde != nil {
		p.VigenteDesde = n.VigenteDesde.UTC()
	}
	if c, ok := auth.ClaimsFrom(r.Context()); ok {
		p.CriadoPor = c.Name
	}
	criado, err := h.st.CriarPrecoLLM(r.Context(), p)
	if errors.Is(err, store.ErrPrecoDuplicado) {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err != nil {
		erroInterno(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, criado)
}

// ApagarPrecoHTTP atende DELETE /api/ia/precos/{id} (admin).
func (h *Handler) ApagarPrecoHTTP(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		http.Error(w, "id inválido", http.StatusBadRequest)
		return
	}
	switch err := h.st.ApagarPrecoLLM(r.Context(), id); {
	case errors.Is(err, store.ErrNotFound):
		http.Error(w, "preço não encontrado", http.StatusNotFound)
	case err != nil:
		erroInterno(w, err)
	default:
		w.WriteHeader(http.StatusNoContent)
	}
}
