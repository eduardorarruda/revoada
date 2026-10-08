package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestNormalizarPapel(t *testing.T) {
	casos := map[string]string{
		"admin": PapelAdmin, "Administrador": PapelAdmin,
		"operador": PapelOperador, "editor": PapelOperador,
		"leitor": PapelLeitor, "viewer": PapelLeitor, "user": PapelLeitor, "": PapelLeitor, "root": PapelLeitor,
	}
	for in, quer := range casos {
		if got := NormalizarPapel(in); got != quer {
			t.Errorf("%q → %q, quer %q", in, got, quer)
		}
	}
}

func TestMatrizDePapeis(t *testing.T) {
	casos := []struct {
		papel string
		p     Permissao
		pode  bool
	}{
		{PapelLeitor, PermVer, true},
		{PapelLeitor, PermEditarMapeamento, false},
		{PapelLeitor, PermExecutarMigracao, false},
		{PapelLeitor, PermRodarDeploy, false},
		{PapelOperador, PermEditarMapeamento, true},
		{PapelOperador, PermExecutarMigracao, true},
		{PapelOperador, PermRodarDeploy, true},
		{PapelOperador, PermGerenciarConexoes, false},
		{PapelOperador, PermGerenciarAgentes, false},
		{PapelOperador, PermGerenciarUsuarios, false},
		{PapelAdmin, PermGerenciarConexoes, true},
		{PapelAdmin, PermGerenciarUsuarios, true},
		{"desconhecido", PermExecutarMigracao, false},
		// Conteúdo das conversas de IA: leitor vê custo e erro, nunca o prompt.
		{PapelLeitor, PermVerConteudoIA, false},
		{PapelOperador, PermVerConteudoIA, true},
		{PapelAdmin, PermVerConteudoIA, true},
		{PapelLeitor, PermApagarDadosIA, false},
		{PapelOperador, PermApagarDadosIA, false},
		{PapelAdmin, PermApagarDadosIA, true},
		{"desconhecido", PermVerConteudoIA, false},
	}
	for _, c := range casos {
		if got := Pode(c.papel, c.p); got != c.pode {
			t.Errorf("%s/%s: got %v, quer %v", c.papel, c.p, got, c.pode)
		}
	}
}

func TestAvaliarExigeMFAEReautenticacaoNasCriticas(t *testing.T) {
	agora := time.Unix(1_800_000_000, 0)
	futuro := agora.Add(time.Minute).Unix()

	if _, cod := avaliar(Claims{Role: PapelLeitor}, PermExecutarMigracao, agora); cod != CodigoSemPermissao {
		t.Errorf("leitor executando: %q", cod)
	}
	if _, cod := avaliar(Claims{Role: PapelOperador}, PermExecutarMigracao, agora); cod != CodigoMFANecessario {
		t.Errorf("operador sem 2FA: %q", cod)
	}
	if _, cod := avaliar(Claims{Role: PapelOperador, MFA: true}, PermExecutarMigracao, agora); cod != CodigoReautenticacao {
		t.Errorf("operador sem reauth: %q", cod)
	}
	if _, cod := avaliar(Claims{Role: PapelOperador, MFA: true, Reauth: futuro}, PermExecutarMigracao, agora); cod != "" {
		t.Errorf("operador completo: %q", cod)
	}
	// não crítica: basta o papel
	if _, cod := avaliar(Claims{Role: PapelOperador}, PermEditarMapeamento, agora); cod != "" {
		t.Errorf("editar rascunho: %q", cod)
	}
}

func TestExigeRespondeJSON403(t *testing.T) {
	h := Exige(PermGerenciarUsuarios, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	req := httptest.NewRequest(http.MethodPost, "/x", nil)
	req = req.WithContext(context.WithValue(req.Context(), claimsKey, Claims{Role: PapelOperador}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || rec.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("code %d ct %q", rec.Code, rec.Header().Get("Content-Type"))
	}

	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/x", nil)) // sem claims
	if rec.Code != http.StatusForbidden {
		t.Fatalf("sem claims: %d", rec.Code)
	}
}

// TestVerConteudoIAECritica: prompt de cliente é dado pessoal — ler o conteúdo pede a
// mesma confirmação de identidade (2FA + reautenticação) que ver credencial.
func TestVerConteudoIAECritica(t *testing.T) {
	if !criticas[PermVerConteudoIA] {
		t.Fatal("PermVerConteudoIA tem de ser crítica")
	}
	agora := time.Unix(1_800_000_000, 0)
	futuro := agora.Add(time.Minute).Unix()
	casos := []struct {
		nome string
		c    Claims
		quer string
	}{
		{"leitor nunca", Claims{Role: PapelLeitor, MFA: true, Reauth: futuro}, CodigoSemPermissao},
		{"operador sem 2FA", Claims{Role: PapelOperador, Reauth: futuro}, CodigoMFANecessario},
		{"operador sem reauth", Claims{Role: PapelOperador, MFA: true}, CodigoReautenticacao},
		{"operador com reauth vencida", Claims{Role: PapelOperador, MFA: true, Reauth: agora.Add(-time.Second).Unix()}, CodigoReautenticacao},
		{"operador completo", Claims{Role: PapelOperador, MFA: true, Reauth: futuro}, ""},
		{"admin sem reauth", Claims{Role: PapelAdmin, MFA: true}, CodigoReautenticacao},
		{"admin completo", Claims{Role: PapelAdmin, MFA: true, Reauth: futuro}, ""},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			if _, cod := avaliar(c.c, PermVerConteudoIA, agora); cod != c.quer {
				t.Fatalf("código %q, quer %q", cod, c.quer)
			}
		})
	}
}

func TestApagarDadosIAECritica(t *testing.T) {
	if !criticas[PermApagarDadosIA] {
		t.Fatal("apagar dados de IA é irreversível: tem de pedir 2FA e reautenticação")
	}
}
