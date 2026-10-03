package logtail

import (
	"context"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
)

// Kmsg coleta o ring buffer do kernel (dmesg, source=kernel) seguindo /dev/kmsg.
// Cada registro tem o formato `prefix;message`, com prefix = `priority,seq,ts_us,flags`.
// A severidade vem de (priority & 7). Best-effort: sem permissão de leitura
// (requer CAP_SYSLOG ou kernel.dmesg_restrict=0), emite warn e encerra.
type Kmsg struct {
	sink *sink
	log  *slog.Logger
}

func NewKmsg(gatewayURL, key, host string, log *slog.Logger) *Kmsg {
	return &Kmsg{sink: newSink(gatewayURL, key, host), log: log}
}

func (k *Kmsg) Run(ctx context.Context) {
	f, err := os.Open("/dev/kmsg")
	if err != nil {
		k.log.Warn("dmesg/kernel: sem permissão para /dev/kmsg (requer CAP_SYSLOG), coletor desativado", "err", err)
		return
	}
	// Pula o backlog: seek para o fim → só mensagens novas.
	_, _ = f.Seek(0, io.SeekEnd)
	// Read em /dev/kmsg é bloqueante; fechar o arquivo no cancelamento desbloqueia.
	go func() { <-ctx.Done(); _ = f.Close() }()

	recs := make(chan record, 1024)
	go func() {
		defer close(recs)
		buf := make([]byte, 8192) // cada Read devolve exatamente um registro
		for {
			if ctx.Err() != nil {
				return
			}
			n, err := f.Read(buf)
			if err != nil {
				return // ctx cancelado (arquivo fechado) ou erro real: encerra
			}
			sev, body, ok := parseKmsg(string(buf[:n]))
			if !ok || body == "" {
				continue
			}
			select {
			case recs <- record{service: "kernel", severity: sev, body: body, labels: map[string]string{"source": "kernel"}}:
			case <-ctx.Done():
				return
			}
		}
	}()
	k.sink.drain(ctx, recs)
}

// parseKmsg decodifica um registro do /dev/kmsg. Formato: `prio,seq,ts_us,flags;message`.
// level = prio & 7 → severity. Pega apenas a primeira linha da mensagem (registros
// podem ter linhas de continuação key=value indentadas, que ignoramos).
func parseKmsg(rec string) (severity, body string, ok bool) {
	i := strings.IndexByte(rec, ';')
	if i < 0 {
		return "", "", false
	}
	prefix := rec[:i]
	body = rec[i+1:]
	if j := strings.IndexByte(body, '\n'); j >= 0 {
		body = body[:j]
	}
	fields := strings.SplitN(prefix, ",", 2)
	prio, err := strconv.Atoi(strings.TrimSpace(fields[0]))
	if err != nil {
		return "", "", false
	}
	return severityFromPriority(prio & 7), strings.TrimSpace(body), true
}
