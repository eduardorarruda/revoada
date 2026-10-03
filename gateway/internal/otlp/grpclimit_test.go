package otlp

import (
	"context"
	"net"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// limiterFake registra o que o interceptor perguntou e responde o que o teste mandar.
type limiterFake struct {
	allow    bool
	gotAddr  string
	gotXFF   string
	chamadas int
}

func (l *limiterFake) Allow(remoteAddr, xff string) bool {
	l.chamadas++
	l.gotAddr, l.gotXFF = remoteAddr, xff
	return l.allow
}

type gateFake struct {
	allow  bool
	dentro int
	saiu   int
}

func (g *gateFake) Enter() bool {
	if !g.allow {
		return false
	}
	g.dentro++
	return true
}
func (g *gateFake) Leave() { g.saiu++ }

func ctxComPeer(ip string, xff ...string) context.Context {
	ctx := peer.NewContext(context.Background(),
		&peer.Peer{Addr: &net.TCPAddr{IP: net.ParseIP(ip), Port: 5555}})
	if len(xff) > 0 {
		ctx = metadata.NewIncomingContext(ctx,
			metadata.Pairs("x-forwarded-for", xff[0]))
	}
	return ctx
}

// TestGRPCRateLimitRecusa: o listener OTLP/gRPC subia com grpc.NewServer() PURO — sem
// interceptor nenhum — enquanto o rl.Wrap cobria só as rotas HTTP. Medido na mesma
// máquina, mesma carga e mesma chave inválida: HTTP recusou 3.590 de 6.000 com 429;
// gRPC deixou passar as 6.000, com delta ZERO no contador de recusa. Agora o mesmo
// limitador (mesmo balde) decide nos dois transportes.
func TestGRPCRateLimitRecusa(t *testing.T) {
	rl := &limiterFake{allow: false}
	itc := rateLimitUnary(rl, nil)
	chamouHandler := false
	h := func(context.Context, any) (any, error) { chamouHandler = true; return "ok", nil }

	_, err := itc(ctxComPeer("198.51.100.7"), nil, &grpc.UnaryServerInfo{}, h)
	if err == nil {
		t.Fatal("requisição acima do limite passou pelo gRPC (era o buraco)")
	}
	if got := status.Code(err); got != codes.ResourceExhausted {
		t.Fatalf("código=%s, quer ResourceExhausted (é o que faz o SDK recuar)", got)
	}
	if chamouHandler {
		t.Fatal("o handler foi executado apesar da recusa")
	}
	if rl.gotAddr != "198.51.100.7:5555" {
		t.Fatalf("peer visto pelo limitador=%q, quer 198.51.100.7:5555", rl.gotAddr)
	}
}

// TestGRPCRateLimitPassa: dentro do limite, o handler roda normalmente e o
// X-Forwarded-For do metadata chega ao limitador (mesmo critério do HTTP).
func TestGRPCRateLimitPassa(t *testing.T) {
	rl := &limiterFake{allow: true}
	itc := rateLimitUnary(rl, nil)
	resp, err := itc(ctxComPeer("10.0.0.5", "9.9.9.9"), nil, &grpc.UnaryServerInfo{},
		func(context.Context, any) (any, error) { return "ok", nil })
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if resp != "ok" {
		t.Fatalf("resposta=%v, quer ok", resp)
	}
	if rl.gotXFF != "9.9.9.9" {
		t.Fatalf("XFF visto pelo limitador=%q, quer 9.9.9.9", rl.gotXFF)
	}
}

// TestGRPCTetoGlobal: o teto GLOBAL de requisições em voo também vale no gRPC —
// um teto por transporte é meio teto. Cheio => Unavailable (o "503" do gRPC).
func TestGRPCTetoGlobal(t *testing.T) {
	gate := &gateFake{allow: false}
	itc := rateLimitUnary(&limiterFake{allow: true}, gate)
	_, err := itc(ctxComPeer("10.0.0.5"), nil, &grpc.UnaryServerInfo{},
		func(context.Context, any) (any, error) { return "ok", nil })
	if got := status.Code(err); got != codes.Unavailable {
		t.Fatalf("código=%s, quer Unavailable", got)
	}

	gate.allow = true
	if _, err := itc(ctxComPeer("10.0.0.5"), nil, &grpc.UnaryServerInfo{},
		func(context.Context, any) (any, error) { return "ok", nil }); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if gate.dentro != 1 || gate.saiu != 1 {
		t.Fatalf("vaga não foi devolvida: dentro=%d saiu=%d", gate.dentro, gate.saiu)
	}
}

// streamFake é o mínimo de grpc.ServerStream que o interceptor de stream usa.
type streamFake struct {
	grpc.ServerStream
	ctx context.Context
}

func (s streamFake) Context() context.Context { return s.ctx }

// TestGRPCRateLimitStream: um serviço em stream registrado amanhã neste listener não
// pode reabrir o buraco em silêncio.
func TestGRPCRateLimitStream(t *testing.T) {
	itc := rateLimitStream(&limiterFake{allow: false}, nil)
	err := itc(nil, streamFake{ctx: ctxComPeer("198.51.100.9")}, &grpc.StreamServerInfo{},
		func(any, grpc.ServerStream) error { return nil })
	if got := status.Code(err); got != codes.ResourceExhausted {
		t.Fatalf("stream: código=%s, quer ResourceExhausted", got)
	}
}

// TestGRPCSemLimitadorNaoQuebra: rl/gate nil (testes) não podem derrubar o caminho.
func TestGRPCSemLimitadorNaoQuebra(t *testing.T) {
	itc := rateLimitUnary(nil, nil)
	if _, err := itc(context.Background(), nil, &grpc.UnaryServerInfo{},
		func(context.Context, any) (any, error) { return "ok", nil }); err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
}
