# Build multi-stage: binário estático + imagem final distroless (sem shell, superfície mínima).
FROM golang:1.26-bookworm AS build
WORKDIR /src

# O gateway é um módulo dentro do monorepo; copiamos só o que ele precisa (o core
# traz a normalização dos spans de IA e a redação de segredos).
COPY core/go.mod core/go.sum ./core/
COPY gateway/go.mod gateway/go.sum* ./gateway/
WORKDIR /src/gateway
RUN go mod download || true

WORKDIR /src
COPY core/ ./core/
COPY gateway/ ./gateway/
COPY deploy/migrations ./deploy/migrations
WORKDIR /src/gateway
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/gateway ./cmd/gateway
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/migrate ./cmd/migrate

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/gateway /gateway
COPY --from=build /out/migrate /migrate
COPY --from=build /src/deploy/migrations /deploy/migrations
EXPOSE 8090
USER nonroot:nonroot
ENTRYPOINT ["/gateway"]
