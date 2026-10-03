package probe

// TABELA ÚNICA DE DIAGNÓSTICOS DA SONDAGEM — CÓPIA ESPELHADA
//
// A original vive em `server/internal/sitecheck/diagnosis.go`. Não dá para
// importá-la: `server/internal/...` é um pacote interno de OUTRO módulo Go
// (github.com/eduardorarruda/revoada/server), e a regra `internal` do Go proíbe o
// módulo agent de importá-lo. Enquanto não houver um módulo comum (ver relatório
// da frente), a tabela é duplicada literalmente e um teste dos dois lados trava
// a lista.
//
// Antes desta unificação os dois vocabulários divergiam — a central dizia "tls" e
// o agente "tls_error"; a central "http_status" e o agente "http_401"; o agente
// ainda tinha "unreachable", que não existia do outro lado. As duas sondas
// alimentam o MESMO consenso e a tela de detalhe mostra as duas lado a lado, o
// que obrigava o usuário a traduzir na cabeça.
const (
	DiagOK = "" // sem falha

	DiagDNSError      = "dns_error"       // o nome não resolve
	DiagConnRefused   = "connect_refused" // a porta respondeu "fechado" (ECONNREFUSED)
	DiagConnTimeout   = "connect_timeout" // estourou o tempo limite sem resposta
	DiagConnError     = "connect_error"   // conexão caiu/rota inexistente (reset, unreachable)
	DiagTLS           = "tls"             // handshake ou validação de certificado falhou
	DiagHTTP5xx       = "http_5xx"        // o servidor respondeu erro dele
	DiagHTTPStatus    = "http_status"     // status diferente do esperado (não-5xx)
	DiagKeywordAusent = "keyword_missing" // corpo COMPLETO lido e a palavra não estava lá
	DiagKeywordIndet  = "keyword_indeterminado"
	DiagSlow          = "slow"
	DiagBadURL        = "bad_url"
	DiagBloqueado     = "bloqueado_pelo_painel"
	DiagNaoClassif    = "unclassified"
)

// diagnosticosCanonicos é a lista travada por teste dos dois lados.
var diagnosticosCanonicos = []string{
	DiagDNSError, DiagConnRefused, DiagConnTimeout, DiagConnError, DiagTLS,
	DiagHTTP5xx, DiagHTTPStatus, DiagKeywordAusent, DiagKeywordIndet,
	DiagSlow, DiagBadURL, DiagBloqueado, DiagNaoClassif,
}
