package logtail

import (
	"regexp"
	"strings"
)

// Redaction de segredos ANTES de o log sair do host. É a defesa correta contra
// vazamento: se um container/serviço de terceiro imprime uma credencial no stdout
// (Authorization: Bearer, DATABASE_URL com senha, AWS key, JWT, chave privada), o
// valor é mascarado aqui e NUNCA chega ao ClickHouse — logo não fica pesquisável no
// painel por ninguém. Aplicado no choke point único (sink.post), cobrindo todas as
// fontes (arquivo, journald, docker, syslog, kernel).
//
// Filosofia: preservar o CONTEXTO (nome da chave, esquema da URL, prefixo do
// header) e mascarar só o SEGREDO — o log continua útil para depurar. Conservador
// o bastante para não estragar logs normais (exige tamanho mínimo do valor).

const redactMark = "«redigido»"

// Fragmentos do padrão chave=valor, montados abaixo em duas regras.
const (
	// nomeAfixo permite prefixo e sufixo no nome da chave. A versão anterior exigia
	// que o nome sensível terminasse EXATAMENTE antes do `=`/`:`, e por isso deixava
	// passar, comprovadamente: AWS_SECRET_ACCESS_KEY=, SECRET_KEY=, api_key_id=,
	// {"secretKey":…}, {"private_key":…}, token_secreto:. Nomes de variável de
	// ambiente e de campo JSON quase nunca são a palavra sensível sozinha.
	nomeAfixo = `[A-Za-z0-9_.-]*`
	// sep cobre `chave=valor`, `chave: valor`, `"chave":"valor"` (JSON), o hash-rocket
	// `chave => valor` (Ruby/Rails/Perl) e `CHAVE 'valor'` (separado por espaço, como
	// PGPASSWORD).
	//
	// O hash-rocket não era só um formato faltando — era o PIOR desfecho possível.
	// Com o `sep` antigo (`"?\s*[=:]\s*"?`), a linha `{"password" => "Senha1"}` casava
	// a aspa, o `=`, e o `valor` consumia apenas o `>`: a saída virava
	// `{"password" =«redigido» "Senha1"}`. O segredo ficava INTACTO e a linha ainda
	// exibia o selo — quem auditasse procurando `«redigido»` concluiria que estava
	// limpa. Reproduzido em 10/08/2026 rodando redactSecrets sobre a linha crua.
	//
	// O ramo do espaço exige aspa depois de propósito. Sem isso, `token expired` viraria
	// `token «redigido»` e a redaction comeria a palavra que explica o log — mascarar
	// demais custa o contexto de que o operador precisa às 3 da manhã.
	sep = `"?(?:\s*(?:=>|[=:])\s*["']?|\s+["'])`
	// valor é o segredo: até o primeiro delimitador de campo. O `>` entra na exclusão
	// para que o valor nunca possa ser a segunda metade de um `=>`.
	valor = `[^\s"',;)}>]`
)

