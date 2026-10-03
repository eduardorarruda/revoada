package dashboards

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

type fakeNormalizacao struct {
	candidatos []store.PainelComTravessao
	salvos     map[string]string // uid -> título salvo
	modelos    map[string]string
	autores    map[string]string
}

func (f *fakeNormalizacao) PaineisComTravessao(context.Context) ([]store.PainelComTravessao, error) {
	return f.candidatos, nil
}

func (f *fakeNormalizacao) UpdateDashboard(_ context.Context, uid, title, _ string, model json.RawMessage, by string) (int, error) {
	f.salvos[uid], f.modelos[uid], f.autores[uid] = title, string(model), by
	return 2, nil
}

// O travessão dos títulos gerados vira vírgula, mas só no que o SISTEMA gerou: um
// painel que alguém criou à mão com o prefixo "host-" é conteúdo da pessoa.
func TestNormalizarTravessaoSoNoQueOSistemaGerou(t *testing.T) {
	f := &fakeNormalizacao{
		salvos: map[string]string{}, modelos: map[string]string{}, autores: map[string]string{},
		candidatos: []store.PainelComTravessao{
			// gerado pelo reconcile: versão 1 do "sistema"
			{UID: "host-srv1", Title: "Visão do Host — srv1", Folder: "Hosts", Model: json.RawMessage(`{"panels":[{"title":"Rede — recebido"}]}`), AutorV1: "sistema"},
			// gerado pelo botão "Criar Visão do Host" (versão 1 de um admin), título no padrão
			{UID: "host-mail", Title: "Visão do Host — mail", Folder: "Hosts", Model: json.RawMessage(`{}`), AutorV1: "admin"},
			// genérico semeado no boot: título sem travessão, modelo com
			{UID: "host-visao-geral", Title: "Visão do Host (genérico)", Folder: "Hosts", Model: json.RawMessage(`{"d":"0% — alto"}`), AutorV1: "sistema"},
			// criado à mão com o prefixo: fica como está
			{UID: "host-notas", Title: "Notas do DBA — backups", Folder: "Geral", Model: json.RawMessage(`{}`), AutorV1: "maria"},
		},
	}
	n, err := NormalizarTravessao(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("normalizou %d painéis; esperado 3", n)
	}
	if f.salvos["host-srv1"] != "Visão do Host, srv1" || f.modelos["host-srv1"] != `{"panels":[{"title":"Rede, recebido"}]}` {
		t.Errorf("host-srv1 salvo como %q / %s", f.salvos["host-srv1"], f.modelos["host-srv1"])
	}
	if f.salvos["host-mail"] != "Visão do Host, mail" {
		t.Errorf("host-mail salvo como %q", f.salvos["host-mail"])
	}
	if f.modelos["host-visao-geral"] != `{"d":"0%, alto"}` {
		t.Errorf("genérico salvo como %s", f.modelos["host-visao-geral"])
	}
	if _, mexeu := f.salvos["host-notas"]; mexeu {
		t.Error("o painel criado à mão foi alterado")
	}
	// Salva como versão nova, assinada pelo sistema: o histórico continua batendo.
	if f.autores["host-srv1"] != "sistema" {
		t.Errorf("autor da versão nova = %q; esperado sistema", f.autores["host-srv1"])
	}
}
