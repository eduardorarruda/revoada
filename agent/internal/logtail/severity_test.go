package logtail

import "testing"

func TestSeverityExceptionsAndStacks(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		{"exceção Java CamelCase", "java.lang.NullPointerException: falhou", "ERROR"},
		{"erro Java CamelCase", "java.lang.OutOfMemoryError: heap", "ERROR"},
		{"corpo costurado com frame at", "NullPointerException: x\n  at com.foo.Bar.baz(Bar.java:42)", "ERROR"},
		{"traceback python", "Traceback (most recent call last):\n  File \"a.py\", line 1", "ERROR"},
		{"palavra error solta", "Error: conexão recusada", "ERROR"},
		{"aviso", "WARN: fila cheia", "WARN"},
		// O que a regex não reconhece é UNKNOWN, não INFO: dizer "informativo" sobre
		// uma linha não classificada é afirmar o que não se sabe, e esconde incidente
		// em formato não previsto atrás do rótulo de rotina.
		{"linha normal com 'terror' não é erro", "o filme de terror começou", "UNKNOWN"},
		{"linha sem classificação", "requisição concluída em 12ms", "UNKNOWN"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := severity(c.line); got != c.want {
				t.Fatalf("severity(%q) = %q, quero %q", c.line, got, c.want)
			}
		})
	}
}

// TestSeverityLogEstruturado cobre o que a regex de palavra nunca poderia acertar:
// nível numérico (pino/bunyan) e rótulo fora da lista de palavras. Era o caso
// medido em que 49 de 51 linhas de um serviço Node caíam em UNKNOWN e o painel
// mostrava "0 erros" para um serviço que só imprimia erro.
func TestSeverityLogEstruturado(t *testing.T) {
	cases := []struct {
		name string
		line string
		want string
	}{
		// pino: nível numérico, sem nenhuma palavra classificável no texto
		{"pino error 50", `{"level":50,"time":1754740000000,"msg":"conexão recusada"}`, "ERROR"},
		{"pino fatal 60", `{"level":60,"msg":"encerrando"}`, "FATAL"},
		{"pino warn 40", `{"level":40,"msg":"fila com 900 itens"}`, "WARN"},
		{"pino info 30", `{"level":30,"msg":"pronto"}`, "INFO"},
		{"pino debug 20", `{"level":20,"msg":"cache hit"}`, "DEBUG"},
		{"pino trace 10", `{"level":10,"msg":"entrou"}`, "TRACE"},
		{"nível customizado entre faixas", `{"level":35,"msg":"x"}`, "INFO"},
		// rótulos fora da lista de palavras da regex
		{"critical minúsculo", `{"level":"critical","msg":"disco cheio"}`, "FATAL"},
		{"CRITICAL maiúsculo", `{"level":"CRITICAL","msg":"disco cheio"}`, "FATAL"},
		{"alert", `{"severity":"alert","msg":"replicação parada"}`, "FATAL"},
		{"lvl abreviado", `{"lvl":"err","msg":"timeout"}`, "ERROR"},
		{"levelname python", `{"levelname":"WARNING","message":"lento"}`, "WARN"},
		{"notice vira INFO", `{"level":"notice","msg":"reload"}`, "INFO"},
		{"número em string", `{"level":"50","msg":"x"}`, "ERROR"},
		// escala syslog na faixa baixa (0..7)
		{"syslog crit 2", `{"severity":3,"msg":"x"}`, "ERROR"},
		{"syslog debug 7", `{"severity":7,"msg":"x"}`, "DEBUG"},
		// prefixo antes do JSON (runtime/supervisor) não atrapalha
		{"prefixo antes do json", `2026-08-09T12:00:00Z app | {"level":50,"msg":"falhou"}`, "ERROR"},
		// entrada costurada: o nível está no cabeçalho, o resto é stack trace
		{"costurado usa o cabeçalho", "{\"level\":50,\"msg\":\"boom\"}\n  at foo.js:1", "ERROR"},
		// o nível DECLARADO vence a adivinhação por palavra
		{"declarado info vence a palavra error", `{"level":30,"msg":"0 errors found"}`, "INFO"},
		// sem campo de nível: cai na regex, como antes
		{"json sem nível cai na regex", `{"msg":"ERROR ao gravar"}`, "ERROR"},
		{"json sem nível e sem palavra", `{"msg":"tudo certo"}`, "UNKNOWN"},
		// nível ininteligível não pode virar palpite
		{"nível desconhecido cai na regex", `{"level":"trololo","msg":"tudo certo"}`, "UNKNOWN"},
		{"faixa 8-9 não pertence a escala nenhuma", `{"level":9,"msg":"tudo certo"}`, "UNKNOWN"},
		// não é JSON: nada muda
		{"texto solto com chave", "map[a:1] {não é json} ok", "UNKNOWN"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := severity(c.line); got != c.want {
				t.Fatalf("severity(%q) = %q, quero %q", c.line, got, c.want)
			}
		})
	}
}
