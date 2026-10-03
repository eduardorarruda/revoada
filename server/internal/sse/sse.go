// Package sse escreve respostas text/event-stream com heartbeat. Usado pelo
// provisionamento (passos da instalação) e pelo canal (tarefas ao vivo).
package sse

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"
)

// IntervaloPing mantém a conexão viva atrás de proxies que derrubam conexão ociosa
// (Traefik/nginx): sem tráfego por um passo demorado, o stream seria cortado.
const IntervaloPing = 15 * time.Second

// Emissor envia eventos SSE. As escritas são serializadas (heartbeat × eventos).
type Emissor struct {
	w    http.ResponseWriter
	fl   http.Flusher
	mu   sync.Mutex
	pare chan struct{}
	uma  sync.Once
}

// Abrir prepara os headers e inicia o heartbeat. ok=false se a resposta não suporta flush.
func Abrir(w http.ResponseWriter) (*Emissor, bool) {
	fl, ok := w.(http.Flusher)
	if !ok {
		return nil, false
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // desativa o buffering do nginx
	w.WriteHeader(http.StatusOK)
	fl.Flush()
	e := &Emissor{w: w, fl: fl, pare: make(chan struct{})}
	go e.ping()
	return e, true
}

func (e *Emissor) ping() {
	t := time.NewTicker(IntervaloPing)
	defer t.Stop()
	for {
		select {
		case <-e.pare:
			return
		case <-t.C:
			e.mu.Lock()
			_, _ = e.w.Write([]byte(": ping\n\n"))
			e.fl.Flush()
			e.mu.Unlock()
		}
	}
}

// Emitir serializa v como JSON num evento `data:`. Com `tipo` não vazio, inclui a
// linha `event:` (o cliente separa, por exemplo, "evento" de "estado").
func (e *Emissor) Emitir(tipo string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if tipo != "" {
		if _, err := e.w.Write([]byte("event: " + tipo + "\n")); err != nil {
			return err
		}
	}
	if _, err := e.w.Write(append(append([]byte("data: "), b...), '\n', '\n')); err != nil {
		return err
	}
	e.fl.Flush()
	return nil
}

// Fechar para o heartbeat e faz o último flush.
func (e *Emissor) Fechar() {
	e.uma.Do(func() { close(e.pare) })
	e.mu.Lock()
	defer e.mu.Unlock()
	e.fl.Flush()
}
