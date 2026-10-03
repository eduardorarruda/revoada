package alerting

import (
	"errors"
	"regexp"
	"strings"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// ignorados é o conjunto de containers que o painel deixou de vigiar, chave
// "host\x00container". Montado uma vez por ciclo; nil = nenhum ignorado.
//
// Vale para TODA regra cuja série traga os dois rótulos (host e container): quem
// ignora um container parado de propósito não quer o "Container caído" dele nem o
// "memória do container" de um container que não existe mais em uso. Alertas do
// servidor (sem rótulo container) não são afetados.
type ignorados map[string]bool

func chaveIgnorado(host, container string) string { return host + "\x00" + container }

func novosIgnorados(lista []store.IgnoredContainer) ignorados {
	if len(lista) == 0 {
		return nil
	}
	ig := make(ignorados, len(lista))
	for _, c := range lista {
		ig[chaveIgnorado(c.Host, c.Container)] = true
	}
	return ig
}

// contem diz se a série/alerta com estes rótulos é de um container ignorado.
func (ig ignorados) contem(labels map[string]string) bool {
	if len(ig) == 0 {
		return false
	}
	host, ctr := labels["host"], labels["container"]
	if host == "" || ctr == "" {
		return false
	}
	return ig[chaveIgnorado(host, ctr)]
}

// Nomes de container do Docker: [A-Za-z0-9][A-Za-z0-9_.-]*. Hostname: letras,
// dígitos, ponto, hífen e sublinhado. Vírgula nunca — é o separador de vários
// containers numa mesma regra (ver splitContainers).
var (
	reContainerIgnorado = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,254}$`)
	reHostIgnorado      = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,252}$`)
)

// validarIgnorado apara e confere o par (host, container) vindo da tela.
func validarIgnorado(host, container string) (string, string, error) {
	host, container = strings.TrimSpace(host), strings.TrimSpace(container)
	if !reHostIgnorado.MatchString(host) {
		return "", "", errors.New("informe o servidor (hostname) do container")
	}
	if !reContainerIgnorado.MatchString(container) {
		return "", "", errors.New("nome de container inválido: use o nome como aparece no Docker (letras, números, _ . -)")
	}
	return host, container, nil
}
