package otlp

import (
	"context"
	"net/http"

	cpb "go.opentelemetry.io/proto/otlp/collector/metrics/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type grpcServer struct {
	cpb.UnimplementedMetricsServiceServer
	rc *Receiver
}

// registerMetricsService registra o MetricsService no servidor gRPC.
func registerMetricsService(srv *grpc.Server, rc *Receiver) {
	cpb.RegisterMetricsServiceServer(srv, &grpcServer{rc: rc})
}

// Export implementa o MetricsService do OTLP/gRPC. A chave vem no metadata x-revoada-key.
func (g *grpcServer) Export(ctx context.Context, req *cpb.ExportMetricsServiceRequest) (*cpb.ExportMetricsServiceResponse, error) {
	key := ""
	if md, ok := metadata.FromIncomingContext(ctx); ok {
		if v := md.Get("x-revoada-key"); len(v) > 0 {
			key = v[0]
		}
	}
	tenant, bound, httpStatus, ok := g.rc.authAgent(ctx, key)
	if !ok {
		switch httpStatus {
		case 401:
			return nil, status.Error(codes.Unauthenticated, "chave desconhecida")
		case 403:
			return nil, status.Error(codes.PermissionDenied, "chave revogada")
		default:
			return nil, status.Error(codes.Internal, "erro de autenticação")
		}
	}
	out, err := g.rc.process(ctx, tenant, bound, key, req)
	switch out.status {
	case http.StatusServiceUnavailable:
		return nil, status.Error(codes.Unavailable, err.Error())
	case http.StatusRequestEntityTooLarge:
		// gRPC não tem "413": ResourceExhausted é o código do OTLP para "lote grande
		// demais / recurso estourado", e é o que faz o SDK reduzir o lote em vez de
		// repetir o mesmo payload para sempre.
		return nil, status.Error(codes.ResourceExhausted, out.message)
	case http.StatusTooManyRequests:
		return nil, status.Error(codes.ResourceExhausted, out.message)
	}
	// Recusa parcial (relógio fora de faixa, teto de labels) viaja no partial_success,
	// como manda o contrato OTLP — o emissor vê quantas linhas caíram e por quê.
	resp := &cpb.ExportMetricsServiceResponse{}
	if out.rejected > 0 {
		resp.PartialSuccess = &cpb.ExportMetricsPartialSuccess{
			RejectedDataPoints: out.rejected,
			ErrorMessage:       out.message,
		}
	}
	return resp, nil
}

// ServeGRPC foi REMOVIDA de propósito: era um segundo caminho para subir o listener
// OTLP/gRPC com grpc.NewServer() puro, sem o interceptor de rate limit. Ninguém a
// chamava (o main usa ServeGRPCAll), mas ela era exatamente a armadilha que deixou
// :4317 sem freio — bastava alguém preferir "a versão simples". Só existe
// ServeGRPCAll, e ela exige o limitador na assinatura.
