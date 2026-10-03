package agentev1

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func TestAssinaturaDeTarefa(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	agora := time.Unix(1_800_000_000, 0)
	tarefa := &Tarefa{Id: "t1", Tipo: "diagnostico.eco", Especificacao: []byte(`{"passos":3}`), ExpiraEm: agora.Add(time.Hour).Unix()}
	if err := AssinarTarefa(priv, tarefa); err != nil {
		t.Fatal(err)
	}
	if err := VerificarTarefa(pub, tarefa, agora); err != nil {
		t.Fatalf("tarefa legítima recusada: %v", err)
	}

	alterada := &Tarefa{}
	*alterada = Tarefa{Id: tarefa.Id, Tipo: "deploy.script", Especificacao: tarefa.Especificacao, ExpiraEm: tarefa.ExpiraEm, Assinatura: tarefa.Assinatura}
	if err := VerificarTarefa(pub, alterada, agora); !errors.Is(err, ErrAssinaturaInvalida) {
		t.Fatalf("tipo trocado no caminho deveria invalidar: %v", err)
	}

	outroPub, _, _ := ed25519.GenerateKey(rand.Reader)
	if err := VerificarTarefa(outroPub, tarefa, agora); !errors.Is(err, ErrAssinaturaInvalida) {
		t.Fatalf("outro painel não pode assinar por este: %v", err)
	}
	if err := VerificarTarefa(pub, tarefa, agora.Add(2*time.Hour)); !errors.Is(err, ErrTarefaVencida) {
		t.Fatalf("tarefa vencida: %v", err)
	}
	if err := VerificarTarefa(pub, &Tarefa{Id: "x"}, agora); !errors.Is(err, ErrAssinaturaInvalida) {
		t.Fatalf("sem assinatura: %v", err)
	}
}

func TestAssinaturaDePedidoEsquema(t *testing.T) {
	pub, priv, _ := ed25519.GenerateKey(rand.Reader)
	p := &PedidoEsquema{PedidoId: "p1", Motor: "firebird", Endereco: "localhost:3050"}
	if err := AssinarPedidoEsquema(priv, p); err != nil {
		t.Fatal(err)
	}
	if err := VerificarPedidoEsquema(pub, p); err != nil {
		t.Fatal(err)
	}
	p.Endereco = "outro:3050"
	if err := VerificarPedidoEsquema(pub, p); !errors.Is(err, ErrAssinaturaInvalida) {
		t.Fatalf("endereço trocado: %v", err)
	}
}

func TestTokenInscricao(t *testing.T) {
	seg := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
	fp := ImpressaoDigital([]byte("certificado da CA"))
	tok := TokenInscricao{Segredo: seg, ImpressaoCA: fp}
	lido, err := LerToken(" " + tok.String() + "\n")
	if err != nil || lido.Segredo != seg || !MesmaImpressao(lido.ImpressaoCA, fp) {
		t.Fatalf("ida e volta falhou: %+v %v", lido, err)
	}
	for _, ruim := range []string{"", "rvd1.x.y", "rvd2." + seg + ".00", "rvd1." + seg + ".zz", "rvd1.!!!." + "00"} {
		if _, err := LerToken(ruim); !errors.Is(err, ErrTokenMalformado) {
			t.Errorf("%q deveria ser malformado, veio %v", ruim, err)
		}
	}
}
