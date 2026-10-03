package selfinstall

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInscricaoDevolveAChaveDoServidor(t *testing.T) {
	var recebido struct{ Token, Hostname string }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/enroll" || r.Method != http.MethodPost {
			t.Errorf("chamada inesperada: %s %s", r.Method, r.URL.Path)
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &recebido)
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"key":"chave-desta-maquina","hostname":"web-01"}`))
	}))
	defer srv.Close()

	key, err := Enroll(context.Background(), srv.URL+"/", "token-abc", "web-01")
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if key != "chave-desta-maquina" {
		t.Errorf("chave = %q", key)
	}
	// O hostname vai junto: é ele que amarra a chave à máquina certa no painel.
	if recebido.Token != "token-abc" || recebido.Hostname != "web-01" {
		t.Errorf("enviado = %+v", recebido)
	}
}

func TestTokenRecusadoRepassaAFraseDoPainel(t *testing.T) {
	// Quem lê isso é o administrador da máquina, num terminal, sem acesso ao
	// painel. Traduzir "401" não ajuda; a frase do painel diz o que fazer.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "token de inscrição inválido ou revogado — gere um instalador novo no painel", http.StatusUnauthorized)
	}))
	defer srv.Close()

	_, err := Enroll(context.Background(), srv.URL, "token-velho", "web-01")
	if err == nil {
		t.Fatal("inscrição com token revogado deveria falhar")
	}
	if !strings.Contains(err.Error(), "revogado") || !strings.Contains(err.Error(), "gere um instalador novo") {
		t.Errorf("mensagem perdeu a instrução do painel: %q", err)
	}
}

func TestRespostaSemChaveNaoPassaPorSucesso(t *testing.T) {
	// 200 com corpo vazio (um proxy no meio do caminho, por exemplo) não pode
	// virar instalação silenciosa sem credencial.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	if _, err := Enroll(context.Background(), srv.URL, "t", "h"); err == nil {
		t.Fatal("resposta sem chave deveria falhar")
	}
}

func TestPainelForaDoArDaErroLegivel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close() // ninguém atendendo

	_, err := Enroll(context.Background(), url, "t", "h")
	if err == nil {
		t.Fatal("deveria falhar com o painel fora do ar")
	}
	if !strings.Contains(err.Error(), "não consegui falar com o painel") {
		t.Errorf("mensagem pouco clara: %q", err)
	}
}

func TestReceitaComTokenEmVezDeChaveEhValida(t *testing.T) {
	// O instalador universal não traz chave; a receita tem de ser aceita mesmo
	// assim, senão o binário do Windows recusaria a si próprio.
	arq := comTrailer([]byte("binário"), Recipe{
		GatewayURL:  "https://painel.exemplo",
		PanelURL:    "https://painel.exemplo",
		EnrollToken: "token-abc",
	})
	rec, _, err := ReadFrom(strings.NewReader(string(arq)), int64(len(arq)))
	if err != nil {
		t.Fatalf("erro: %v", err)
	}
	if rec.EnrollToken != "token-abc" || rec.Key != "" {
		t.Errorf("receita = %+v", rec)
	}
}

func TestReceitaSemChaveESemTokenEhRejeitada(t *testing.T) {
	arq := comTrailer([]byte("binário"), Recipe{GatewayURL: "https://painel.exemplo"})
	if _, _, err := ReadFrom(strings.NewReader(string(arq)), int64(len(arq))); err == nil {
		t.Fatal("receita sem credencial nenhuma deveria ser rejeitada")
	}
}
