# Painel Revoada completo: API + MCP + interface web num binário só, numa imagem
# distroless. É a imagem do `docker compose up` da raiz (docker-compose.yml).
#
#   docker build -t revoada-painel .
#
# 1) interface (React/Vite) → web/dist
FROM node:26-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci --no-audit --no-fund
COPY web/ ./
RUN npm run build

# 2) painel Go com a interface embutida (server/internal/webui, go:embed)
FROM golang:1.26-bookworm AS build
WORKDIR /src
COPY core/go.mod core/go.sum ./core/
COPY proto/go.mod proto/go.sum ./proto/
COPY server/go.mod server/go.sum ./server/
WORKDIR /src/server
RUN go mod download
WORKDIR /src
COPY core/ ./core/
COPY proto/ ./proto/
COPY server/ ./server/
COPY --from=web /web/dist/ ./server/internal/webui/dist/
WORKDIR /src/server
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/revoada-painel ./cmd/server
# A chave mestra do cofre fica em /var/lib/revoada: criado aqui com o dono `nonroot`
# para o volume herdar a permissão (a imagem final não tem shell).
RUN mkdir -p /out/dados && chmod 700 /out/dados

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/revoada-painel /revoada-painel
COPY --from=build --chown=65532:65532 /out/dados /var/lib/revoada
ENV REVOADA_DATA_DIR=/var/lib/revoada \
    REVOADA_SERVER_ADDR=:8091 \
    REVOADA_CANAL_ADDR=:7443
EXPOSE 8091 7443
USER nonroot:nonroot
ENTRYPOINT ["/revoada-painel"]
