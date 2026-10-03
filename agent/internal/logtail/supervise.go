package logtail

import (
	"context"
	"log/slog"
	"time"
)

// Supervise roda run(ctx) e o reinicia com backoff exponencial sempre que ele
// retorna antes do contexto ser cancelado. Os coletores de stream (journald,
// docker, kmsg) terminam no primeiro erro de leitura — sem supervisão, um restart
// do dockerd, um overrun do ring buffer do kernel (EPIPE) ou a morte do journalctl
// matariam a coleta de vez, em silêncio. Aqui ela se recupera.
//
// Falha rápida repetida (cada execução dura menos que resetAfter) é tratada como
// indisponibilidade permanente (dependência ausente, sem permissão): após
// maxFastFails tentativas seguidas, desiste com log de erro — evita girar para
// sempre quando o recurso simplesmente não existe. Uma execução longa reseta o
// contador (morte transiente).
func Supervise(ctx context.Context, log *slog.Logger, source string, run func(context.Context)) {
	const (
		minBackoff   = 2 * time.Second
		maxBackoff   = 60 * time.Second
		resetAfter   = 30 * time.Second
		maxFastFails = 5
	)
	backoff := minBackoff
	fastFails := 0
	for {
		start := time.Now()
		run(ctx)
		if ctx.Err() != nil {
			return
		}
		if time.Since(start) >= resetAfter {
			fastFails = 0 // rodou bastante → morte transiente; reinicia limpo
			backoff = minBackoff
		} else {
			fastFails++
			if fastFails >= maxFastFails {
				log.Error("coletor de log falha repetidamente ao iniciar; desistindo (verifique dependência/permissão)", "source", source)
				return
			}
		}
		log.Warn("coletor de log parou; reiniciando", "source", source, "em", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, maxBackoff)
	}
}
