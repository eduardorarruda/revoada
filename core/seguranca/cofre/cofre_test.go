package cofre

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func novoCofre(t *testing.T) (*Cofre, *ProvedorMemoria) {
	t.Helper()
	p, err := NovoProvedorMemoria()
	if err != nil {
		t.Fatal(err)
	}
	c, err := Novo(p)
	if err != nil {
		t.Fatal(err)
	}
	return c, p
}

func TestCifrarDecifrarVoltaAoTextoOriginal(t *testing.T) {
	c, _ := novoCofre(t)
	ctx := context.Background()
	s, err := c.Cifrar(ctx, []byte("senha-do-banco"), []byte("conexao:1"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(s.Cifrado, []byte("senha-do-banco")) {
		t.Fatal("texto puro apareceu no segredo cifrado")
	}
	got, err := c.Decifrar(ctx, s, []byte("conexao:1"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "senha-do-banco" {
		t.Fatalf("got %q", got)
	}
}

func TestDecifrarComContextoErradoFalha(t *testing.T) {
	c, _ := novoCofre(t)
	ctx := context.Background()
	s, _ := c.Cifrar(ctx, []byte("x"), []byte("conexao:1"))
	if _, err := c.Decifrar(ctx, s, []byte("conexao:2")); !errors.Is(err, ErrSegredoInvalido) {
		t.Fatalf("esperava ErrSegredoInvalido, veio %v", err)
	}
}

func TestDecifrarTextoAdulteradoFalha(t *testing.T) {
	c, _ := novoCofre(t)
	ctx := context.Background()
	s, _ := c.Cifrar(ctx, []byte("x"), nil)
	s.Cifrado[len(s.Cifrado)-1] ^= 0xFF
	if _, err := c.Decifrar(ctx, s, nil); !errors.Is(err, ErrSegredoInvalido) {
		t.Fatalf("esperava ErrSegredoInvalido, veio %v", err)
	}
}

func TestDecifrarSegredoVazioFalha(t *testing.T) {
	c, _ := novoCofre(t)
	if _, err := c.Decifrar(context.Background(), Segredo{}, nil); !errors.Is(err, ErrSegredoInvalido) {
		t.Fatalf("esperava ErrSegredoInvalido, veio %v", err)
	}
}

func TestVersaoDaKEKTrocadaNoBancoNaoDecifra(t *testing.T) {
	c, p := novoCofre(t)
	ctx := context.Background()
	s, _ := c.Cifrar(ctx, []byte("x"), nil)
	if err := p.Girar(); err != nil {
		t.Fatal(err)
	}
	s.KEKVersao = 2 // atacante tenta forçar outra versão
	if _, err := c.Decifrar(ctx, s, nil); !errors.Is(err, ErrSegredoInvalido) {
		t.Fatalf("esperava ErrSegredoInvalido, veio %v", err)
	}
}

func TestRotacaoReembrulhaSemMudarOTexto(t *testing.T) {
	c, p := novoCofre(t)
	ctx := context.Background()
	s, _ := c.Cifrar(ctx, []byte("segredo"), []byte("ctx"))
	if err := p.Girar(); err != nil {
		t.Fatal(err)
	}
	r, err := c.Reembrulhar(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if r.KEKVersao != 2 || !bytes.Equal(r.Cifrado, s.Cifrado) {
		t.Fatalf("reembrulho inesperado: versão %d", r.KEKVersao)
	}
	got, err := c.Decifrar(ctx, r, []byte("ctx"))
	if err != nil || string(got) != "segredo" {
		t.Fatalf("got %q, err %v", got, err)
	}
	// o segredo antigo continua legível enquanto a versão 1 existir
	if _, err := c.Decifrar(ctx, s, []byte("ctx")); err != nil {
		t.Fatalf("versão antiga deveria decifrar: %v", err)
	}
}

func TestNovoSemProvedorFalha(t *testing.T) {
	if _, err := Novo(nil); err == nil {
		t.Fatal("esperava erro")
	}
}

func TestProvedorArquivoCriaPersisteEReabre(t *testing.T) {
	dir := t.TempDir()
	caminho := filepath.Join(dir, "sub", "chave-mestra.json")
	p, criado, err := AbrirOuCriarArquivo(caminho)
	if err != nil || !criado {
		t.Fatalf("criado=%v err=%v", criado, err)
	}
	c, _ := Novo(p)
	ctx := context.Background()
	s, _ := c.Cifrar(ctx, []byte("x"), nil)

	if runtime.GOOS != "windows" {
		st, _ := os.Stat(caminho)
		if st.Mode().Perm() != 0o600 {
			t.Fatalf("permissão %v, esperava 0600", st.Mode().Perm())
		}
	}

	p2, criado, err := AbrirOuCriarArquivo(caminho)
	if err != nil || criado {
		t.Fatalf("reabrir: criado=%v err=%v", criado, err)
	}
	c2, _ := Novo(p2)
	got, err := c2.Decifrar(ctx, s, nil)
	if err != nil || string(got) != "x" {
		t.Fatalf("got %q err %v", got, err)
	}

	if err := p2.Girar(); err != nil {
		t.Fatal(err)
	}
	p3, _, err := AbrirOuCriarArquivo(caminho)
	if err != nil || p3.VersaoAtiva() != 2 {
		t.Fatalf("rotação não persistiu: versão %d err %v", p3.VersaoAtiva(), err)
	}
}

func TestProvedorArquivoRecusaPermissaoAberta(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permissões Unix")
	}
	caminho := filepath.Join(t.TempDir(), "k.json")
	if _, _, err := AbrirOuCriarArquivo(caminho); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(caminho, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AbrirOuCriarArquivo(caminho); !errors.Is(err, ErrPermissaoAberta) {
		t.Fatalf("esperava ErrPermissaoAberta, veio %v", err)
	}
}

func TestProvedorArquivoCorrompidoFalha(t *testing.T) {
	caminho := filepath.Join(t.TempDir(), "k.json")
	if err := os.WriteFile(caminho, []byte("{nao é json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AbrirOuCriarArquivo(caminho); err == nil {
		t.Fatal("esperava erro")
	}
}

func TestLimparZeraBuffer(t *testing.T) {
	b := []byte{1, 2, 3}
	Limpar(b)
	if !bytes.Equal(b, []byte{0, 0, 0}) {
		t.Fatalf("%v", b)
	}
}
