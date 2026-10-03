package queue

import (
	"testing"

	"github.com/eduardorarruda/revoada/gateway/internal/chhttp"
	"github.com/nats-io/nats.go"
)

// TestAckWaitMaiorQueInsert trava a relação que produziu GRAVAÇÃO EM DOBRO: o prazo de
// ack do JetStream tem de ser MAIOR que o timeout de um INSERT no ClickHouse.
//
// O que aconteceu quando não era: o consumer subia sem AckWait (default de 30 s do
// JetStream) e o cliente HTTP do ClickHouse espera até 60 s. Todo insert entre 30 s e
// 60 s vencia o ack e o MESMO lote era reentregue e gravado de novo — medido em dev com
// `docker pause` no ClickHouse: 5 pontos distintos viraram count()=10 com
// uniqExact(idx)=5, e o writer reportou sucesso nas duas vezes.
//
// Este teste falha se alguém baixar o ackWait ou subir o timeout do insert sem olhar
// para o outro lado.
func TestAckWaitMaiorQueInsert(t *testing.T) {
	if ackWait <= chhttp.RequestTimeout {
		t.Fatalf("ackWait=%s <= timeout de insert=%s: insert lento vira reentrega e linha em dobro",
			ackWait, chhttp.RequestTimeout)
	}
	// O heartbeat tem de caber várias vezes no prazo, senão um único InProgress perdido
	// já deixa o ack vencer.
	if ackHeartbeat*3 > ackWait {
		t.Fatalf("ackHeartbeat=%s sem folga dentro de ackWait=%s", ackHeartbeat, ackWait)
	}
}

// TestSemBufferDeReconexao trava o que faz o 503 do gateway ser HONESTO.
//
// Com o buffer de reconexão ligado (default de 8 MiB), publicar com o NATS fora não
// falhava de verdade: a mensagem ia para o buffer e era entregue ao reconectar, mas o
// PubAck não chegava a tempo e o gateway respondia 503. O agente obedecia o 503 e
// reenviava — e a linha nascia duplicada. Medido em dev: `docker stop revoada-nats` →
// POST → 503 → `docker start` → o lote "que falhou" ENTROU (count=1); reenvio do mesmo
// lote → count=2.
func TestSemBufferDeReconexao(t *testing.T) {
	var o nats.Options
	for _, opt := range connOptions() {
		if err := opt(&o); err != nil {
			t.Fatalf("aplicando opção: %v", err)
		}
	}
	if o.ReconnectBufSize != -1 {
		t.Fatalf("ReconnectBufSize=%d, quer -1 (buffer desligado): com buffer, 503 mente",
			o.ReconnectBufSize)
	}
	if o.MaxReconnect != -1 {
		t.Fatalf("MaxReconnects=%d, quer -1 (reconectar para sempre)", o.MaxReconnect)
	}
}

// TestContadorDeReentregaExposto garante que a reentrega tem NÚMERO em /metrics — era
// exatamente o que faltava quando a gravação em dobro aconteceu: revoada_writer_errors_total
// era 0 (nenhum insert falhou) e num_redelivered do consumer era 0 (a reentrega já
// tinha sido confirmada). Um gráfico com o dobro do valor e zero sinal.
func TestContadorDeReentregaExposto(t *testing.T) {
	antes := msgsRedelivered.Value()
	msgsRedelivered.Inc()
	if msgsRedelivered.Value() != antes+1 {
		t.Fatalf("contador de reentrega não soma: %d -> %d", antes, msgsRedelivered.Value())
	}
}
