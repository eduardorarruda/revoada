package logtail

import "github.com/eduardorarruda/revoada/core/redacao"

// As regras de redaction moram em core/redacao, compartilhadas com o gateway (que as
// aplica ao conteúdo dos spans de IA). O tailer continua chamando redactSecrets no
// choke point único (record.go), cobrindo todas as fontes de log.

const redactMark = redacao.Marca

func redactSecrets(s string) string { return redacao.Texto(s) }
