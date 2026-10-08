# Revoada — Segurança e operação

Os 20 requisitos de segurança e operação do projeto (ARQUITETURA §22): o que cada um cobre,
onde está no código e como configurar. Atualizado a cada etapa.

| # | Requisito | Como o Revoada atende | Onde |
|---|---|---|---|
| 1 | **HTTPS** | Painel com TLS próprio (`REVOADA_TLS_CERT`/`REVOADA_TLS_CHAVE`) ou Let's Encrypt automático (`REVOADA_TLS_DOMINIO`, com :80 só redirecionando); atrás de proxy, `REVOADA_HTTPS_NO_PROXY=1`. HSTS de 1 ano. Canal dos agentes sempre TLS 1.3 + mTLS. Aviso no log se rodar em HTTP puro fora de dev | `cmd/server/main.go` (`servirHTTP`), `internal/blindagem`, `internal/canal` |
| 2 | **Senhas com hash** | Argon2id com sal por senha; comparação em tempo constante; login de usuário inexistente gasta o mesmo tempo (anti-enumeração) | `internal/auth/crypto.go` |
| 3 | **MFA** | TOTP (RFC 6238) com proteção contra reuso do código; 10 códigos de recuperação de uso único (só o hash é guardado); obrigatório para administrador e operador; reautenticação de 5 min para ações críticas | `core/seguranca/totp`, `internal/auth/mfa.go`, tela Segurança da conta |
| 4 | **Rate limit** | Por IP no login/2FA/inscrição; por usuário nas APIs; bloqueio progressivo da conta (5 erros → 1 min, dobrando até 1 h) | `internal/httpapi` (limitadores), `store/seguranca.go` |
| 5 | **Validação de inputs** | JSON estrito (campo desconhecido = erro), limite de corpo de 16 MiB, validadores de fronteira (host:porta, caminho, usuário, listas fechadas), política de senha (12+ e lista de senhas comuns) | `core/validacao`, `internal/entrada`, `core/seguranca/senha` |
| 6 | **Sanitização de dados** | Texto livre sem caracteres de controle/invisíveis; segredos redigidos na auditoria e nos logs; a interface (React) escapa toda saída; IDs de requisição externos só se tiverem formato válido | `core/validacao`, `internal/audit`, `internal/blindagem` |
| 7 | **SQL Injection** | Valores sempre parametrizados; nomes de tabela/coluna só se existirem no schema capturado e citados pelo motor; filtros estruturados (nunca SQL livre); transformações de lista fechada | `core/ident`, `core/migracao/modelo` |
| 8 | **Migrations** | Versionadas (`NNNN_nome.up.sql`), aplicadas em ordem no boot, cada uma numa transação, com trava entre painéis e checksum (arquivo alterado depois de aplicado é recusado) | `internal/store/migracoes`, `store/migrar.go` |
| 9 | **Rollback** | Todo `up` tem `down`; `revoada-painel migracoes desfazer <versão>`; rollback de migração de dados (ARQUITETURA §9.4) e de deploy (§10); releases versionadas permitem voltar o binário | `store/migrar.go`, `cmd/server` |
| 10 | **Controle de acesso** | Papéis administrador/operador/leitor com matriz verificada no back-end em cada rota; escopo por servidor; agente só executa tarefa assinada e de tipo liberado localmente | `internal/auth/papeis.go`, `internal/authz`, `agent/internal/canal` |
| 11 | **Expiração de sessão** | Access token de 15 min; refresh de uso único com inatividade de 24 h (`REVOADA_SESSAO_INATIVIDADE_HORAS`) e limite absoluto de 7 dias (`REVOADA_SESSAO_MAX_HORAS`); "sair de todos os dispositivos"; desativar usuário derruba as sessões | `internal/auth/handlers.go`, migração 0005 |
| 12 | **Secrets** | Nada em código; `REVOADA_JWT_SECRET` forte exigido fora de dev; segredos de banco e 2FA no cofre em envelope; chave mestra fora do banco (`REVOADA_DATA_DIR`); gitleaks no CI | `core/seguranca/cofre`, `internal/segredos`, `.github/workflows/ci.yml` |
| 13 | **CORS** | Só a origem do painel e as de `REVOADA_CORS_ORIGENS`; preflight de origem estranha recusado; escrita (POST/PUT/PATCH/DELETE) de origem estranha recusada (defesa contra CSRF) | `internal/blindagem` |
| 14 | **Logs** | JSON estruturado (`slog`) no painel e no agente; `X-Request-ID` em toda requisição; `correlacao_id` liga tarefa, painel e agente; query string fora do log (tokens); auditoria encadeada por hash (`GET /api/audit/integridade`) | `internal/blindagem`, `internal/audit`, `store/audit.go` |
| 15 | **Backups** | Diário de ClickHouse + PostgreSQL para MinIO com retenção (14 diários + 3 mensais) e ensaio de restauração; **chave mestra em backup separado e cifrado** | `deploy/backup/backup.sh`, `restore-drill.sh`, `backup-chave.sh` |
| 16 | **Criptografia** | Em repouso: AES-256-GCM em envelope (DEK por segredo, KEK fora do banco, rotação sem decifrar). Credencial por tarefa: X25519 + XChaCha20-Poly1305. Em trânsito: TLS 1.2+ (painel) / 1.3 + mTLS (agentes). Tarefas assinadas com Ed25519 | `core/seguranca/*`, `internal/canal` |
| 17 | **Dependências** | `govulncheck` e `npm audit` no CI; Dependabot semanal (Go, npm, Actions, Docker) | `.github/workflows/ci.yml`, `.github/dependabot.yml` |
| 18 | **PMP — Princípio do Menor Privilégio** | Painel em imagem distroless como usuário sem privilégio; agente com usuário próprio e sandbox do systemd; lista local de tarefas permitidas (vazia por padrão); papel leitor por padrão para novos usuários; recomendação de usuário somente leitura no banco de origem; IA/MCP sem acesso a credenciais nem a dados | `server/Dockerfile`, `deploy/agent/*.service`, `agent/internal/canal`, `internal/auth` |
| 19 | **Monitoramento** | O Revoada monitora a si mesmo: `/metrics` (Prometheus, com `REVOADA_METRICAS_TOKEN`), `/healthz` e `/readyz`; presença dos agentes com alerta quando caem; alertas e on-call herdados | `internal/blindagem`, `internal/canal`, `internal/alerting` |
| 20 | **Plano de recuperação** | Runbook de desastre com backup, restauração, ordem de subida e o que fazer se a chave mestra se perder | `docs/runbook-dr.md` |

