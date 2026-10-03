package notify

import "testing"

// O destino vai para uma tabela que qualquer admin lê. A regra que não pode
// quebrar: endereço sai, segredo NUNCA sai.
func TestDestinoDo(t *testing.T) {
	casos := []struct {
		nome string
		tipo string
		cfg  map[string]any
		quer string
	}{
		{"whatsapp com um número", "whatsapp", map[string]any{"to": "5511999999999", "token": "segredo"}, "5511999999999"},
		{"whatsapp com lista", "whatsapp", map[string]any{"numbers": []any{"5511111111111", "5522222222222"}}, "5511111111111, 5522222222222"},
		{"e-mail", "smtp", map[string]any{"to": "ops@exemplo.com.br", "password": "segredo"}, "ops@exemplo.com.br"},
		{"telegram", "telegram", map[string]any{"chat_id": "-100123", "bot_token": "segredo"}, "chat -100123"},
		{"webhook: host e caminho, sem a query", "webhook", map[string]any{"url": "https://hooks.slack.com/services/T00/B01/XXXX?token=segredo"}, "hooks.slack.com/services/T00/B01/XXXX"},
		{"tipo desconhecido não vaza nada", "exotico", map[string]any{"api_key": "segredo", "to": "alguem"}, ""},
		{"config vazio", "whatsapp", map[string]any{}, ""},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			got := DestinoDo(c.tipo, c.cfg)
			if got != c.quer {
				t.Fatalf("DestinoDo(%q) = %q, queria %q", c.tipo, got, c.quer)
			}
		})
	}
}

// Trava explícita: nenhum valor secreto pode aparecer no destino, em nenhum tipo.
func TestDestinoNuncaVazaSegredo(t *testing.T) {
	const marca = "NAO-PODE-APARECER"
	cfg := map[string]any{
		"to": "5511999999999", "chat_id": "-100", "url": "https://ex.com/h",
		"password": marca, "token": marca, "api_key": marca, "apikey": marca,
		"secret": marca, "authorization": marca, "bot_token": marca,
	}
	for _, tipo := range []string{"smtp", "telegram", "whatsapp", "webhook", "outro"} {
		if d := DestinoDo(tipo, cfg); contemMarca(d, marca) {
			t.Fatalf("tipo %q vazou segredo no destino: %q", tipo, d)
		}
	}
}

func contemMarca(s, marca string) bool {
	return len(s) >= len(marca) && (s == marca || indexOf(s, marca) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
