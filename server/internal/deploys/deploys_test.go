package deploys

import "testing"

func TestValidar(t *testing.T) {
	ok := Pedido{Agente: "srv-01", Aplicacao: "loja", Versao: "v1.2.3", Ambiente: "producao", Commit: "a1b2c3d"}
	if err := validar(ok); err != nil {
		t.Fatal(err)
	}
	for _, ruim := range []Pedido{
		{Agente: "srv", Aplicacao: "../etc", Versao: "1"},
		{Agente: "srv", Aplicacao: "loja", Versao: "1; rm -rf /"},
		{Agente: "srv", Aplicacao: "loja", Versao: "$(id)"},
		{Agente: "srv", Aplicacao: "loja", Versao: "1", Commit: "não-é-hash"},
		{Agente: " ", Aplicacao: "loja", Versao: "1"},
	} {
		if validar(ruim) == nil {
			t.Errorf("deveria recusar %+v", ruim)
		}
	}
	if !nomeRepo.MatchString("eduardorarruda/loja") || nomeRepo.MatchString("x; drop") {
		t.Fatal("repositório")
	}
}
