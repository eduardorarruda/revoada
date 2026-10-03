package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCarregarEnvNaoSobrescreve(t *testing.T) {
	arq := filepath.Join(t.TempDir(), "painel.env")
	if err := os.WriteFile(arq, []byte("# comentário\nREVOADA_TESTE_A=1\nexport REVOADA_TESTE_B=\"dois\"\nREVOADA_TESTE_C=do-arquivo\nlixo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("REVOADA_TESTE_C", "do-ambiente")
	if err := carregarEnv(arq); err != nil {
		t.Fatal(err)
	}
	if os.Getenv("REVOADA_TESTE_A") != "1" || os.Getenv("REVOADA_TESTE_B") != "dois" || os.Getenv("REVOADA_TESTE_C") != "do-ambiente" {
		t.Fatalf("A=%q B=%q C=%q", os.Getenv("REVOADA_TESTE_A"), os.Getenv("REVOADA_TESTE_B"), os.Getenv("REVOADA_TESTE_C"))
	}
	os.Unsetenv("REVOADA_TESTE_A")
	os.Unsetenv("REVOADA_TESTE_B")
}

func TestArgumentoEnv(t *testing.T) {
	resto, env := argumentoEnv([]string{"migracoes", "--env", "/etc/x.env", "listar"})
	if env != "/etc/x.env" || len(resto) != 2 || resto[0] != "migracoes" || resto[1] != "listar" {
		t.Fatalf("%v %q", resto, env)
	}
	if _, env := argumentoEnv([]string{"--env"}); env != "" {
		t.Fatal("--env sem valor")
	}
}
