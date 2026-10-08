package genai

import (
	"math"
	"testing"
	"time"
)

func f64(v float64) *float64 { return &v }

func perto(a, b float64) bool { return math.Abs(a-b) < 1e-12 }

func TestCustoSeparaCacheDaEntrada(t *testing.T) {
	p := Preco{Entrada: 3, Saida: 15, CacheLeitura: f64(0.30), CacheEscrita: f64(3.75)}
	// 170 de entrada = 20 novos + 100 lidos + 50 escritos; 30 de saída.
	u := Uso{Entrada: 170, Saida: 30, CacheLeitura: 100, CacheEscrita: 50}
	quer := (20*3 + 100*0.30 + 50*3.75 + 30*15) / 1e6
	if got := Custo(u, p); !perto(got, quer) {
		t.Fatalf("custo = %v, quer %v", got, quer)
	}
}

func TestCustoSemPrecoDeCacheCobraComoEntrada(t *testing.T) {
	p := Preco{Entrada: 2, Saida: 8}
	u := Uso{Entrada: 1000, Saida: 0, CacheLeitura: 400}
	if got := Custo(u, p); !perto(got, 1000*2/1e6) {
		t.Fatalf("custo = %v", got)
	}
}

func TestCustoEhLinear(t *testing.T) {
	p := Preco{Entrada: 0.15, Saida: 0.60, CacheLeitura: f64(0.075)}
	a := Uso{Entrada: 120, Saida: 30, CacheLeitura: 100}
	b := Uso{Entrada: 5000, Saida: 900, CacheLeitura: 0}
	soma := Uso{Entrada: a.Entrada + b.Entrada, Saida: a.Saida + b.Saida, CacheLeitura: a.CacheLeitura + b.CacheLeitura}
	if !perto(Custo(a, p)+Custo(b, p), Custo(soma, p)) {
		t.Fatal("custo do agregado diferente da soma dos custos")
	}
}

func TestTabelaAcharEspecificidadeEProvedor(t *testing.T) {
	d := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	tab := NovaTabela([]Preco{
		{Provedor: "openai", Modelo: "gpt-4o*", Entrada: 2.5, VigenteDesde: d},
		{Provedor: "openai", Modelo: "gpt-4o-mini*", Entrada: 0.15, VigenteDesde: d},
		{Provedor: "openai", Modelo: "gpt-4o-mini-2024-07-18", Entrada: 0.14, VigenteDesde: d},
		{Provedor: "anthropic", Modelo: "claude-sonnet-4*", Entrada: 3, VigenteDesde: d},
	})
	agora := d.AddDate(0, 6, 0)
	casos := []struct {
		provedor, modelo string
		quer             float64
		ok               bool
	}{
		{"openai", "gpt-4o-2024-08-06", 2.5, true},
		{"openai", "gpt-4o-mini-2024-07-18", 0.14, true}, // exato vence prefixo
		{"openai", "GPT-4o-mini-2025-01-01", 0.15, true}, // maiúsculas e prefixo mais longo
		{"azure.ai.openai", "gpt-4o", 2.5, true},         // revenda compatível
		{"", "claude-sonnet-4-5", 3, true},               // sem provedor casa pelo modelo
		{"aws.bedrock", "claude-sonnet-4-5", 0, false},   // provedor incompatível: sem preço
		{"openai", "modelo-desconhecido", 0, false},
		{"openai", "", 0, false},
	}
	for _, c := range casos {
		p, ok := tab.Achar(c.provedor, c.modelo, agora)
		if ok != c.ok || (ok && p.Entrada != c.quer) {
			t.Errorf("%s/%s: ok=%v entrada=%v, quer ok=%v %v", c.provedor, c.modelo, ok, p.Entrada, c.ok, c.quer)
		}
	}
}

func TestTabelaRespeitaVigencia(t *testing.T) {
	jan := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	jun := time.Date(2025, 6, 1, 0, 0, 0, 0, time.UTC)
	tab := NovaTabela([]Preco{
		{Modelo: "m*", Entrada: 10, VigenteDesde: jan},
		{Modelo: "m*", Entrada: 5, VigenteDesde: jun},
	})
	if p, _ := tab.Achar("", "m1", jun.Add(-time.Hour)); p.Entrada != 10 {
		t.Fatalf("antes da troca deveria valer 10, veio %v", p.Entrada)
	}
	if p, _ := tab.Achar("", "m1", jun); p.Entrada != 5 {
		t.Fatalf("na troca deveria valer 5, veio %v", p.Entrada)
	}
	if _, ok := tab.Achar("", "m1", jan.Add(-time.Hour)); ok {
		t.Fatal("antes de qualquer vigência não há preço")
	}
	var nula *Tabela
	if _, ok := nula.Achar("", "m1", jun); ok {
		t.Fatal("tabela nula achou preço")
	}
}