// redactors é a lista de (padrão, substituição). Compilados uma vez no load.
var redactors = []struct {
	re   *regexp.Regexp
	repl string
}{
	// Header Authorization: Bearer/Basic <token> — mantém o esquema, mascara o token.
	//
	// O separador é o `sep` compartilhado, e não `\s*[:=]\s*`, por um motivo medido: a
	// aplicação moderna não loga o header cru, loga o registro estruturado
	// (`{"authorization":"Bearer eyJ…"}`). A aspa entre o nome e os dois-pontos não é
	// espaço, então o `\s*` da versão anterior não casava NADA e o Bearer atravessava
	// intacto — auditado fim a fim em 2026-08: o token de sessão chegou em claro ao
	// ClickHouse e ficou pesquisável no painel.
	{regexp.MustCompile(`(?i)(authorization` + sep + `(?:bearer|basic|token)\s+)[A-Za-z0-9._~+/=-]{8,}`), `${1}` + redactMark},
	// ...e Authorization SEM esquema. Um número grande de APIs internas manda a chave
	// nua (`Authorization: 9f8e7d6c…`), e a regra acima, que exige bearer/basic/token,
	// deixava justamente essas passar. O piso é maior (12) porque sem o esquema o que
	// resta é "authorization: <palavra>", e um piso curto redigiria mensagens de
	// resultado ("authorization: denied", "authorization: ok") sem nenhum segredo dentro.
	{regexp.MustCompile(`(?i)(authorization` + sep + `)[A-Za-z0-9._~+/=-]{12,}`), `${1}` + redactMark},
	// Chave privada PEM — o marcador.
	{regexp.MustCompile(`(?i)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----`), redactMark + `-PRIVATE-KEY`},
	// ...e o CORPO. Mascarar só o marcador era uma falsa sensação de segurança: as
	// linhas base64 da chave chegam como registros próprios (o stitching não as
	// costura, porque não são indentadas), então um `SELECT body FROM logs ORDER BY
	// ts` reconstruía a chave inteira. Auditado em 2026-08 com chave real de teste:
	// o marcador saiu redigido e as duas linhas de corpo chegaram intactas.
	//
	// A regra é deliberadamente sobre a linha TODA: uma linha de log que não é nada
	// além de 40+ caracteres de base64 não é texto de aplicação — é material
	// criptográfico, e mascarar é a escolha certa mesmo nos poucos falsos positivos.
	//
	// `(?m)` + `[ \t]` em vez de `\s`: o corpo enviado pode já vir COSTURADO
	// (multiline.go), e sem o modo multiline o `^…$` só valeria para o texto inteiro
	// — uma linha de chave no meio de um bloco costurado escaparia. `[ \t]` (e não
	// `\s`, que inclui `\n`) mantém a âncora presa a UMA linha.
	{regexp.MustCompile(`(?m)^[ \t]*[A-Za-z0-9+/]{40,}={0,2}[ \t]*$`), redactMark + `-BLOCO-BASE64`},
	// Cookie de sessão — credencial VIVA: quem lê a tela de logs assume a sessão do
	// usuário final da aplicação do cliente. Servidor web e proxy logam header de
	// requisição por padrão, então este é um dos caminhos mais prováveis.
	//
	// Mesmo defeito do Authorization e mesma correção (`sep`): o proxy que loga em JSON
	// escreve `"cookie":"sid=…"`, e o `\s*[:=]\s*` da versão anterior não casava a aspa
	// — o cookie de sessão chegou em claro ao ClickHouse no teste fim a fim de 2026-08.
	// O valor exclui a aspa (senão a redaction comeria o fechamento do campo JSON) e a
	// cauda `(?:;\s*…)*` cobre o header com vários pares (`Cookie: a=1; b=2`), em que
	// antes só o PRIMEIRO par era mascarado e os demais seguiam viagem.
	{regexp.MustCompile(`(?i)((?:set-)?cookie` + sep + `)[^\s"';]{4,}(?:;\s*[^\s"';]+)*`), `${1}` + redactMark},
	// AWS Access Key ID.
	{regexp.MustCompile(`AKIA[0-9A-Z]{16}`), redactMark},
	// Chave no formato `sk-…` (OpenAI, Stripe restrita, e o resto do ecossistema que
	// copiou o prefixo). Não casava em nenhuma regra: não é `chave=valor` — aparece
	// solta no meio de uma mensagem de erro do SDK ("invalid api key sk-proj-…"), que é
	// exatamente quando ela é impressa. O piso de 16 evita `sk-teste`.
	{regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{16,}`), redactMark},
	// Credencial em linha de comando: `curl -u usuario:senha`. Passava inteira porque a
	// regra de connection string exige `://…@` e a de chave=valor exige um nome
	// sensível — aqui não há nem um nem outro. Linha de comando com -u vai parar no log
	// por dois caminhos rotineiros: script de deploy com `set -x` e mensagem de erro que
	// ecoa o comando que falhou. Mantém o usuário (contexto) e mascara só a senha.
	{regexp.MustCompile(`(?i)((?:^|\s)(?:-u|--user)[ =]+[^\s:'"]+:)[^\s'"]+`), `${1}` + redactMark},
	// GitHub token, Slack token.
	{regexp.MustCompile(`gh[pousr]_[A-Za-z0-9]{20,}`), redactMark},
	{regexp.MustCompile(`xox[baprs]-[A-Za-z0-9-]{10,}`), redactMark},
	// JWT (três segmentos base64url) — comum em Authorization e dumps de env.
	{regexp.MustCompile(`eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{6,}`), redactMark},
	// Senha embutida em connection string: scheme://user:SENHA@host → mascara a senha.
	{regexp.MustCompile(`([a-zA-Z][a-zA-Z0-9+.-]*://[^:/\s]+:)[^@/\s]{3,}(@)`), `${1}` + redactMark + `${2}`},
	// chave=valor / chave: valor para nomes sensíveis — mantém o nome, mascara o valor.
	//
	// Duas regras, e não uma, por causa do piso de tamanho do valor. O piso existe
	// para não estragar log normal (`token=1`), mas aplicá-lo a TUDO deixava passar
	// `{"password":"abc12"}` — uma senha curta continua sendo uma senha. Então:
	//
	//  1) nomes INEQUÍVOCOS (password/passwd/senha/pwd/secret) não têm piso: não
	//     existe uso legítimo de um campo chamado "senha" cujo valor possa vazar.
	//     O preço é mascarar `senhas: 3` num log de auditoria — troca deliberada,
	//     porque mascarar demais custa contexto e mascarar de menos custa a chave.
	//  2) nomes AMBÍGUOS (token/key/credential) mantêm o piso de 6 caracteres, onde
	//     o risco de falso positivo é real (`key=a`, `token=1` aparecem em log de
	//     aplicação com significado nenhum de segredo).
	//
	// A lista de nomes ambíguos era enumerada (api_key, private_key, serverkey,
	// access_key) e por isso não cobria a chave do PRÓPRIO painel: um teste com canário
	// mostrou `X-Revoada-Key:`, `x-revoada-key:`, `server_key=`, `server-key:`, `key=` e
	// `x-api-key:` chegando em claro ao ClickHouse. Enumerar variantes é uma corrida
	// perdida — hífen, underscore, camelCase e prefixo de header multiplicam sem fim —,
	// então a regra passa a ser o radical `key` com afixo livre, que subsume todas elas
	// (inclusive as antigas) sem depender de adivinhar a próxima grafia. O piso de 6
	// continua sendo o que separa segredo de contador: `key=a` segue intocado.
	{regexp.MustCompile(`(?i)(` + nomeAfixo + `(?:password|passwd|senha|pwd|secret)` + nomeAfixo + sep + `)` + valor + `+`), `${1}` + redactMark},
	{regexp.MustCompile(`(?i)(` + nomeAfixo + `(?:key|token|credential)` + nomeAfixo + sep + `)` + valor + `{6,}`), `${1}` + redactMark},
	// Forma XML/tag: `<password>segredo</password>`. O separador aqui é o próprio `>`,
	// que nenhum dos padrões acima alcança — e é formato comum em log de aplicação
	// Java/.NET e em dump de configuração.
	{regexp.MustCompile(`(?i)(<` + nomeAfixo + `(?:password|passwd|senha|pwd|secret)` + nomeAfixo + `>)[^<]+`), `${1}` + redactMark},
	{regexp.MustCompile(`(?i)(<` + nomeAfixo + `(?:key|token|credential)` + nomeAfixo + `>)[^<]{6,}`), `${1}` + redactMark},
}

// redactSecrets aplica todos os padrões de redaction a uma linha de log. Retorna a
// string original se nada casar (caminho mais comum — custo de alguns regexes já
// compilados por linha, aceitável no volume após o rate limit).
func redactSecrets(s string) string {
	for _, r := range redactors {
		if r.re.MatchString(s) {
			s = r.re.ReplaceAllString(s, r.repl)
		}
	}
	return redactCartoes(s)
}

// Número de cartão (PAN): 13 a 19 dígitos, contíguos ou em grupos com espaço/hífen.
// Regex sozinha não basta — epoch em milissegundos e ids de pedido também são números
// longos, e uma sequência aleatória passa no Luhn uma vez em dez. Por isso o candidato
// só é mascarado se tiver prefixo de bandeira conhecida E dígito verificador válido.
// Os quatro últimos ficam (é o que o atendimento usa e o PCI permite exibir).
var reCandidatoCartao = regexp.MustCompile(`\b\d(?:[ -]?\d){12,18}\b`)

func redactCartoes(s string) string {
	if !reCandidatoCartao.MatchString(s) {
		return s
	}
	return reCandidatoCartao.ReplaceAllStringFunc(s, func(m string) string {
		d := strings.NewReplacer(" ", "", "-", "").Replace(m)
		if !bandeiraConhecida(d) || !luhn(d) {
			return m
		}
		return redactMark + "-" + d[len(d)-4:]
	})
}

// bandeiraConhecida: Visa, Mastercard (51–55 e 2221–2720), Amex, Diners, Discover,
// JCB, Elo e Hipercard — com o tamanho de cada uma.
func bandeiraConhecida(d string) bool {
	n := len(d)
	p := func(prefixos ...string) bool {
		for _, x := range prefixos {
			if strings.HasPrefix(d, x) {
				return true
			}
		}
		return false
	}
	switch {
	case p("4011", "4312", "4389", "4514", "4576", "5041", "5066", "5067", "509", "6277", "6362", "6363", "650", "6516", "6550", "606282", "3841"):
		return n >= 13 // Elo e Hipercard
	case d[0] == '4':
		return n == 13 || n == 16 || n == 19
	case p("51", "52", "53", "54", "55"):
		return n == 16
	case n == 16 && d[:4] >= "2221" && d[:4] <= "2720":
		return true
	case p("34", "37"):
		return n == 15
	case p("300", "301", "302", "303", "304", "305", "36", "38"):
		return n == 14
	case p("6011", "65"):
		return n == 16
	case p("35"):
		return n >= 16
	}
	return false
}

func luhn(d string) bool {
	soma := 0
	for i := 0; i < len(d); i++ {
		x := int(d[len(d)-1-i] - '0')
		if i%2 == 1 {
			if x *= 2; x > 9 {
				x -= 9
			}
		}
		soma += x
	}
	return soma%10 == 0
}
