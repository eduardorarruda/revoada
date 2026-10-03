package alerting

import (
	"context"
	"log/slog"
)

// LogNotifier apenas registra transições no log. É o notificador padrão até a
// P4.2 (SMTP/webhook/Telegram + roteamento) entrar no lugar.
type LogNotifier struct{ Log *slog.Logger }

func (n LogNotifier) Notify(_ context.Context, note Notification) {
	n.Log.Info("notificação de alerta",
		"state", note.State, "rule", note.Rule.Name, "severity", note.Rule.Severity,
		"value", note.Value, "labels", note.Labels)
}
