package journey

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// servidor de teste: /form (GET, 200) e /submit (POST) que quebra se broken=true.
func testServer(broken *bool) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/form", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte("<h1>Formulario de contato</h1>"))
	})
	mux.HandleFunc("/submit", func(w http.ResponseWriter, _ *http.Request) {
		if *broken {
			http.Error(w, "erro", http.StatusInternalServerError)
			return
		}
		w.Write([]byte("Obrigado! Recebemos."))
	})
	return httptest.NewServer(mux)
}

func journeyFor(base string) store.Journey {
	return store.Journey{Name: "t", Steps: []store.JourneyStep{
		{Name: "abrir", Method: "GET", URL: base + "/form", AssertContains: "Formulario de contato"},
		{Name: "enviar", Method: "POST", URL: base + "/submit", Form: map[string]string{"email": "x@y.z"}, AssertContains: "Obrigado"},
	}}
}

func TestExecutePassaEQuebra(t *testing.T) {
	broken := false
	srv := testServer(&broken)
	defer srv.Close()
	r := &Runner{} // execute() não usa st/ch/notifier

	ok, diag, _ := r.execute(context.Background(), journeyFor(srv.URL))
	if !ok {
		t.Fatalf("jornada deveria passar, falhou: %s", diag)
	}

	// quebra o /submit: a jornada deve falhar no passo 2 (o GET /form ainda passa).
	broken = true
	ok, diag, _ = r.execute(context.Background(), journeyFor(srv.URL))
	if ok {
		t.Fatal("jornada deveria falhar com /submit quebrado")
	}
	if diag == "" || diag[:6] != "passo " {
		t.Errorf("diagnóstico deveria apontar o passo, veio: %q", diag)
	}
}

// O editor oferece corpo para PUT; o executor só enviava o form em POST, e um passo
// PUT/PATCH com `form` saía sem corpo, sem nenhum aviso.
func TestFormEnviadoEmMetodosComCorpo(t *testing.T) {
	for _, method := range []string{"POST", "PUT", "PATCH", "put"} {
		t.Run(method, func(t *testing.T) {
			var gotMethod, gotType, gotEmail string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				gotMethod = req.Method
				gotType = req.Header.Get("Content-Type")
				if err := req.ParseForm(); err != nil {
					t.Errorf("ParseForm: %v", err)
				}
				gotEmail = req.PostForm.Get("email")
			}))
			defer srv.Close()

			j := store.Journey{Name: "t", Steps: []store.JourneyStep{
				{Name: "enviar", Method: method, URL: srv.URL, Form: map[string]string{"email": "x@y.z"}},
			}}
			if ok, diag, _ := (&Runner{}).execute(context.Background(), j); !ok {
				t.Fatalf("jornada deveria passar, falhou: %s", diag)
			}
			if gotMethod != strings.ToUpper(method) {
				t.Errorf("método = %q, esperado %q", gotMethod, strings.ToUpper(method))
			}
			if gotType != "application/x-www-form-urlencoded" {
				t.Errorf("Content-Type = %q, esperado form-urlencoded", gotType)
			}
			if gotEmail != "x@y.z" {
				t.Errorf("campo email = %q, esperado %q (corpo não foi enviado)", gotEmail, "x@y.z")
			}
		})
	}
}

func TestAssertContainsFalha(t *testing.T) {
	broken := false
	srv := testServer(&broken)
	defer srv.Close()
	r := &Runner{}
	j := store.Journey{Name: "t", Steps: []store.JourneyStep{
		{Name: "abrir", Method: "GET", URL: srv.URL + "/form", AssertContains: "texto que não existe"},
	}}
	if ok, _, _ := r.execute(context.Background(), j); ok {
		t.Error("deveria falhar quando o texto esperado está ausente")
	}
}
