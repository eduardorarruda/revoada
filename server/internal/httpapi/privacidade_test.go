package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// A política mostra os prazos desta instalação: quem mudou a retenção ou a sessão no
// .env não pode ler na tela o prazo padrão.
func TestPrivacidadeDevolveOsPrazosConfigurados(t *testing.T) {
	t.Setenv("REVOADA_AUDIT_RETENTION_DAYS", "400")
	t.Setenv("REVOADA_SESSAO_MAX_HORAS", "48")
	t.Setenv("REVOADA_PRIVACIDADE_CONTROLADOR", "Empresa Exemplo Ltda.")
	t.Setenv("REVOADA_PRIVACIDADE_CONTATO", "privacidade@exemplo.com.br")

	rec := httptest.NewRecorder()
	privacidade(rec, httptest.NewRequest(http.MethodGet, "/api/privacidade", nil))
	var r struct {
		Controlador   string `json:"controlador"`
		Contato       string `json:"contato"`
		AuditoriaDias int    `json:"auditoria_dias"`
		SessaoMax     int    `json:"sessao_max_horas"`
		Inatividade   int    `json:"sessao_inatividade_horas"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &r); err != nil {
		t.Fatal(err)
	}
	if r.AuditoriaDias != 400 || r.SessaoMax != 48 || r.Inatividade != 24 {
		t.Fatalf("prazos: %+v", r)
	}
	if r.Controlador != "Empresa Exemplo Ltda." || r.Contato != "privacidade@exemplo.com.br" {
		t.Fatalf("controlador/contato: %+v", r)
	}
}
