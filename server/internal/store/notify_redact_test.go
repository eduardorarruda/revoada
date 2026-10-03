package store

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestRedactSecretsAninhado é o vazamento medido: um canal do tipo webhook guarda os
// cabeçalhos em `config.headers`, e é ali que mora o `Authorization: Bearer …` do
// sistema de terceiro. A redação antiga só trocava strings do PRIMEIRO nível, então
// `GET /api/notify/channels` devolvia `config.headers.Authorization` LEGÍVEL.
func TestRedactSecretsAninhado(t *testing.T) {
	cfg := map[string]any{
		"url":      "https://erp.cliente.com/hook",
		"password": "hunter2",
		"headers": map[string]any{
			"Authorization": "Bearer sk-live-EXPOSTO",
			"X-Tenant":      "acme",
		},
		"lista": []any{
			map[string]any{"api_key": "AKIA-EXPOSTA", "nome": "n1"},
		},
	}
	redactSecrets(cfg)
	saida, _ := json.Marshal(cfg)
	for _, segredo := range []string{"hunter2", "sk-live-EXPOSTO", "AKIA-EXPOSTA"} {
		if strings.Contains(string(saida), segredo) {
			t.Errorf("segredo %q saiu em claro: %s", segredo, saida)
		}
	}
	// O que NÃO é segredo continua visível — senão a tela de canais fica inútil.
	if !strings.Contains(string(saida), "https://erp.cliente.com/hook") {
		t.Errorf("a url não deveria ser redigida: %s", saida)
	}
}

// TestRedactSecretsPreservaEdicao: o marcador precisa ser o MESMO que o UpdateChannel
// reconhece para preservar o valor atual — senão salvar o formulário sem retocar o
// segredo o sobrescreveria com "••••••" e o canal pararia de funcionar.
func TestRedactSecretsPreservaEdicao(t *testing.T) {
	cfg := map[string]any{"password": "hunter2"}
	redactSecrets(cfg)
	if cfg["password"] != redactedMark {
		t.Fatalf("marcador inesperado: %v", cfg["password"])
	}
	if redactedMark != "••••••" {
		t.Error("o marcador mudou; UpdateChannel/SetWhatsAppIntegration comparam com ••••••")
	}
}
