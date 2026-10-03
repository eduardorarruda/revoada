# server

Serviço Go de API do **Revoada** (codinome interno *Revoada*; identificadores técnicos
seguem `revoada-*`). Serve toda a API `/api/*` consumida pelo frontend: autenticação, query de
séries (ClickHouse), metadados (PostgreSQL), dashboards, inventário de hosts, alerting/on-call,
notificações, logs, traces, incidentes, sondas de site, jornadas, TV/Kiosk e status page pública.
Também roda os laços de fundo (avaliador de alertas, checador de sites, runner de jornadas,
escalonamento, retenção).

## Endpoints (núcleo)
| Método | Rota | Auth | Descrição |
|---|---|---|---|
| GET | `/healthz` `/readyz` | — | liveness / readiness (ClickHouse + PostgreSQL) |
| POST | `/api/auth/register` | — | cria usuário (1º = admin; demais = viewer; senha ≥ 8) |
| POST | `/api/auth/login` | — | valida senha; retorna access token no header `X-Access-Token` + refresh cookie httpOnly |
| POST | `/api/auth/refresh` | cookie | rotaciona o refresh e emite novo access token |
| POST | `/api/auth/logout` | cookie | revoga a sessão |
| GET | `/api/me` | Bearer | usuário autenticado |
| POST | `/api/query` | Bearer | consulta de séries (seleção adaptativa de tabela) |
| GET | `/api/metrics` | Bearer | lista de métricas |
| GET | `/api/label-values?metric=&label=` | Bearer | valores de um label |

## Demais grupos da API (todos Bearer, salvo indicado)
- **Dashboards:** `GET/POST/PUT/DELETE /api/dashboards[/{uid}]`, `.../versions`, `.../rollback`, `/api/dashboards/starter`.
- **Hosts/inventário:** `GET /api/hosts`, `PATCH /api/hosts/{hostname}` (renomear via `display_name`; o `hostname` continua a chave), `GET /api/events`, `GET /api/health-wall`.
- **Alerting/on-call:** `/api/alert-rules` (+ `/preview`), `/api/alerts` (+ `/{id}/ack`), `/api/silences`, `/api/escalation-policies`, `/api/oncall` (+ `/overrides`).
- **Notificações:** `/api/notify/channels` (+ `/{id}/test`), `/api/notify/routes`, `/api/notify/log`.
- **Logs:** `/api/logs/search`, `/histogram`, `/context`, `/patterns`, `/services`, `/tail` (live), `/metrics` (métrica derivada de busca).
- **Traces:** `/api/traces/search`, `/service-map`, `/exemplars`, `/api/traces/{trace_id}`, `/api/traces/{trace_id}/logs`, `/api/correlate` (correlação métrica→trace→log).
- **Incidentes:** `/api/incidents` (+ `/declare-from-alert`, `/{id}/status|comment|link`, `/{id}/postmortem.md`).
- **Sondas de site:** `/api/site-checks` (+ `/overview`, `/{id}/history`, `/probes`).
- **Sintéticos/jornadas:** `/api/journeys`.
- **TV/Kiosk:** `/api/tv/tokens|playlists|resolve|query|status` (tokens somente-leitura).
- **Descoberta:** `/api/discovery` (+ `/suggestions`, `/apply`).
- **Público (sem login):** `GET /api/status` (status page); `GET /api/live` (WebSocket, auth por query-param).
- **Deploy:** `POST /api/events/deploy` (autenticado por `REVOADA_DEPLOY_TOKEN`).

## Autenticação
- Senhas com **Argon2id**; access token **JWT HS256** (15 min); refresh token opaco
  (7 dias) guardado como sha256 em `sessions`, cookie `httpOnly`+`SameSite=Strict`, rotacionado
  a cada refresh. RBAC: `admin` > `editor` > `viewer`.
- **Rate limit:** 1200 req/min por usuário autenticado; 10 tentativas/min por IP em
  `register`/`login`/`refresh` (anti brute-force).
- **Fail-fast de segredo:** fora de `REVOADA_ENV=development`, o boot **aborta** se
  `REVOADA_JWT_SECRET` estiver vazio/no default inseguro. Em dev, apenas avisa.

## Query API
`POST /api/query {metric, tenant, filters, from, to, step, agg}` escolhe a tabela pela janela/step:
- step ≥ 1h **ou** janela > 90d → `metrics_1h`
- step ≥ 60s **ou** janela > 7d → `metrics_1m`
- caso contrário → `metrics` (bruto)

Resposta colunar: `{metric, table, ts:[epoch...], series:[{labels, values:[...]}]}`.

## Config (env `REVOADA_`)
`REVOADA_SERVER_ADDR` (:8091) · `REVOADA_PG_DSN` · `REVOADA_CH_ADDR/USER/PASSWORD/DB` ·
`REVOADA_JWT_SECRET` (obrigatório fora de dev) · `REVOADA_ENV` (`development` libera o secret default) ·
`REVOADA_SECURE_COOKIES` (cookies de sessão só sobre HTTPS) · `REVOADA_RETENTION_DAYS` (120; TTL de
`site_check_results`/`notification_log`) · `REVOADA_PUBLIC_URL` · `REVOADA_HOST_DASHBOARD_UID` ·
`REVOADA_DEPLOY_TOKEN` · `REVOADA_PG_MAX_CONNS` (20).

## Exemplos
```bash
curl -X POST -d '{"username":"ana","password":"senha12345"}' localhost:8091/api/auth/register
ACCESS=$(curl -sD- -o/dev/null -X POST -d '{"username":"ana","password":"senha12345"}' \
  localhost:8091/api/auth/login | grep -i x-access-token | awk '{print $2}' | tr -d '\r')
curl -H "Authorization: Bearer $ACCESS" localhost:8091/api/me
```
