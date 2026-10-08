package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPrecosLLMIntegracao(t *testing.T) {
	s := abrirTeste(t)
	ctx := context.Background()
	origem := "referencia-teste-" + time.Now().Format("150405.000000")
	cache := 0.075
	vig := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	ps := []PrecoLLM{
		{Provedor: "openai", Modelo: "teste-a*", EntradaPor1M: 0.15, SaidaPor1M: 0.6, CacheLeituraPor1M: &cache, VigenteDesde: vig},
		{Provedor: "openai", Modelo: "teste-b*", EntradaPor1M: 2.5, SaidaPor1M: 10, VigenteDesde: vig},
	}
	if n, err := s.SemearPrecosLLM(ctx, origem, ps); err != nil || n != 2 {
		t.Fatalf("semear: %d %v", n, err)
	}
	if n, err := s.SemearPrecosLLM(ctx, origem, ps); err != nil || n != 0 {
		t.Fatalf("semear de novo: %d %v", n, err)
	}
	novo, err := s.CriarPrecoLLM(ctx, PrecoLLM{Provedor: "openai", Modelo: "teste-a*", EntradaPor1M: 0.1, SaidaPor1M: 0.4,
		Moeda: "USD", VigenteDesde: vig.AddDate(0, 6, 0), Origem: "manual", CriadoPor: "teste"})
	if err != nil || novo.ID == 0 || novo.CacheLeituraPor1M != nil {
		t.Fatalf("criar: %+v %v", novo, err)
	}
	if _, err := s.CriarPrecoLLM(ctx, novo); !errors.Is(err, ErrPrecoDuplicado) {
		t.Fatalf("duplicado: %v", err)
	}
	lista, err := s.ListarPrecosLLM(ctx)
	if err != nil {
		t.Fatal(err)
	}
	achou := 0
	for _, p := range lista {
		if p.Origem == origem && p.Modelo == "teste-a*" && p.CacheLeituraPor1M != nil && *p.CacheLeituraPor1M == 0.075 {
			achou++
		}
	}
	if achou != 1 {
		t.Fatalf("linha semeada com cache não voltou igual: %+v", lista)
	}
	if err := s.ApagarPrecoLLM(ctx, novo.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.ApagarPrecoLLM(ctx, novo.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("apagar de novo: %v", err)
	}
}
