package ia

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// A tabela de referência vem embutida no binário e é importada uma vez por origem
// (SemearPrecosLLM). Ela envelhece: a tela avisa quando passa de diasReferenciaVelha.
//
//go:embed precos_referencia.json
var referenciaJSON []byte

const diasReferenciaVelha = 90

type referencia struct {
	Origem       string    `json:"origem"`
	ColetadaEm   string    `json:"coletada_em"`
	VigenteDesde time.Time `json:"vigente_desde"`
	Precos       []struct {
		Provedor     string   `json:"provedor"`
		Modelo       string   `json:"modelo"`
		Entrada      float64  `json:"entrada"`
		Saida        float64  `json:"saida"`
		CacheLeitura *float64 `json:"cache_leitura"`
		CacheEscrita *float64 `json:"cache_escrita"`
	} `json:"precos"`
}

func lerReferencia() (referencia, error) {
	var r referencia
	if err := json.Unmarshal(referenciaJSON, &r); err != nil {
		return r, fmt.Errorf("tabela de preços de referência ilegível: %w", err)
	}
	return r, nil
}

// SemearReferencia importa a tabela de referência se essa origem ainda não entrou.
func (h *Handler) SemearReferencia(ctx context.Context) (int, error) {
	r, err := lerReferencia()
	if err != nil {
		return 0, err
	}
	ps := make([]store.PrecoLLM, 0, len(r.Precos))
	for _, p := range r.Precos {
		ps = append(ps, store.PrecoLLM{
			Provedor: p.Provedor, Modelo: p.Modelo, EntradaPor1M: p.Entrada, SaidaPor1M: p.Saida,
			CacheLeituraPor1M: p.CacheLeitura, CacheEscritaPor1M: p.CacheEscrita, VigenteDesde: r.VigenteDesde,
		})
	}
	return h.st.SemearPrecosLLM(ctx, r.Origem, ps)
}

// InfoReferencia diz de quando é a tabela de referência em uso e se está velha. A data
// é a da coleta mais recente entre as origens "referencia-AAAA-MM" cadastradas.
type InfoReferencia struct {
	Referencia          string `json:"referencia"`
	DiasDesdeAtualizada int    `json:"dias_desde_atualizacao"`
	Desatualizada       bool   `json:"desatualizada"`
}

func infoReferencia(ps []store.PrecoLLM, agora time.Time) *InfoReferencia {
	var maisNova time.Time
	nome := ""
	for _, p := range ps {
		mes, ok := strings.CutPrefix(p.Origem, "referencia-")
		if !ok {
			continue
		}
		t, err := time.Parse("2006-01", mes)
		if err == nil && t.After(maisNova) {
			maisNova, nome = t, mes
		}
	}
	if nome == "" {
		return nil
	}
	dias := int(agora.Sub(maisNova).Hours() / 24)
	return &InfoReferencia{Referencia: nome, DiasDesdeAtualizada: dias, Desatualizada: dias > diasReferenciaVelha}
}
