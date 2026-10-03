package selo

import (
	"bytes"
	"errors"
	"testing"
	"time"
)

var agora = time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)

func TestSelarEAbrirComAChaveCerta(t *testing.T) {
	agente, err := GerarPar()
	if err != nil {
		t.Fatal(err)
	}
	s, err := Selar(agente.PublicKey().Bytes(), []byte("s3nh@"), "tarefa:abc", time.Minute, agora)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(s.Cifrado, []byte("s3nh@")) {
		t.Fatal("texto puro no selo")
	}
	got, err := Abrir(agente, s, "tarefa:abc", agora.Add(30*time.Second))
	if err != nil || string(got) != "s3nh@" {
		t.Fatalf("got %q err %v", got, err)
	}
}

func TestOutroAgenteNaoAbre(t *testing.T) {
	a, _ := GerarPar()
	b, _ := GerarPar()
	s, _ := Selar(a.PublicKey().Bytes(), []byte("x"), "t", time.Minute, agora)
	if _, err := Abrir(b, s, "t", agora); !errors.Is(err, ErrSeloInvalido) {
		t.Fatalf("esperava ErrSeloInvalido, veio %v", err)
	}
}

func TestContextoDiferenteNaoAbre(t *testing.T) {
	a, _ := GerarPar()
	s, _ := Selar(a.PublicKey().Bytes(), []byte("x"), "tarefa:1", time.Minute, agora)
	if _, err := Abrir(a, s, "tarefa:2", agora); !errors.Is(err, ErrSeloInvalido) {
		t.Fatalf("esperava ErrSeloInvalido, veio %v", err)
	}
}

func TestSeloVencidoNaoAbre(t *testing.T) {
	a, _ := GerarPar()
	s, _ := Selar(a.PublicKey().Bytes(), []byte("x"), "t", time.Minute, agora)
	if _, err := Abrir(a, s, "t", agora.Add(2*time.Minute)); !errors.Is(err, ErrSeloVencido) {
		t.Fatalf("esperava ErrSeloVencido, veio %v", err)
	}
}

func TestEstenderValidadeNoSeloInvalida(t *testing.T) {
	a, _ := GerarPar()
	s, _ := Selar(a.PublicKey().Bytes(), []byte("x"), "t", time.Minute, agora)
	s.ExpiraEm += 3600 // atacante tenta esticar a validade
	if _, err := Abrir(a, s, "t", agora); !errors.Is(err, ErrSeloInvalido) {
		t.Fatalf("esperava ErrSeloInvalido, veio %v", err)
	}
}

func TestChavePublicaInvalidaFalha(t *testing.T) {
	if _, err := Selar([]byte{1, 2, 3}, []byte("x"), "t", time.Minute, agora); err == nil {
		t.Fatal("esperava erro")
	}
}

func TestCarregarPrivadaRoundTrip(t *testing.T) {
	a, _ := GerarPar()
	b, err := CarregarPrivada(a.Bytes())
	if err != nil || !bytes.Equal(a.PublicKey().Bytes(), b.PublicKey().Bytes()) {
		t.Fatalf("err %v", err)
	}
	if _, err := CarregarPrivada([]byte{1}); err == nil {
		t.Fatal("esperava erro")
	}
}
