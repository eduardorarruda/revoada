package notify

import (
	"fmt"
	"net/url"
	"strings"
)

// DestinoDo devolve, em uma linha legível, PARA ONDE uma mensagem sai por este
// canal — o número de WhatsApp, o e-mail, o chat do Telegram, o host do webhook.
//
// Existe porque o histórico de envios dizia apenas "whatsapp": com dois canais
// de WhatsApp cadastrados, não havia como saber qual número recebeu (ou deixou
// de receber) o alerta, que é exatamente a pergunta de quem reclama não ter sido
// avisado. O valor é gravado junto do envio (notification_log.destination), não
// consultado depois: canal renomeado, redirecionado ou apagado não pode reescrever
// o passado.
//
// NUNCA devolve segredo. Só lê campos de endereçamento; senha, token e api_key
// não são consultados aqui em nenhuma hipótese — e é por isso que este código
// enumera os campos que lê em vez de despejar o config inteiro.
func DestinoDo(tipo string, cfg map[string]any) string {
	texto := func(k string) string {
		v, ok := cfg[k]
		if !ok || v == nil {
			return ""
		}
		return strings.TrimSpace(fmt.Sprint(v))
	}

	switch strings.ToLower(tipo) {
	case "smtp":
		return texto("to")
	case "telegram":
		if id := texto("chat_id"); id != "" {
			return "chat " + id
		}
	case "whatsapp":
		// O canal aceita um destinatário ou uma lista; a tela mostra todos, porque
		// "foi para dois dos três números" é a informação que interessa.
		if to := texto("to"); to != "" {
			return to
		}
		if lista, ok := cfg["numbers"].([]any); ok {
			var ns []string
			for _, n := range lista {
				if s := strings.TrimSpace(fmt.Sprint(n)); s != "" {
					ns = append(ns, s)
				}
			}
			return strings.Join(ns, ", ")
		}
	case "webhook":
		// Só host + caminho: a URL de webhook costuma trazer o segredo na query
		// (…/hooks/T00/B01/XXXX ou ?token=…), e o histórico é lido por qualquer
		// admin. Host e caminho bastam para identificar o destino.
		if u := texto("url"); u != "" {
			if p, err := url.Parse(u); err == nil && p.Host != "" {
				return p.Host + p.Path
			}
			return "(url inválida)"
		}
	}
	return ""
}
