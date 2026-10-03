package otlp

import (
	"context"
	"strings"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

// Limiter é o freio de ingestão visto por este pacote. É a MESMA implementação que
// embrulha as rotas HTTP (server.RateLimiter) — a interface existe só para não
// inverter a dependência entre os pacotes.
type Limiter interface {
	// Allow decide pelo IP de origem e CONTA a recusa em /metrics.
	Allow(remoteAddr, xff string) bool
}

// Gate é o teto GLOBAL de requisições em voo (server.InFlight). O mesmo objeto do
// caminho HTTP: um teto por transporte é meio teto.
type Gate interface {
	Enter() bool
	Leave()
}

// POR QUE este arquivo existe (medido nesta máquina, mesma carga, mesma chave inválida):
//
// O rate limit cobria só o listener HTTP (rl.Wrap nas rotas /v1/*). O listener
// OTLP/gRPC de :4317 — que recebe MÉTRICAS, LOGS e TRACES, os mesmos três sinais do
// HTTP — subia com grpc.NewServer() puro, sem interceptor nenhum. Resultado da medição:
//
//	HTTP  6.000 requisições → 2.410 responderam 401 e 3.590 responderam 429
//	                          (revoada_ingest_rate_limited_total +3.590)
//	gRPC  6.000 requisições → 6.000 passaram, ZERO recusas, 10.374 req/s
//	                          (revoada_ingest_rate_limited_total +0)
//
// Cada uma dessas 6.000 chegou até a autenticação, isto é, até o PostgreSQL — o mesmo
// banco onde moram as sessões do painel e o cofre de credenciais SSH. O freio do HTTP
// não era um freio: era um freio de UMA PORTA, e a outra estava escancarada.
//
// O interceptor usa o MESMO limitador (mesmo balde, mesmo critério de IP, mesmas redes
// de proxy confiáveis) do caminho HTTP: dois baldes separados dariam ao gRPC uma cota
// extra, que é meia porta aberta.

// grpcPeer devolve o endereço do peer direto e o X-Forwarded-For (quando houver) de
// um contexto gRPC, no mesmo formato que o caminho HTTP entrega ao limitador.
func grpcPeer(ctx context.Context) (remoteAddr, xff string) {
	if p, ok := peer.FromContext(ctx); ok && p.Addr != nil {
		remoteAddr = p.Addr.String()
	}
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get("x-forwarded-for"); len(v) > 0 {
			xff = strings.Join(v, ",")
		}
	}
	return remoteAddr, xff
}

// errRateLimited é a recusa do gRPC equivalente ao 429 do HTTP. ResourceExhausted é o
// código que o contrato do OTLP manda usar em throttling — e é o que faz os SDKs
// (e o nosso agente) recuarem em vez de repetirem o mesmo lote em rajada.
func errRateLimited() error {
	return status.Error(codes.ResourceExhausted, "rate limit excedido, retente")
}

// errOverloaded é a recusa por teto GLOBAL de requisições em voo (503 do HTTP).
// Unavailable é o código que o OTLP trata como "tente de novo mais tarde".
func errOverloaded() error {
	return status.Error(codes.Unavailable, "gateway sobrecarregado, retente")
}

// rateLimitUnary é o interceptor unário. Todos os Export do OTLP (métricas, logs,
// traces) são unários — este é o caminho que a medição acima atravessou.
func rateLimitUnary(rl Limiter, gate Gate) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, _ *grpc.UnaryServerInfo, h grpc.UnaryHandler) (any, error) {
		if rl != nil {
			addr, xff := grpcPeer(ctx)
			if !rl.Allow(addr, xff) {
				return nil, errRateLimited()
			}
		}
		if gate != nil {
			if !gate.Enter() {
				return nil, errOverloaded()
			}
			defer gate.Leave()
		}
		return h(ctx, req)
	}
}

// rateLimitStream cobre um eventual serviço em stream registrado neste listener.
// Hoje não há nenhum; o interceptor entra junto para que registrar um amanhã não
// reabra, em silêncio, exatamente o buraco que este arquivo fecha.
func rateLimitStream(rl Limiter, gate Gate) grpc.StreamServerInterceptor {
	return func(srv any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		if rl != nil {
			addr, xff := grpcPeer(ss.Context())
			if !rl.Allow(addr, xff) {
				return errRateLimited()
			}
		}
		if gate != nil {
			if !gate.Enter() {
				return errOverloaded()
			}
			defer gate.Leave()
		}
		return h(srv, ss)
	}
}
