package redacao

import (
	"strings"
	"testing"
)

func TestRedactSecrets(t *testing.T) {
	// Cada caso: a entrada DEVE ser mascarada (o segredo não pode sobrar no texto).
	secretCases := []struct {
		name, in, mustNotContain string
	}{
		// --- casos que já passavam antes (guarda de regressão) ---
		{"bearer", "Authorization: Bearer abcDEF123456ghijkl", "abcDEF123456ghijkl"},
		{"aws key id", "id=AKIAIOSFODNN7EXAMPLE fim", "AKIAIOSFODNN7EXAMPLE"},
		{"github token", "token ghp_0123456789abcdefghijklmnopqrstuvwx", "ghp_0123456789abcdefghijklmnopqrstuvwx"},
		{"slack token", "hook xoxb-1234567890-abcdefghij", "xoxb-1234567890-abcdefghij"},
		{"jwt", "auth eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U", "dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U"},
		{"connection string", "postgres://user:sup3rSecret@db:5432/x", "sup3rSecret"},
		{"password kv", `password="hunter2secret"`, "hunter2secret"},
		{"apikey kv", "apikey: k-9f8e7d6c5b4a3210", "k-9f8e7d6c5b4a3210"},
		{"private key marker", "-----BEGIN OPENSSH PRIVATE KEY-----", "BEGIN OPENSSH PRIVATE KEY"},
		{"corpo base64 da chave", "MIIEpAIBAAKCAQEAwJ8kL3nQ7fVxYtZbR2mHsD9aP0uGvCeNiXkTlOoRfBhUyMdWq", "MIIEpAIBAAKCAQEAwJ8kL3nQ7fVxYtZbR2mHsD9aP0uGvCeNiXkTlOoRfBhUyMdWq"},
		{"cookie de sessão", "Cookie: session=8f3b1c2d9e4a5b6c7d8e", "8f3b1c2d9e4a5b6c7d8e"},

		// --- (a) nome sensível com prefixo/sufixo: vazavam antes ---
		{"env AWS_SECRET_ACCESS_KEY", "AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG/bPxRfiCY", "wJalrXUtnFEMI"},
		{"env SECRET_KEY", "SECRET_KEY=django-insecure-abc123", "django-insecure-abc123"},
		{"sufixo api_key_id", "api_key_id=AKIA00112233445566", "AKIA00112233445566"},
		{"json secretKey", `{"secretKey":"s3cr3t-value-here"}`, "s3cr3t-value-here"},
		{"json private_key", `{"private_key":"-----BEGIN"}`, "-----BEGIN"},
		{"prefixo token_secreto", "token_secreto: abcd1234efgh", "abcd1234efgh"},
		{"env DB_PASSWORD", "DB_PASSWORD=trocar-isso-agora", "trocar-isso-agora"},
		{"camelCase apiKeyValue", "apiKeyValue=9f8e7d6c5b4a", "9f8e7d6c5b4a"},
		{"plural credentials", "credentials=abcdef123456", "abcdef123456"},
		{"env SERVERKEY_PROD", "SERVERKEY_PROD=zzzz999988887777", "zzzz999988887777"},
		{"access_token com sufixo", "access_token_v2=aaaaaaaaaaaa", "aaaaaaaaaaaa"},

		// --- (b) valor curto em nome inequívoco: vazava antes ---
		{"senha curta json", `{"password":"abc12"}`, "abc12"},
		{"senha curta kv", "senha=1234", "1234"},
		{"pwd curto", "pwd: xyz", "xyz"},
		{"passwd curto", "passwd=ab", "=ab"},
		{"secret curto", "secret=q1", "=q1"},

		// --- (c) log ESTRUTURADO: a aspa entre nome e `:` quebrava o `\s*` e o header
		// atravessava intacto até o ClickHouse (auditado fim a fim em 2026-08) ---
		{"authorization json bearer", `{"authorization":"Bearer eyJhbGciOiJIUzI1NiJ9x"}`, "eyJhbGciOiJIUzI1NiJ9x"},
		{"authorization json minusculo", `{"level":"info","authorization":"Basic dXNlcjpzZW5oYQ=="}`, "dXNlcjpzZW5oYQ=="},
		{"cookie json", `{"cookie":"sid=8f3b1c2d9e4a5b6c7d8e"}`, "8f3b1c2d9e4a5b6c7d8e"},
		{"set-cookie json", `{"set-cookie":"session=abcd1234efgh5678"}`, "abcd1234efgh5678"},
		// header cru com vários pares: antes só o primeiro par era mascarado.
		{"cookie cru com dois pares", "Cookie: a=1234abcd; sid=8f3b1c2d9e4a", "8f3b1c2d9e4a"},

		// --- (d) chave do próprio painel e variantes de nome: vazaram com canário ---
		{"X-Revoada-Key header", "X-Revoada-Key: canario-9f8e7d6c5b4a", "canario-9f8e7d6c5b4a"},
		{"x-revoada-key minusculo", "x-revoada-key: canario-1122334455", "canario-1122334455"},
		{"server_key kv", "server_key=canario-aabbccddeeff", "canario-aabbccddeeff"},
		{"server-key header", "server-key: canario-0099887766", "canario-0099887766"},
		{"key isolado com valor longo", "key=canario-abcdef123456", "canario-abcdef123456"},
		{"x-api-key header", "x-api-key: canario-zzzz11112222", "canario-zzzz11112222"},
		{"revoada_key json", `{"revoada_key":"canario-777788889999"}`, "canario-777788889999"},
		{"authorization sem esquema", "Authorization: 9f8e7d6c5b4a3210ffee", "9f8e7d6c5b4a3210ffee"},

		// --- (e) formatos que não são chave=valor nenhum ---
		{"curl -u usuario:senha", "curl -u ana:sup3rSenha https://api.exemplo/x", "sup3rSenha"},
		{"curl --user", "curl --user=admin:trocar123 https://api.exemplo/x", "trocar123"},
		{"chave sk-", "SDK erro: invalid api key sk-proj-AbCdEf0123456789XyZ", "sk-proj-AbCdEf0123456789XyZ"},
	}
	for _, c := range secretCases {
		t.Run(c.name, func(t *testing.T) {
			got := Texto(c.in)
			if strings.Contains(got, c.mustNotContain) {
				t.Fatalf("segredo não mascarado: %q -> %q", c.in, got)
			}
			if !strings.Contains(got, Marca) {
				t.Fatalf("faltou o marcador de redaction em %q", got)
			}
		})
	}
}

