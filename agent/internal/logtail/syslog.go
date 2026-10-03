package logtail

// NewSyslog cria um coletor de syslog (source=syslog). Reaproveita integralmente
// a lógica de tail/offset/rotação/stitching do Tailer de arquivos, apenas seguindo
// os arquivos clássicos do syslog e marcando os registros com source=syslog.
// Best-effort: globs que não existem (ex.: /var/log/messages em Debian) são ignorados.
func NewSyslog(gatewayURL, key, host string) *Tailer {
	t := New(gatewayURL, key, host, []string{"/var/log/syslog", "/var/log/messages"})
	t.source = "syslog"
	return t
}
