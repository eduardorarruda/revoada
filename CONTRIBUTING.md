# Como contribuir

Obrigado por querer melhorar o Revoada! Correções, testes, documentação e ideias são bem-vindos.

## Antes de começar

- Para mudanças grandes, abra uma *issue* primeiro e descreva o problema e a proposta.
- Defeitos de segurança **não** vão em issue pública: veja [SECURITY.md](SECURITY.md).
- O projeto é escrito em **português do Brasil**: código, comentários, mensagens e interface.

## Ambiente de desenvolvimento

Pré-requisitos: Docker + Compose, Go 1.25 e Node 22+.

```bash
make dev-up                                   # PostgreSQL, ClickHouse, NATS, Redis
make migrate                                  # tabelas de telemetria no ClickHouse
cd server && REVOADA_ENV=development go run ./cmd/server   # painel em :8091
cd gateway && go run ./cmd/gateway            # ingestão em :8090
cd web && npm install && npm run dev          # interface em :5173 (proxy para :8091)
```

Para coletar métricas da sua própria máquina, gere uma chave em **Infraestrutura → Adicionar
servidor** e rode o agente (`cd agent && go run ./cmd/revoada-agent -config agent.yaml`).

## Testes

```bash
go test ./...                                # em cada módulo: core, proto, gateway, server, agent
cd web && npx tsc -b && npx vitest run       # interface
```

Os testes de integração da migração usam Firebird e PostgreSQL de verdade (Docker) e rodam em
série — veja o cabeçalho de `agent/internal/migracao/copia/copia_integracao_test.go`. A bateria
`tests/validacao/` confere um stack rodando contra o kernel da máquina (CPU, RAM, disco, logs,
checagens de site); veja o [README dela](tests/validacao/README.md).

## Padrões

- **Teste primeiro** para correções: um teste que falha sem a correção.
- Funções pequenas, erros tratados explicitamente, nada de segredo no código.
- `gofmt`/`go vet` limpos; `eslint` limpo no que você tocou.
- Commits no formato `tipo: descrição` (`feat`, `fix`, `docs`, `test`, `refactor`, `chore`, `ci`).
- Um PR por assunto, com a descrição do *porquê* e de como foi testado.

## Licença das contribuições

Ao enviar um PR você concorda que a contribuição seja distribuída sob a
[AGPL-3.0](LICENSE), a mesma licença do projeto.