// TestRedactPreservaContexto: mascarar não pode apagar o NOME da chave — é ele que
// permite ao operador entender o que aconteceu sem ver o segredo.
func TestRedactPreservaContexto(t *testing.T) {
	cases := []struct{ in, mustContain string }{
		{"AWS_SECRET_ACCESS_KEY=wJalrXUtnFEMI/K7MDENG", "AWS_SECRET_ACCESS_KEY="},
		{`{"password":"abc12"}`, `"password":"`},
		{"Authorization: Bearer abcDEF123456ghijkl", "Authorization: Bearer "},
		{"postgres://user:sup3rSecret@db:5432/x", "postgres://user:"},
		// estruturado: o nome do campo (e o esquema) continuam legíveis.
		{`{"authorization":"Bearer eyJhbGciOiJIUzI1NiJ9x"}`, `"authorization":"Bearer `},
		{`{"cookie":"sid=8f3b1c2d9e4a5b6c7d8e"}`, `"cookie":"`},
		{"X-Revoada-Key: canario-9f8e7d6c5b4a", "X-Revoada-Key: "},
		{"curl -u ana:sup3rSenha https://api.exemplo/x", "curl -u ana:"},
	}
	for _, c := range cases {
		if got := Texto(c.in); !strings.Contains(got, c.mustContain) {
			t.Errorf("perdeu o contexto %q: %q -> %q", c.mustContain, c.in, got)
		}
	}
}

