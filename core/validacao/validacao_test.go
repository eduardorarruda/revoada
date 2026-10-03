package validacao

import "testing"

func TestTextoSanitiza(t *testing.T) {
	got, err := Texto("nome", "  ERP‮ cliente​ X \n prod  ", true, 50)
	if err != nil || got != "ERP cliente X prod" {
		t.Fatalf("%q %v", got, err)
	}
	if _, err := Texto("nome", "   ", true, 10); err == nil {
		t.Fatal("vazio obrigatório")
	}
	if _, err := Texto("nome", "abcdef", true, 3); err == nil {
		t.Fatal("longo")
	}
	if _, err := Texto("nome", "\xff", false, 3); err == nil {
		t.Fatal("UTF-8 inválido")
	}
}

func TestHostPorta(t *testing.T) {
	ok := map[string]string{"banco.empresa.com:5432": "banco.empresa.com:5432", "10.0.0.5": "10.0.0.5:3050", "[::1]:3050": "[::1]:3050"}
	for in, quer := range ok {
		if got, err := HostPorta("endereco", in, 3050); err != nil || got != quer {
			t.Errorf("%s → %q %v", in, got, err)
		}
	}
	for _, ruim := range []string{"host:0", "host:99999", "ho st:1", "a;rm -rf:1", "-x.com:1", ""} {
		if _, err := HostPorta("endereco", ruim, 3050); err == nil {
			t.Errorf("%q deveria falhar", ruim)
		}
	}
}

func TestCaminhoUsuarioEscolhaOpcoes(t *testing.T) {
	if _, err := CaminhoBanco("banco", "/dados/erp.fdb"); err != nil {
		t.Fatal(err)
	}
	for _, ruim := range []string{"../etc/passwd", "a;b", "x\x00", "$(id)", ""} {
		if _, err := CaminhoBanco("banco", ruim); err == nil {
			t.Errorf("caminho %q deveria falhar", ruim)
		}
	}
	if _, err := Usuario("usuario", "SYSDBA"); err != nil {
		t.Fatal(err)
	}
	if _, err := Usuario("usuario", "a' OR 1=1"); err == nil {
		t.Fatal("aspas no usuário")
	}
	if _, err := Escolha("motor", "oracle", "firebird", "postgres"); err == nil {
		t.Fatal("fora da lista")
	}
	if _, err := Opcoes("opcoes", map[string]string{"charset": "WIN1252"}, "charset", "sslmode"); err != nil {
		t.Fatal(err)
	}
	if _, err := Opcoes("opcoes", map[string]string{"host": "x"}, "charset"); err == nil {
		t.Fatal("chave desconhecida")
	}
}
