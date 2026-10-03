# Revoada — atalhos de desenvolvimento.
# Requer Go no PATH e o stack de dados de pé (make dev-up).

COMPOSE := docker compose -f deploy/docker-compose.dev.yml
AGENT_PKG := ./cmd/revoada-agent

.PHONY: help dev-up dev-down migrate build test vet agent agent-linux agent-windows agent-winres

help:
	@echo "Alvos: dev-up dev-down migrate build test vet agent agent-linux agent-windows"

dev-up:            ## sobe o stack de dados (ClickHouse, Postgres, Redis, NATS)
	$(COMPOSE) up -d clickhouse postgres redis nats

dev-down:          ## derruba o stack (mantém volumes)
	$(COMPOSE) down

migrate:           ## aplica as migrations do ClickHouse
	cd gateway && go run ./cmd/migrate -dir ../deploy/migrations/clickhouse

build:             ## compila os binários do gateway
	cd gateway && go build ./...

vet:               ## go vet
	cd gateway && go vet ./...

test:              ## testes unitários
	cd gateway && go test ./...

agent: agent-linux agent-windows agent-macos  ## compila o agente (Linux/Windows/macOS) e publica os instaladores em dist/
	# O painel monta o instalador com a chave embutida a partir DESTE diretório
	# (REVOADA_AGENT_DIST_DIR); sem os scripts aqui, só o Windows funcionaria.
	cp deploy/agent/install.sh deploy/agent/install-macos.sh dist/

agent-linux:       ## compila revoada-agent para linux/amd64 em dist/
	mkdir -p dist
	cd agent && CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o ../dist/revoada-agent $(AGENT_PKG)

agent-winres:      ## (re)gera o recurso de ícone/versão do .exe (requer go-winres no PATH)
	cd agent/cmd/revoada-agent && go-winres make --in winres/winres.json --arch amd64 --out rsrc

agent-windows:     ## compila revoada-agent.exe (windows/amd64) com ícone Revoada embutido
	mkdir -p dist
	cd agent && CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o ../dist/revoada-agent.exe $(AGENT_PKG)

agent-macos:       ## compila os dois binários do macOS (Apple Silicon e Intel) em dist/
	mkdir -p dist
	cd agent && CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o ../dist/revoada-agent-darwin-arm64 $(AGENT_PKG)
	cd agent && CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o ../dist/revoada-agent-darwin-amd64 $(AGENT_PKG)

backup:            ## backup ClickHouse + PostgreSQL para o MinIO
	bash deploy/backup/backup.sh

restore-drill:     ## restaura o backup mais recente em ambiente isolado e valida contagens
	bash deploy/backup/restore-drill.sh

painel-web:        ## compila a interface e a copia para dentro do painel (go:embed)
	cd web && npm ci && npm run build
	find server/internal/webui/dist -mindepth 1 ! -name index.html -delete
	cp -r web/dist/. server/internal/webui/dist/

painel: painel-web ## binário ÚNICO do painel (API + MCP + interface) em dist/
	cd server && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o ../dist/revoada-painel ./cmd/server

desktop:           ## app desktop (Wails; Linux precisa de libgtk-3-dev e libwebkit2gtk-4.1-dev)
	cd desktop && GOWORK=off CGO_ENABLED=1 go build -tags desktop,production,webkit2_41 -trimpath -ldflags "-s -w" -o ../dist/revoada-desktop .
