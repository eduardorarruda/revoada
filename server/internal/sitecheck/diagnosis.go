package sitecheck

// TABELA ÚNICA DE DIAGNÓSTICOS DA SONDAGEM
//
// Antes existiam DOIS vocabulários: o da sonda central (dns_error, connect_timeout,
// tls, http_5xx, http_status, keyword_missing, slow) e o da sonda do agente
// (dns_error, connect_timeout, tls_error, connect_refused, unreachable, http_NNN).
// As duas alimentam o MESMO consenso e aparecem lado a lado na tela de detalhe, o
// que obrigava o usuário a traduzir "tls" ↔ "tls_error" e "unreachable" ↔ o quê.
//
// Esta é a lista canônica. `agent/internal/probe/diagnosis.go` mantém uma cópia
// literal desta tabela (os módulos Go são separados e `server/internal/...` não é
// importável do módulo agent — ver relatório da frente). Qualquer alteração aqui
// tem de ser espelhada lá; há teste dos dois lados travando a lista.
const (
	DiagOK = "" // sem falha

	DiagDNSError    = "dns_error"       // o nome não resolve
	DiagConnRefused = "connect_refused" // a porta respondeu "fechado" (ECONNREFUSED)
	DiagConnTimeout = "connect_timeout" // estourou o tempo limite sem resposta
	DiagConnError   = "connect_error"   // conexão caiu/rota inexistente (reset, unreachable)
	// Conectou (e negociou TLS, quando https), mas nenhum byte de resposta veio até o
	// prazo. É a assinatura de servidor com a fila cheia — Apache no teto de workers,
	// PHP-FPM sem filho livre — vista no WHM em 25/09. Saía como connect_timeout, o
	// mesmo rótulo de quando o pedido de conexão nem é respondido; a diferença é a
	// que diz o que consertar.
	DiagSemResposta   = "sem_resposta"
	DiagTLS           = "tls"             // handshake ou validação de certificado falhou
	DiagHTTP5xx       = "http_5xx"        // o servidor respondeu erro dele
	DiagHTTPStatus    = "http_status"     // status diferente do esperado (não-5xx)
	DiagKeywordAusent = "keyword_missing" // corpo COMPLETO lido e a palavra não estava lá
	// DiagKeywordIndet: o corpo foi truncado no teto de leitura ANTES do fim, então
	// não dá para afirmar que a palavra não existe. Nunca reporte keyword_missing
	// nesse caso (era falso DOWN: site no ar, 200, painel declarando falha).
	DiagKeywordIndet = "keyword_indeterminado"
	DiagSlow         = "slow"    // respondeu certo, acima da latência máxima
	DiagBadURL       = "bad_url" // a URL não é montável/esquema não permitido
	DiagBloqueado    = "bloqueado_pelo_painel"
	// DiagRedirectLoop: o alvo mandou o cliente de volta mais vezes que o limite
	// (loop http↔https, .htaccess quebrado, WordPress com siteurl errado). É falha
	// REAL do alvo — o site está inutilizável para qualquer visitante. Antes vinha
	// embrulhado em ErrBlockedAddress e virava `bloqueado_pelo_painel`, o que fazia
	// o checker sair antes da máquina de estados: UP para sempre, uptime intacto,
	// sem alerta. Ver safehttp.ErrTooManyRedirects.
	DiagRedirectLoop = "redirect_loop"
	// DiagNaoClassificado substitui o antigo `default: return "connect_timeout"`, que
	// transformava todo erro imprevisto em "timeout" (medido no dev: 1.172 registros
	// com connect_timeout e avg(total_ms)=0,33 ms — a assinatura de porta fechada,
	// não de timeout).
	DiagNaoClassificado = "unclassified"
)

// DiagnosisIsOurFault diz se o diagnóstico descreve uma recusa NOSSA e não uma
// falha do alvo. Resultado assim não pode derrubar o check nem entrar no uptime.
func DiagnosisIsOurFault(d string) bool {
	return d == DiagBloqueado
}

// DiagnosisIsInconclusive diz se a sondagem NÃO CONSEGUIU DECIDIR — o corpo foi
// cortado no teto de leitura antes do fim, então a asserção de palavra-chave é
// indeterminada.
//
// Por que existe: o rótulo `keyword_indeterminado` foi criado para dizer "não
// sabemos", mas a consequência continuou sendo a de uma falha — Result.OK=false,
// gravado ok=false, entrando no numerador negativo E no denominador do uptime, e
// duas seguidas viravam DOWN + notificação "Site DOWN" com severidade crítica.
// Medido: a mesma página de 1.024.013 bytes com a palavra a 700 KiB dava
// ok=true na sonda central (teto 8 MiB) e up=false na sonda do agente (teto
// 512 KiB) — duas "falhas" no consenso e DOWN de um site no ar, acusando o
// cliente pelo tamanho da própria landing page. Indeterminado agora recebe o
// mesmo tratamento de DiagBloqueado: não vira DOWN, não notifica, e fica fora do
// numerador e do denominador do uptime. Uma pergunta sem resposta não é um "não".
func DiagnosisIsInconclusive(d string) bool {
	return d == DiagKeywordIndet
}

// DiagnosisIsUnmeasured reúne os diagnósticos que NÃO são medida do alvo: a
// recusa do nosso guard e a asserção indeterminada. Nenhum deles pode derrubar um
// check nem entrar na conta de uptime.
func DiagnosisIsUnmeasured(d string) bool {
	return DiagnosisIsOurFault(d) || DiagnosisIsInconclusive(d)
}
