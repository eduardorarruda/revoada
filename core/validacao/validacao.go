// Package validacao valida e sanitiza entradas na fronteira do sistema (itens 5 e 6
// da lista de segurança): tudo que vem do usuário, da API ou do MCP passa por aqui
// antes de virar dado gravado, comando ou consulta.
package validacao

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Erro de validação com o campo, para a tela mostrar ao lado dele.
type Erro struct {
	Campo    string `json:"campo"`
	Mensagem string `json:"mensagem"`
}

func (e Erro) Error() string { return e.Campo + ": " + e.Mensagem }

func falha(campo, f string, a ...any) error {
	return Erro{Campo: campo, Mensagem: fmt.Sprintf(f, a...)}
}

// Texto limpa um texto livre (nome, rótulo, descrição): tira espaços das pontas,
// remove caracteres de controle e invisíveis (inclusive os de inversão de direção,
// usados para disfarçar nomes) e confere o tamanho em caracteres.
func Texto(campo, s string, obrigatorio bool, maximo int) (string, error) {
	if !utf8.ValidString(s) {
		return "", falha(campo, "texto com codificação inválida")
	}
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(' ')
		case unicode.IsControl(r), unicode.Is(unicode.Cf, r): // controle e formatação (U+200B, U+202E…)
			continue
		default:
			b.WriteRune(r)
		}
	}
	limpo := strings.Join(strings.Fields(b.String()), " ")
	if obrigatorio && limpo == "" {
		return "", falha(campo, "obrigatório")
	}
	if utf8.RuneCountInString(limpo) > maximo {
		return "", falha(campo, "máximo de %d caracteres", maximo)
	}
	return limpo, nil
}

// HostPorta confere "host:porta" (porta 1–65535; host = nome DNS ou IP).
func HostPorta(campo, s string, portaPadrao int) (string, error) {
	s = strings.TrimSpace(s)
	host, porta, err := net.SplitHostPort(s)
	if err != nil {
		if portaPadrao == 0 {
			return "", falha(campo, "use host:porta (ex.: banco.empresa.com:5432)")
		}
		host, porta = s, strconv.Itoa(portaPadrao)
	}
	n, err := strconv.Atoi(porta)
	if err != nil || n < 1 || n > 65535 {
		return "", falha(campo, "porta inválida")
	}
	if !hostValido(host) {
		return "", falha(campo, "host inválido")
	}
	return net.JoinHostPort(host, porta), nil
}

func hostValido(h string) bool {
	if net.ParseIP(h) != nil {
		return true
	}
	if h == "" || len(h) > 253 {
		return false
	}
	for _, parte := range strings.Split(h, ".") {
		if parte == "" || len(parte) > 63 || strings.HasPrefix(parte, "-") || strings.HasSuffix(parte, "-") {
			return false
		}
		for _, r := range parte {
			if !(r == '-' || r == '_' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
				return false
			}
		}
	}
	return true
}

// ErrCaminho: caminho com caractere proibido.
var ErrCaminho = errors.New("caminho inválido")

// CaminhoBanco confere o caminho do arquivo do banco (Firebird) ou o nome do banco
// (PostgreSQL): sem NUL, sem quebra de linha, sem "..", sem caracteres de shell.
func CaminhoBanco(campo, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 512 {
		return "", falha(campo, "obrigatório (até 512 caracteres)")
	}
	if strings.ContainsAny(s, "\x00\r\n;|&$`<>\"'") || strings.Contains(s, "..") {
		return "", falha(campo, "caracteres não permitidos no caminho")
	}
	return s, nil
}

// Usuario de banco: letras, números e _.@- (sem espaço nem aspas).
func Usuario(campo, s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" || len(s) > 128 {
		return "", falha(campo, "obrigatório (até 128 caracteres)")
	}
	for _, r := range s {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune("_.@-$", r)) {
			return "", falha(campo, "caractere %q não permitido", r)
		}
	}
	return s, nil
}

// Escolha confere que o valor está numa lista fechada.
func Escolha(campo, s string, opcoes ...string) (string, error) {
	s = strings.TrimSpace(s)
	for _, o := range opcoes {
		if s == o {
			return s, nil
		}
	}
	return "", falha(campo, "use um destes: %s", strings.Join(opcoes, ", "))
}

// Opcoes valida um mapa chave→valor de opções de conexão (chaves de uma lista
// fechada, valores curtos e sem controle).
func Opcoes(campo string, m map[string]string, permitidas ...string) (map[string]string, error) {
	out := map[string]string{}
	for k, v := range m {
		if _, err := Escolha(campo, k, permitidas...); err != nil {
			return nil, falha(campo, "opção %q desconhecida (use: %s)", k, strings.Join(permitidas, ", "))
		}
		limpo, err := Texto(campo+"."+k, v, false, 100)
		if err != nil {
			return nil, err
		}
		out[k] = limpo
	}
	return out, nil
}