// TestRedactSemFalsoPositivo: log normal não pode ser mutilado. Cobre em especial a
// regra do bloco base64 da linha inteira — a mais agressiva do conjunto — com linhas
// longas, com hash, com id e com stack trace, que são o que de fato circula.
func TestRedactSemFalsoPositivo(t *testing.T) {
	keep := []string{
		"GET /api/hosts 200 in 12ms",
		"debug=1 verbose=true retries=3",
		"user ana logou com sucesso",
		"cpu=42% mem=13MB conexões=7",
		// piso de tamanho preservado nos nomes ambíguos
		"token=1 tentativa=2",
		"key=a",
		// linhas longas sem ser base64 puro (têm espaço, ponto, barra, dois-pontos)
		"2026-08-09T12:00:00Z INFO requisição concluída para /v1/relatorios/mensal com 200 em 143ms",
		"arquivo /var/lib/revoada-agent/buffer/20260809T120000-metrics.otlp enviado com sucesso",
		"sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08 verificado",
		"processando pedido 4815162342 do cliente 108 na fila principal do sistema",
		// stack trace costurado: nenhuma linha é base64 puro
		"NullPointerException: x\n  at com.foo.Bar.baz(Bar.java:42)\n  at com.foo.App.main(App.java:7)",
		// palavras que CONTÊM fragmentos sensíveis mas sem separador: nada a mascarar
		"rotina de troca de senhas concluída",
		"tokenizador inicializado",
		// piso do `key` isolado: contador e enum curtos seguem legíveis
		"key=ok",
		"sort_key=id",
		// authorization sem esquema tem piso maior justamente para estes
		"authorization: denied",
		"authorization=ok",
		// `sk-` curto não é chave
		"branch sk-teste criada",
	}
	for _, in := range keep {
		if got := Texto(in); got != in {
			t.Errorf("log normal alterado: %q -> %q", in, got)
		}
	}
}

// TestRedactBlocoBase64EmCorpoCosturado: as linhas de corpo de uma chave privada
// chegam como texto de várias linhas quando algo as costura; o `(?m)` garante que
// CADA linha seja avaliada, não só o texto inteiro.
func TestRedactBlocoBase64EmCorpoCosturado(t *testing.T) {
	in := "erro ao ler chave:\nMIIEpAIBAAKCAQEAwJ8kL3nQ7fVxYtZbR2mHsD9aP0uGvCeNiXkTlOoRfBhUyMdWq\nfim"
	got := Texto(in)
	if strings.Contains(got, "MIIEpAIBAAKCAQEAwJ8k") {
		t.Fatalf("corpo base64 no meio do texto não foi mascarado: %q", got)
	}
	if !strings.Contains(got, "erro ao ler chave:") || !strings.Contains(got, "fim") {
		t.Fatalf("as linhas normais em volta foram perdidas: %q", got)
	}
}

// Formatos de separador que vazavam. O primeiro é o mais grave e o motivo deste teste
// existir: com o `sep` antigo, `{"password" => "x"}` saía como
// `{"password" =«redigido» "x"}` — segredo INTACTO e ainda com o selo de redigido, de
// modo que uma auditoria procurando `«redigido»` daria a linha por limpa. Falso
// negativo carimbado de positivo é pior que falso negativo.
func TestRedactSeparadoresQueVazavam(t *testing.T) {
	casos := []struct{ entrada, segredo string }{
		{`{"password" => "SenhaSuperSecreta1"}`, "SenhaSuperSecreta1"},        // hash-rocket Ruby/Rails
		{`api_key => 'chave-super-secreta-1234'`, "chave-super-secreta-1234"}, // idem, nome ambíguo
		{`<password>SegredoXML</password>`, "SegredoXML"},                     // forma XML/tag
		{`PGPASSWORD 'SegredoEspaco'`, "SegredoEspaco"},                       // separado por espaço
		{`export SECRET_KEY="SegredoDeAmbiente"`, "SegredoDeAmbiente"},        // env com aspas
	}
	for _, c := range casos {
		got := Texto(c.entrada)
		if strings.Contains(got, c.segredo) {
			t.Errorf("VAZOU: %q -> %q (o segredo %q continua na linha)", c.entrada, got, c.segredo)
		}
		if !strings.Contains(got, Marca) {
			t.Errorf("%q -> %q: sem o selo de redigido", c.entrada, got)
		}
	}
}

// O outro lado da mesma moeda: mascarar demais custa o contexto que o operador precisa
// às 3 da manhã. O ramo do separador por ESPAÇO exige aspa depois exatamente para que
// a palavra seguinte a "token"/"password" num texto corrido não seja comida.
func TestRedactNaoComePalavraDeTextoCorrido(t *testing.T) {
	for _, c := range []string{
		"token expired after 30s",
		"password reset requested by user 42",
		"secret rotation scheduled",
		"key=a",   // piso de 6 no nome ambíguo
		"token=1", // idem
	} {
		if got := Texto(c); got != c {
			t.Errorf("redigiu demais: %q -> %q", c, got)
		}
	}
}
