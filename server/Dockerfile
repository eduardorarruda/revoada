# Build multi-stage: binário estático + imagem distroless.
FROM golang:1.27-bookworm AS build
WORKDIR /src
# O painel depende dos módulos `core` e `proto` via replace ../core e ../proto —
# os dois precisam estar no contexto da build.
COPY core/go.mod core/go.sum ./core/
COPY proto/go.mod proto/go.sum ./proto/
COPY server/go.mod server/go.sum ./server/
WORKDIR /src/server
RUN go mod download
WORKDIR /src
COPY core/ ./core/
COPY proto/ ./proto/
COPY server/ ./server/
WORKDIR /src/server
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/server ./cmd/server
# Ferramenta administrativa que roda por `docker exec`, não pelo ENTRYPOINT. Precisa
# estar DENTRO da imagem: ela é distroless (sem shell, sem Go, sem gerenciador de
# pacotes), então não há como instalá-la depois num container em pé. Custo: ~8 MB.
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/apagar-historico ./cmd/apagar-historico
# Diretório de dados (chave mestra do cofre). Criado aqui com o dono `nonroot` para
# que o volume nomeado herde a permissão — a imagem final não tem shell para mkdir.
RUN mkdir -p /out/dados && chmod 700 /out/dados

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/server /server
# Uso: docker exec revoada-server /apagar-historico
# (sem flags é dry-run; ver o cabeçalho de server/cmd/apagar-historico/main.go)
COPY --from=build /out/apagar-historico /apagar-historico
COPY --from=build --chown=65532:65532 /out/dados /var/lib/revoada
ENV REVOADA_DATA_DIR=/var/lib/revoada
EXPOSE 8091
USER nonroot:nonroot
ENTRYPOINT ["/server"]
