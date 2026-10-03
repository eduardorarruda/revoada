package deploy

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/eduardorarruda/revoada/agent/internal/config"
	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
)

type relator struct{ msgs []string }

func (r *relator) Evento(_ string, _ agentev1.EventoTarefa_Nivel, m string, _ float64, _ map[string]float64) {
	r.msgs = append(r.msgs, m)
}

// app de teste: o "deploy" grava a versão num arquivo; o health check responde 200
// só quando a versão no ar não é "quebrada".
func preparar(t *testing.T) (*Motor, string, func(string) []byte) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("scripts de teste em sh")
	}
	dir := t.TempDir()
	noAr := filepath.Join(dir, "no-ar.txt")
	os.WriteFile(filepath.Join(dir, "deploy.sh"), []byte("echo implantando $REVOADA_VERSAO\necho $REVOADA_VERSAO > no-ar.txt\n"), 0o755)
	os.WriteFile(filepath.Join(dir, "rollback.sh"), []byte("echo voltando para $REVOADA_VERSAO_ANTERIOR\necho $REVOADA_VERSAO_ANTERIOR > no-ar.txt\n"), 0o755)
	var chamadas atomic.Int64
	saude := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		chamadas.Add(1)
		b, _ := os.ReadFile(noAr)
		if strings.TrimSpace(string(b)) == "quebrada" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(saude.Close)
	m := NovoMotor(map[string]config.AplicacaoDeploy{"loja": {Tipo: "script", Diretorio: dir, Script: "deploy.sh", Rollback: "rollback.sh",
		Saude: saude.URL, EsperaS: 3}}, filepath.Join(dir, "estado"))
	esp := func(v string) []byte { b, _ := json.Marshal(Especificacao{Aplicacao: "loja", Versao: v}); return b }
	return m, noAr, esp
}

func TestDeploySaudavelERollbackAutomatico(t *testing.T) {
	m, noAr, esp := preparar(t)
	ctx := context.Background()
	if _, err := m.Aplicar(ctx, esp("1.0.0"), &relator{}); err != nil {
		t.Fatal(err)
	}
	out, err := m.Aplicar(ctx, esp("1.1.0"), &relator{})
	if err != nil {
		t.Fatal(err)
	}
	if r := out.(*Resumo); r.Anterior != "1.0.0" || !r.Saudavel || r.Revertido || !strings.Contains(strings.Join(r.Saida, "\n"), "implantando 1.1.0") {
		t.Fatalf("resumo: %+v", r)
	}
	// versão que não sobe: volta sozinho para 1.1.0
	out, err = m.Aplicar(ctx, esp("quebrada"), &relator{})
	if err == nil || !strings.Contains(err.Error(), "revertido para 1.1.0") {
		t.Fatalf("queria rollback: %v", err)
	}
	if r := out.(*Resumo); !r.Revertido {
		t.Fatalf("resumo: %+v", r)
	}
	if b, _ := os.ReadFile(noAr); strings.TrimSpace(string(b)) != "1.1.0" {
		t.Fatalf("no ar depois do rollback: %q", b)
	}
	if st := m.lerEstado("loja"); st.Atual != "1.1.0" || st.Anterior != "1.0.0" {
		t.Fatalf("o estado não pode registrar a versão que falhou: %+v", st)
	}
}

func TestDeployRecusaOQueNaoFoiLiberado(t *testing.T) {
	m, _, esp := preparar(t)
	for _, c := range []struct {
		corpo []byte
		erro  string
	}{
		{[]byte(`{"aplicacao":"outra","versao":"1"}`), "não está liberada"},
		{esp("1.0; rm -rf /"), "versão inválida"},
		{esp("$(id)"), "versão inválida"},
	} {
		if _, err := m.Aplicar(context.Background(), c.corpo, &relator{}); err == nil || !strings.Contains(err.Error(), c.erro) {
			t.Errorf("%s: %v", c.corpo, err)
		}
	}
	if _, err := dentroDe("/srv/app", "../../etc/passwd"); err == nil {
		t.Fatal("script fora da pasta")
	}
}