## Variáveis de segurança

| Variável | Padrão | Para quê |
|---|---|---|
| `REVOADA_JWT_SECRET` | — (obrigatória fora de dev) | assina os tokens de sessão |
| `REVOADA_DATA_DIR` | diretório de configuração do SO | onde mora a chave mestra do cofre |
| `REVOADA_TLS_CERT` / `REVOADA_TLS_CHAVE` | — | HTTPS com certificado próprio |
| `REVOADA_TLS_DOMINIO` | — | HTTPS automático (Let's Encrypt) |
| `REVOADA_HTTPS_NO_PROXY` | `0` | o HTTPS está num proxy na frente |
| `REVOADA_CORS_ORIGENS` | — | outras origens autorizadas |
| `REVOADA_SESSAO_INATIVIDADE_HORAS` | `24` | sessão expira sem uso |
| `REVOADA_SESSAO_MAX_HORAS` | `168` | limite absoluto da sessão |
| `REVOADA_METRICAS_TOKEN` | — | protege `/metrics` |
| `REVOADA_CANAL_ADDR` / `REVOADA_CANAL_HOSTS` | `:7443` / `localhost,127.0.0.1` | canal dos agentes |

## Chave mestra

Fica em `REVOADA_DATA_DIR/chave-mestra.json` (permissão 0600). Faça backup com
`deploy/backup/backup-chave.sh` e guarde **longe** do backup do banco. Sem ela, os
segredos guardados no banco ficam ilegíveis — o painel continua funcionando, mas é
preciso recadastrar credenciais de bancos e reativar o 2FA dos usuários.

## Revisões e achados

| Data | Revisão | Achado | Severidade | Correção |
|---|---|---|---|---|
| 2026-10-01 | Segurança das Etapas 5–6 | A trilha de auditoria só redigia nomes de campo em inglês: `senha` (conexão de banco e reautenticação) e `codigo` (TOTP) ficavam em texto puro na trilha encadeada | **Crítica** | Lista de redação ganhou os nomes em português e uma regra por trecho do nome (`senha`, `password`, `secret`, `segredo`, `token`, `privad`); teste com os corpos reais das duas rotas (`audit_test.go`). As linhas afetadas só existiam no banco de teste local, com valores de teste. Em instalação real anterior a esta correção: rotacione as senhas das conexões e peça troca de senha a quem reautenticou sem 2FA |
| 2026-10-01 | Segurança das Etapas 5–6 | `GET …/checkpoints` mostrava `ultima_chave` (valor de chave da origem, pode ser CPF/e-mail) a qualquer papel; rotas de leitura da migração usavam só "autenticado" | Média | A última chave só aparece para quem pode executar migração; todas as rotas de leitura da migração passam por `auth.Exige(PermVer)` |
| 2026-10-01 | Go (correção) das Etapas 5–6 | Falso bloqueio de FK com pai de PK composta; checksum que falhava quando a coluna de conferência recebia DEFAULT; nomes de staging que podiam colidir em tabelas de nome longo; troca na ordem das FKs da origem, não do destino | Média | Corrigidos, com teste de integração FB 2.5 → PG 16 |
| 2026-10-01 | Segurança das Etapas 7–10 | MCP: a chave da linha citada em mensagens de erro era cortada só até o primeiro espaço ("Maria da Silva" deixava "da Silva") — vazava dado de linha para token de escopo leitura | Alta | Tudo depois de "linha de chave" sai da mensagem; teste com chave com espaço e dois-pontos |
| 2026-10-01 | Segurança das Etapas 7–10 | MCP: relatório de simulação em formato inesperado voltava sem redação | Média | Formato inesperado → o resumo sai vazio (falha segura) |
| 2026-10-01 | Segurança das Etapas 7–10 | Token de deploy é único e global: `X-Revoada-Repositorio` só rotula, não autoriza | Média | **Aberto.** Até haver tokens por repositório/aplicação, não compartilhe o `REVOADA_DEPLOY_TOKEN` entre repositórios de confiança diferente; o agente só implanta o que está em `canal.deploy` |
| 2026-10-01 | Segurança das Etapas 7–10 | Freio do MCP (120/min por token) é por processo | Baixa | **Aceito na v1** (painel de instância única); vira contador compartilhado se o painel escalar horizontalmente |
| 2026-10-08 | Segurança dos agentes de IA (ADR 008) | Spans de busca, embedding e reordenação do OpenInference (`retrieval.documents.*`, `reranker.*`, `embedding.embeddings.*`) deixavam o texto dos documentos em `spans.labels` mesmo com o conteúdo desligado; e a limpeza só rodava em span reconhecido como IA | **Crítica** | Chaves acrescentadas; a limpeza de conteúdo roda em todo span; span só com `gen_ai.prompt.N` passa a ser reconhecido; teste com fixture real e com span não reconhecido. Corrigido antes de publicar |
| 2026-10-08 | Segurança dos agentes de IA | `exception.message`/`stacktrace` de span de IA (provedor costuma ecoar trecho do pedido no erro 4xx) iam para os rótulos sem redação | Alta | Passam por `core/redacao` em span de IA |
| 2026-10-08 | Segurança dos agentes de IA | Ler o conteúdo das conversas (dado pessoal) não pedia reautenticação | Média | `PermVerConteudoIA` virou permissão crítica (2FA + reautenticação de 5 min), como ver credencial |
| 2026-10-08 | Segurança dos agentes de IA | Token MCP com escopo `ia_conteudo` lê as conversas de **todos** os servidores: tokens MCP não têm escopo por servidor | Média | **Aceito na v1, com cautela.** É o primeiro escopo MCP que devolve texto de usuário; conceda só a agentes de confiança, com validade curta, e revogue quando não precisar. Escopo por servidor nos tokens MCP fica como próximo passo |
| 2026-10-08 | Segurança dos agentes de IA | Fixture de teste com chave falsa no formato `sk-proj-…` (dispararia o gitleaks/push protection) | Baixa | Trocada por `sk-teste-falso-…` nas fixtures e scripts |
