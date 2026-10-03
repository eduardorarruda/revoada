-- Metadados de autenticação do server (idempotente).
CREATE TABLE IF NOT EXISTS users (
    id                  BIGSERIAL PRIMARY KEY,
    username            TEXT UNIQUE NOT NULL,
    password_hash       TEXT NOT NULL DEFAULT '',   -- Argon2id encoded ('' = sem senha local)
    role                TEXT NOT NULL DEFAULT 'viewer',  -- admin | editor | viewer
    must_reset_password BOOLEAN NOT NULL DEFAULT FALSE,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS sessions (
    id          BIGSERIAL PRIMARY KEY,
    user_id     BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash  TEXT UNIQUE NOT NULL,   -- sha256 do refresh token
    expires_at  TIMESTAMPTZ NOT NULL,
    revoked     BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS sessions_user_idx ON sessions (user_id);

-- Dashboards: modelo JSON versionado.
CREATE TABLE IF NOT EXISTS dashboards (
    id         BIGSERIAL PRIMARY KEY,
    uid        TEXT UNIQUE NOT NULL,
    title      TEXT NOT NULL,
    folder     TEXT NOT NULL DEFAULT 'Geral',
    model      JSONB NOT NULL,
    version    INT NOT NULL DEFAULT 1,
    updated_by TEXT,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS dashboards_folder_idx ON dashboards (folder);

CREATE TABLE IF NOT EXISTS dashboard_versions (
    id           BIGSERIAL PRIMARY KEY,
    dashboard_id BIGINT NOT NULL REFERENCES dashboards(id) ON DELETE CASCADE,
    version      INT NOT NULL,
    model        JSONB NOT NULL,
    created_by   TEXT,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (dashboard_id, version)
);

-- Playlists de TV: sequência de dashboards com duração por tela.
CREATE TABLE IF NOT EXISTS playlists (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    items      JSONB NOT NULL DEFAULT '[]',  -- [{dashboard_uid, duration_seconds}]
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Tokens de TV (modo kiosk): acesso somente-leitura a um dashboard OU playlist, só pelo token.
CREATE TABLE IF NOT EXISTS tv_tokens (
    id             BIGSERIAL PRIMARY KEY,
    token_hash     TEXT UNIQUE NOT NULL,     -- sha256 do token
    name           TEXT NOT NULL,
    location       TEXT,
    dashboard_uid  TEXT NOT NULL DEFAULT '',
    playlist_id    BIGINT,                   -- se preenchido, roda a playlist
    revoked        BOOLEAN NOT NULL DEFAULT FALSE,
    last_seen      TIMESTAMPTZ,
    bundle_version TEXT,
    reload_at      TIMESTAMPTZ,              -- watchdog: TV recarrega se reload_at > carga atual
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Colunas adicionadas depois (tabela pode já existir sem elas).
ALTER TABLE tv_tokens ADD COLUMN IF NOT EXISTS playlist_id BIGINT;
ALTER TABLE tv_tokens ADD COLUMN IF NOT EXISTS bundle_version TEXT;
ALTER TABLE tv_tokens ADD COLUMN IF NOT EXISTS reload_at TIMESTAMPTZ;
-- Valor em claro do token: permite recopiar o link da TV a qualquer momento (a
-- listagem é só de admin). Tokens antigos ficam NULL até serem regenerados.
ALTER TABLE tv_tokens ADD COLUMN IF NOT EXISTS token TEXT;

-- Alerting (Fase 4): regras e histórico de disparos.
CREATE TABLE IF NOT EXISTS alert_rules (
    id             BIGSERIAL PRIMARY KEY,
    name           TEXT NOT NULL,
    metric         TEXT NOT NULL,
    filters        JSONB NOT NULL DEFAULT '{}',
    agg            TEXT NOT NULL DEFAULT 'avg',
    condition_op   TEXT NOT NULL DEFAULT '>',       -- > < >= <=
    threshold      DOUBLE PRECISION NOT NULL,
    window_seconds INT NOT NULL DEFAULT 300,         -- janela de avaliação
    for_seconds    INT NOT NULL DEFAULT 0,           -- persistência antes de disparar (0 = imediato)
    severity       TEXT NOT NULL DEFAULT 'warning',  -- info | warning | critical
    runbook        TEXT,
    enabled        BOOLEAN NOT NULL DEFAULT TRUE,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS alert_events (
    id          BIGSERIAL PRIMARY KEY,
    rule_id     BIGINT NOT NULL REFERENCES alert_rules(id) ON DELETE CASCADE,
    fingerprint TEXT NOT NULL,                       -- rule_id + labels
    labels      JSONB NOT NULL DEFAULT '{}',
    state       TEXT NOT NULL,                       -- firing | resolved
    value       DOUBLE PRECISION,
    severity    TEXT NOT NULL,
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ended_at    TIMESTAMPTZ,
    acked_by    TEXT,
    acked_at    TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS alert_events_active_idx ON alert_events (fingerprint) WHERE ended_at IS NULL;

-- Notificações (P4.2): contact points, roteamento e auditoria de envios.
CREATE TABLE IF NOT EXISTS notification_channels (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    type       TEXT NOT NULL,                       -- smtp | webhook | telegram | whatsapp
    config     JSONB NOT NULL DEFAULT '{}',         -- segredos do canal (host/token/etc.)
    enabled    BOOLEAN NOT NULL DEFAULT TRUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Configurações globais da aplicação (chave→JSON). Hoje guarda a integração única
-- do WhatsApp (Evolution API), reusada por todos os canais desse tipo.
CREATE TABLE IF NOT EXISTS app_settings (
    key        TEXT PRIMARY KEY,
    value      JSONB NOT NULL DEFAULT '{}',
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Árvore de rotas: avaliadas por prioridade; a primeira que casa envia. Se `continue`
-- for TRUE, segue avaliando as próximas (permite fan-out). Sem match → rota fallback.
CREATE TABLE IF NOT EXISTS alert_routes (
    id                     BIGSERIAL PRIMARY KEY,
    name                   TEXT NOT NULL,
    min_severity           TEXT NOT NULL DEFAULT 'info',   -- info < warning < critical
    matchers               JSONB NOT NULL DEFAULT '{}',    -- {label: valor} exatos que precisam casar
    channel_ids            JSONB NOT NULL DEFAULT '[]',    -- [id,...]
    group_wait_seconds     INT NOT NULL DEFAULT 30,        -- espera juntando alertas do grupo
    continue_matching      BOOLEAN NOT NULL DEFAULT FALSE,
    priority               INT NOT NULL DEFAULT 100,       -- menor = avaliado antes
    enabled                BOOLEAN NOT NULL DEFAULT TRUE,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS notification_log (
    id           BIGSERIAL PRIMARY KEY,
    channel_id   BIGINT,
    channel_type TEXT NOT NULL,
    route_name   TEXT,
    subject      TEXT NOT NULL,
    alert_count  INT NOT NULL DEFAULT 1,
    -- sent      = o provedor confirmou a entrega pelo endereçamento que chega
    -- unverified = aceitou, mas ninguém confirma que chegou (WhatsApp sem LID)
    -- error     = recusado
    status       TEXT NOT NULL,
    detail       TEXT,
    sent_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS notification_log_sent_idx ON notification_log (sent_at DESC);
-- PARA QUEM foi, gravado no instante do envio.
--
-- A tabela só guardava `channel_id` e `channel_type`, então o histórico dizia
-- "whatsapp" e parava aí: com dois ou mais canais de WhatsApp, era impossível
-- saber qual número recebeu (ou deixou de receber) o alerta — justamente o que
-- se quer saber quando alguém reclama que não foi avisado.
--
-- São colunas próprias, e não um JOIN com `notification_channels`, porque o
-- registro tem de continuar verdadeiro depois: o canal pode ser renomeado,
-- apontado para outro número ou apagado, e um JOIN passaria a exibir o destino
-- de HOJE ao lado de um envio de semanas atrás. Nunca guardam segredo — só o
-- endereçamento (número, e-mail, chat, host do webhook).
ALTER TABLE notification_log ADD COLUMN IF NOT EXISTS channel_name TEXT;
ALTER TABLE notification_log ADD COLUMN IF NOT EXISTS destination TEXT;
-- Nota: bases criadas em 29/07/2026 podem ter a coluna `provider_ids` e o índice
-- `notification_log_queued_idx`, de uma camada de confirmação assíncrona que existiu
-- para a Evolution API e saiu junto com ela. Ficam órfãos de propósito: apagá-los
-- seria DDL destrutivo sem ganho, e não atrapalham.

-- Alias amigável de host (a tabela `hosts` é criada pelo gateway). IF EXISTS evita
-- falhar em base onde `hosts` ainda não existe; server e gateway aplicam schema no
-- boot em ordem indefinida, por isso o ALTER vive nos dois lados.
ALTER TABLE IF EXISTS hosts ADD COLUMN IF NOT EXISTS display_name TEXT;

-- Auto-discovery (Fase 6, P6.2): serviços detectados pelo agente (o gateway grava).
CREATE TABLE IF NOT EXISTS host_services (
    id            BIGSERIAL PRIMARY KEY,
    tenant_id     TEXT NOT NULL,
    hostname      TEXT NOT NULL,
    kind          TEXT NOT NULL,
    detail        TEXT,
    source        TEXT,
    discovered_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (tenant_id, hostname, kind)
);

-- Jornadas de browser (Fase 6, P6.4): roteiro multi-passo (navegar → preencher → asserir).
CREATE TABLE IF NOT EXISTS journeys (
    id                BIGSERIAL PRIMARY KEY,
    tenant_id         TEXT NOT NULL DEFAULT 'default',
    name              TEXT NOT NULL,
    steps             JSONB NOT NULL DEFAULT '[]',
    interval_seconds  INT NOT NULL DEFAULT 300,
    enabled           BOOLEAN NOT NULL DEFAULT TRUE,
    state             TEXT NOT NULL DEFAULT 'UP',
    consecutive_fails INT NOT NULL DEFAULT 0,
    last_diagnosis    TEXT,
    last_checked_at   TIMESTAMPTZ,
    next_check_at     TIMESTAMPTZ,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Sondas multi-região (Fase 6, P6.3): resultado por sonda para cada URL.
CREATE TABLE IF NOT EXISTS probe_results (
    tenant_id      TEXT NOT NULL,
    url            TEXT NOT NULL,
    probe_location TEXT NOT NULL,
    up             BOOLEAN NOT NULL,
    total_ms       DOUBLE PRECISION NOT NULL DEFAULT 0,
    diagnostic     TEXT,
    reported_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
    -- status: código HTTP visto pela sonda remota (0 = nem houve resposta).
    status         INT NOT NULL DEFAULT 0,
    -- truncated: a sonda cortou o corpo no teto de leitura. Marca que total_ms é um
    -- PISO e que a asserção de palavra-chave ficou indeterminada — um reporte assim
    -- NÃO vota como falha no consenso (ver sitecheck.votoDeFalha).
    truncated      BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (tenant_id, url, probe_location)
);
-- Upgrade de bases que já tinham probe_results sem estas colunas (idempotente).
-- O gateway grava as duas (probeReq.status / probeReq.truncated) e o servidor lê.
ALTER TABLE probe_results ADD COLUMN IF NOT EXISTS status INT NOT NULL DEFAULT 0;
ALTER TABLE probe_results ADD COLUMN IF NOT EXISTS truncated BOOLEAN NOT NULL DEFAULT FALSE;
-- SuspectProbeLocations varre por (tenant_id, reported_at) sem url: sem este
-- índice, desqualificar uma sonda doente custaria uma varredura da tabela inteira
-- a cada ciclo de sondagem.
CREATE INDEX IF NOT EXISTS probe_results_loc_idx ON probe_results (tenant_id, reported_at DESC);

-- Métricas derivadas de busca de logs (Fase 5, P5.2): contagem periódica → série.
CREATE TABLE IF NOT EXISTS log_metrics (
    id           BIGSERIAL PRIMARY KEY,
    name         TEXT NOT NULL,
    metric_name  TEXT NOT NULL,                    -- nome da série gravada em metrics
    service      TEXT,
    severity_min INT NOT NULL DEFAULT 0,
    query        TEXT,                             -- termo (body ILIKE %query%)
    enabled      BOOLEAN NOT NULL DEFAULT TRUE,
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Plantão e escalonamento (P4.3).
CREATE TABLE IF NOT EXISTS escalation_policies (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL,
    levels     JSONB NOT NULL DEFAULT '[]',         -- [{wait_seconds:N, channel_ids:[...]}]
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS oncall_rotations (
    id             BIGSERIAL PRIMARY KEY,
    name           TEXT NOT NULL,
    members        JSONB NOT NULL DEFAULT '[]',     -- ["Ana","Bruno",...] rotação semanal
    rotation_start DATE NOT NULL,                   -- início da semana 0
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE IF NOT EXISTS oncall_overrides (
    id          BIGSERIAL PRIMARY KEY,
    rotation_id BIGINT NOT NULL REFERENCES oncall_rotations(id) ON DELETE CASCADE,
    member      TEXT NOT NULL,
    starts_at   TIMESTAMPTZ NOT NULL,
    ends_at     TIMESTAMPTZ NOT NULL
);

-- Colunas de estado de alerta acrescentadas na P4.3 (tabela alert_events já existe).
ALTER TABLE alert_rules  ADD COLUMN IF NOT EXISTS escalation_policy_id BIGINT;
-- Escopo por servidor: lista de hostnames aos quais a regra se aplica. '[]' = global
-- (todos os servidores que reportam a métrica). Um ou mais hostnames = escopo restrito.
ALTER TABLE alert_rules  ADD COLUMN IF NOT EXISTS hosts JSONB NOT NULL DEFAULT '[]';
-- channel_ids: canais que a regra avisa ao disparar. '[]' = todos os habilitados
-- (padrão, compatível com regras antigas). A seleção de canais passou a ser por regra
-- (as rotas de notificação foram descontinuadas).
ALTER TABLE alert_rules  ADD COLUMN IF NOT EXISTS channel_ids JSONB NOT NULL DEFAULT '[]';
ALTER TABLE alert_events ADD COLUMN IF NOT EXISTS escalation_level INT NOT NULL DEFAULT 0;
ALTER TABLE alert_events ADD COLUMN IF NOT EXISTS flapping BOOLEAN NOT NULL DEFAULT FALSE;
-- Quem encerrou o alerta à mão. Preenchido só na resolução manual: um alerta que o
-- avaliador fechou sozinho fica com resolved_by NULL, e assim dá para distinguir
-- "o problema passou" de "alguém disse que passou".
ALTER TABLE alert_events ADD COLUMN IF NOT EXISTS resolved_by TEXT;

-- Checks de website/LP (P4.4, §3.9).
CREATE TABLE IF NOT EXISTS site_checks (
    id                BIGSERIAL PRIMARY KEY,
    tenant_id         TEXT NOT NULL DEFAULT 'default',
    name              TEXT NOT NULL,
    url               TEXT NOT NULL,
    tier              TEXT NOT NULL DEFAULT 'padrao',   -- critico(60s) | padrao(300s) | basico(600s)
    expect_status     INT NOT NULL DEFAULT 200,
    keyword           TEXT,                             -- palavra-chave esperada no corpo
    max_latency_ms    INT NOT NULL DEFAULT 0,           -- 0 = sem limite; acima disso = DEGRADADO
    enabled           BOOLEAN NOT NULL DEFAULT TRUE,
    -- estado da máquina UP → DEGRADADO → SUSPEITO → DOWN
    state             TEXT NOT NULL DEFAULT 'UP',
    consecutive_fails INT NOT NULL DEFAULT 0,
    last_checked_at   TIMESTAMPTZ,
    next_check_at     TIMESTAMPTZ,                      -- reteste em 30s após 1ª falha
    last_diagnosis    TEXT,
    down_since        TIMESTAMPTZ,
    cert_alert_stage  INT NOT NULL DEFAULT 0,           -- 0=nenhum, 30, 15, 7 (último limiar alertado)
    group_name        TEXT NOT NULL DEFAULT 'Geral',    -- agrupamento na status page (P6.4)
    probe_locations   JSONB NOT NULL DEFAULT '[]',      -- sondas designadas (P6.3)
    -- Sitemap (P6.5): um check kind='sitemap' descobre as URLs de um sitemap.xml e
    -- mantém um check-filho (kind='http', parent_id=pai) por página. Filhos coletam
    -- métricas sem alertar individualmente (alerting=false); o pai agrega e alerta.
    kind              TEXT NOT NULL DEFAULT 'http',     -- http | sitemap
    parent_id         BIGINT REFERENCES site_checks(id) ON DELETE CASCADE,
    alerting          BOOLEAN NOT NULL DEFAULT TRUE,    -- dispara notificações? (filhos = false)
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Upgrade de bases que já tinham site_checks sem estas colunas (idempotente; DEPOIS do CREATE).
ALTER TABLE site_checks ADD COLUMN IF NOT EXISTS group_name TEXT NOT NULL DEFAULT 'Geral';
ALTER TABLE site_checks ADD COLUMN IF NOT EXISTS probe_locations JSONB NOT NULL DEFAULT '[]';
ALTER TABLE site_checks ADD COLUMN IF NOT EXISTS kind TEXT NOT NULL DEFAULT 'http';
ALTER TABLE site_checks ADD COLUMN IF NOT EXISTS parent_id BIGINT REFERENCES site_checks(id) ON DELETE CASCADE;
ALTER TABLE site_checks ADD COLUMN IF NOT EXISTS alerting BOOLEAN NOT NULL DEFAULT TRUE;
-- Canais de notificação que o check avisa ao cair/degradar. '[]' = usa as Rotas genéricas.
ALTER TABLE site_checks ADD COLUMN IF NOT EXISTS channel_ids JSONB NOT NULL DEFAULT '[]';
-- public: aparece na status page PÚBLICA (/api/status, sem login)? Default FALSE.
--
-- Antes não havia flag: /api/status publicava TODOS os checks com a URL COMPLETA,
-- caminho e query inclusive. Provado com `curl` sem credencial nenhuma:
-- {"name":"Form page","url":"http://127.0.0.1:9088/form",...}. O painel monitora
-- endpoint interno, ambiente de staging e URL de formulário — isso é
-- reconhecimento pronto, entregue de graça. O default TEM de ser FALSE: publicar é
-- irreversível (o que vazou, vazou), enquanto "não apareceu na status page" é um
-- clique para corrigir. Opt-in é o único default em que o erro do usuário é
-- reparável.
ALTER TABLE site_checks ADD COLUMN IF NOT EXISTS public BOOLEAN NOT NULL DEFAULT FALSE;
-- Backfill ÚNICO: os checks que já existiam quando a coluna nasceu estavam TODOS na
-- status page pública, porque não havia flag. Apagá-los da página junto com o deploy
-- seria trocar um vazamento por uma regressão visível a cliente — a página fica muda
-- e ninguém sabe por quê. Eles permanecem públicos; a proteção vale para os checks
-- criados DAQUI PARA FRENTE, que é onde entram endpoint interno e staging.
--
-- Roda uma vez só: a marca em app_settings é o guarda. Sem ela, todo boot voltaria a
-- publicar o que o operador acabou de esconder — um "conserto" que desfaz a decisão
-- dele a cada restart é pior que o problema original.
DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM app_settings WHERE key = 'site_checks.public.backfill') THEN
        UPDATE site_checks SET public = TRUE WHERE parent_id IS NULL;
        INSERT INTO app_settings (key, value) VALUES ('site_checks.public.backfill', '{"done":true}');
    END IF;
END $$;
-- timeout_ms: limite de resposta DESTE check (0 = default do pacote, 20 s). O
-- campo Target.TimeoutMS existia, era LIDO daqui e mesmo assim não chegava à sonda
-- (o checker montava o Target sem ele): todo check usava o default fixo enquanto o
-- comentário anunciava timeout por check. Ligado de verdade em 13/08/2026, junto com
-- o salto do default de 15 s para 20 s.
ALTER TABLE site_checks ADD COLUMN IF NOT EXISTS timeout_ms INT NOT NULL DEFAULT 0;
CREATE INDEX IF NOT EXISTS site_checks_parent_idx ON site_checks (parent_id);

CREATE TABLE IF NOT EXISTS site_check_results (
    id             BIGSERIAL PRIMARY KEY,
    check_id       BIGINT NOT NULL REFERENCES site_checks(id) ON DELETE CASCADE,
    ts             TIMESTAMPTZ NOT NULL DEFAULT now(),
    ok             BOOLEAN NOT NULL,
    status         INT,
    diagnosis      TEXT,                                -- '' se ok
    dns_ms         DOUBLE PRECISION,
    connect_ms     DOUBLE PRECISION,
    tls_ms         DOUBLE PRECISION,
    ttfb_ms        DOUBLE PRECISION,
    total_ms       DOUBLE PRECISION,
    cert_days_left INT,
    -- truncated: o corpo estourou o teto de leitura da sonda. Sem esta coluna, o
    -- histórico publicava o PISO de total_ms como se fosse o total: medido, corpo
    -- de 10 MiB dava ok=true, trunc=true, total=13,7 ms (só o teto foi lido) e a
    -- tela mostrava 13,7 ms como o tempo da página.
    truncated      BOOLEAN NOT NULL DEFAULT FALSE
);
ALTER TABLE site_check_results ADD COLUMN IF NOT EXISTS truncated BOOLEAN NOT NULL DEFAULT FALSE;
CREATE INDEX IF NOT EXISTS site_check_results_idx ON site_check_results (check_id, ts DESC);
-- UptimeBatch filtra `WHERE ts >= $1 GROUP BY check_id` (varredura temporal, sem
-- check_id): o índice composto acima não serve. BRIN em ts é ideal p/ esta tabela
-- append-only (dados em ordem de inserção) e ocupa uma fração de um btree.
CREATE INDEX IF NOT EXISTS site_check_results_ts_brin ON site_check_results USING brin (ts);

-- Limiares de saúde por recurso (Fase A): warn/crit por métrica (cpu|mem|disk),
-- global (hostname='') com override por host. Sem linha = default embutido no
-- pacote internal/health. tenant_id sempre presente (single-tenant hoje, multi
-- amanhã). Idempotente/aditiva.
CREATE TABLE IF NOT EXISTS host_thresholds (
    tenant_id  TEXT NOT NULL DEFAULT 'default',
    hostname   TEXT NOT NULL DEFAULT '',   -- '' = default global
    metric     TEXT NOT NULL,               -- cpu | mem | disk
    warn       DOUBLE PRECISION NOT NULL,
    crit       DOUBLE PRECISION NOT NULL,
    PRIMARY KEY (tenant_id, hostname, metric)
);

-- Onboarding SSH + cofre cifrado (Fase G): alvos de provisionamento remoto do
-- agente. `secret_blob` guarda a credencial SSH CIFRADA (NaCl secretbox, base64;
-- nonce||ciphertext) — NUNCA em claro. `host_key_fp` fixa (pin/TOFU) o host key do
-- alvo; `serverkey` é a chave de ingestão gerada/reaproveitada. Idempotente/aditiva.
CREATE TABLE IF NOT EXISTS provision_targets (
    id                  BIGSERIAL PRIMARY KEY,
    tenant_id           TEXT NOT NULL DEFAULT 'default',
    name                TEXT NOT NULL,
    host                TEXT NOT NULL,
    ssh_port            INT NOT NULL DEFAULT 22,
    ssh_user            TEXT NOT NULL,
    auth_type           TEXT NOT NULL,           -- password | key
    secret_blob         TEXT NOT NULL,           -- credencial cifrada (secretbox, base64)
    host_key_fp         TEXT,                    -- fingerprint pinado do host key (TOFU)
    serverkey           TEXT,                    -- chave de ingestão associada
    status              TEXT NOT NULL DEFAULT 'pending',
    last_provisioned_at TIMESTAMPTZ,
    created_at          TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX IF NOT EXISTS provision_targets_tenant_idx ON provision_targets (tenant_id);
-- hostname REAL da máquina (capturado por SSH no provisionamento), para casar o
-- alvo com a linha de inventário `hosts` (chave = hostname reportado pelo agente).
-- Sem isto o casamento tentava nome-amigável/IP × hostname e nunca batia.
ALTER TABLE provision_targets ADD COLUMN IF NOT EXISTS hostname TEXT;
CREATE INDEX IF NOT EXISTS provision_targets_hostname_idx ON provision_targets (tenant_id, hostname);

-- ─── Permissões por usuário × servidor (Fase G2) ────────────────────────────
-- Modelo: cada usuário (não-admin) só vê/edita/recebe alerta dos servidores que o
-- admin liberar. Admin ignora tudo isto (vê e pode tudo). A chave de escopo é o
-- hostname técnico (o mesmo de `hosts.hostname` e de labels['host'] no ClickHouse).

-- Permissões diretas usuário → hostname. Flags independentes; na escrita normalizamos
-- can_edit ⇒ can_view (quem edita, vê). notify = recebe alerta daquele servidor no seu
-- canal pessoal. Sem linha para um host = sem acesso àquele host.
CREATE TABLE IF NOT EXISTS user_server_perms (
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    hostname   TEXT   NOT NULL,
    can_view   BOOLEAN NOT NULL DEFAULT TRUE,
    can_edit   BOOLEAN NOT NULL DEFAULT FALSE,
    notify     BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (user_id, hostname)
);

-- Grupos de servidores: atalho de administração. Em vez de marcar 5 servidores para
-- cada usuário da Revoada, cria-se o grupo uma vez e vincula-se o grupo ao usuário. Um
-- servidor pode estar em N grupos (compartilhado). A permissão efetiva do usuário é a
-- UNIÃO das permissões diretas com as herdadas dos grupos que ele participa.
CREATE TABLE IF NOT EXISTS server_groups (
    id         BIGSERIAL PRIMARY KEY,
    name       TEXT NOT NULL UNIQUE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE TABLE IF NOT EXISTS server_group_hosts (
    group_id   BIGINT NOT NULL REFERENCES server_groups(id) ON DELETE CASCADE,
    hostname   TEXT   NOT NULL,
    PRIMARY KEY (group_id, hostname)
);
CREATE TABLE IF NOT EXISTS user_server_groups (
    user_id    BIGINT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id   BIGINT NOT NULL REFERENCES server_groups(id) ON DELETE CASCADE,
    can_edit   BOOLEAN NOT NULL DEFAULT FALSE,
    notify     BOOLEAN NOT NULL DEFAULT FALSE,
    PRIMARY KEY (user_id, group_id)
);
CREATE INDEX IF NOT EXISTS user_server_groups_group_idx ON user_server_groups (group_id);

-- Canal pessoal: um canal de notificação (WhatsApp é o principal) vinculado a um
-- usuário. Recebe SÓ os alertas dos servidores em que o usuário tem notify=true, e
-- NÃO entra no fan-out "todos os canais" das regras (é pessoal, não da regra).
-- NULL = canal global da regra (comportamento atual, intacto).
ALTER TABLE notification_channels ADD COLUMN IF NOT EXISTS user_id BIGINT REFERENCES users(id) ON DELETE CASCADE;

-- Usuário desativado: mantém o histórico mas não loga nem recebe notificação.
ALTER TABLE users ADD COLUMN IF NOT EXISTS disabled BOOLEAN NOT NULL DEFAULT FALSE;

-- Perfil do usuário (Fase G2.1): nome de exibição, email e celular. Novos usuários
-- criados pela tela admin logam por EMAIL — na prática guardamos `username = email`,
-- então o fluxo de login (UserByUsername) não muda e o admin de bootstrap (username
-- 'admin', sem email) segue logando normalmente. O celular alimenta o canal pessoal de
-- WhatsApp (nosso canal principal), criado automaticamente no cadastro.
ALTER TABLE users ADD COLUMN IF NOT EXISTS email     TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS phone     TEXT;
ALTER TABLE users ADD COLUMN IF NOT EXISTS full_name TEXT;
-- Email único (case-insensitive) quando preenchido — login por email exige unicidade.
-- Parcial: não colide com usuários legados sem email (ex.: o admin de bootstrap).
CREATE UNIQUE INDEX IF NOT EXISTS users_email_unique ON users (lower(email)) WHERE email IS NOT NULL AND email <> '';

-- Papéis do Revoada (SPEC §14): admin | operador | leitor. Migra os legados de forma
-- idempotente e pelo MENOR privilégio: 'user'/'viewer' (que só liam) viram leitor;
-- 'editor' vira operador. Papel desconhecido é tratado como leitor pelo middleware.
UPDATE users SET role='leitor'   WHERE role IN ('user','viewer');
UPDATE users SET role='operador' WHERE role = 'editor';
ALTER TABLE users ALTER COLUMN role SET DEFAULT 'leitor';

-- Trilha de auditoria: registra TODA alteração feita pela API (quem, quando, o quê).
-- Append-only e admin-only na leitura. O ator é gravado desnormalizado (sem FK) de
-- propósito: apagar um usuário NÃO pode apagar o rastro do que ele fez — as FKs deste
-- schema usam ON DELETE CASCADE, que levaria a trilha junto.
-- O payload é o corpo da requisição com os segredos já redigidos (senha, chave SSH,
-- serverkey, apikey, token...) — a redação acontece no middleware, nunca aqui.
CREATE TABLE IF NOT EXISTS audit_log (
    id          BIGSERIAL PRIMARY KEY,
    tenant_id   TEXT NOT NULL DEFAULT 'default',
    actor_id    BIGINT,                       -- users.id no momento da ação (NULL = não autenticado)
    actor_name  TEXT NOT NULL DEFAULT '',     -- username/email no momento da ação
    actor_role  TEXT NOT NULL DEFAULT '',     -- admin | user | deploy-token | anônimo
    method      TEXT NOT NULL,                -- POST | PUT | PATCH | DELETE
    path        TEXT NOT NULL,                -- rota concreta (ex.: /api/site-checks/12)
    resource    TEXT NOT NULL DEFAULT '',     -- recurso normalizado (ex.: site-checks)
    target      TEXT NOT NULL DEFAULT '',     -- id/nome do objeto alterado, quando dá para extrair
    status      INT  NOT NULL DEFAULT 0,      -- código HTTP da resposta (>=400 = tentativa falha)
    payload     JSONB NOT NULL DEFAULT '{}',  -- corpo enviado, com segredos redigidos
    ip          TEXT NOT NULL DEFAULT '',
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
-- Consulta típica: janela de tempo (mais recentes primeiro) + filtros por ator/recurso.
CREATE INDEX IF NOT EXISTS audit_log_created_idx  ON audit_log (created_at DESC);
CREATE INDEX IF NOT EXISTS audit_log_actor_idx    ON audit_log (actor_id);
CREATE INDEX IF NOT EXISTS audit_log_resource_idx ON audit_log (resource);

-- Tokens de inscrição (instalador universal): um MESMO arquivo instalado em vários
-- servidores. O instalador universal carrega um destes tokens no lugar da chave de
-- ingestão; ao rodar, o agente se apresenta ao painel e recebe uma chave PRÓPRIA,
-- criada na hora e amarrada ao hostname daquela máquina.
--
-- A separação importa: o token só CRIA chave, não envia telemetria nem lê nada. Se
-- vazar, o estrago é alguém cadastrar servidores falsos — visível no inventário, e
-- estancado revogando o token, sem tocar nos agentes que já entraram por ele (a
-- chave de cada um é independente e continua valendo).
--
-- Sem prazo de validade por decisão do usuário: vale até ser revogado à mão.
CREATE TABLE IF NOT EXISTS enrollment_tokens (
    token        TEXT PRIMARY KEY,
    tenant_id    TEXT NOT NULL DEFAULT 'default',
    label        TEXT NOT NULL DEFAULT '',     -- nome dado pelo admin (ex.: "Mutirão agosto")
    revoked      BOOLEAN NOT NULL DEFAULT FALSE,
    uses         INT NOT NULL DEFAULT 0,       -- quantos servidores entraram por este token
    last_used_at TIMESTAMPTZ,
    created_by   TEXT NOT NULL DEFAULT '',     -- quem gerou (desnormalizado, como na auditoria)
    created_at   TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Faxina de canais fantasma. Apagar um canal de notificação deixava o ID dele dentro
-- de `channel_ids` das regras e dos checks de site para sempre: a lista de regras
-- contava o fantasma ("1 canal(is)" numa regra sem nenhuma caixa marcada) e não havia
-- como desmarcá-lo, porque o canal não existe mais. Pior, uma regra que só apontasse
-- para fantasmas parecia configurada e não avisava ninguém.
--
-- A exclusão de canal passou a limpar as referências (store.DeleteChannel); isto aqui
-- conserta o que já estava gravado. É idempotente e barato — o WHERE só encontra
-- linha quando ainda há fantasma —, então roda a cada boot como invariante.
--
-- Efeito de borda assumido: regra que apontava SÓ para fantasmas fica com a lista
-- vazia, que significa "todos os canais ativos". Antes ela não avisava ninguém — um
-- alerta ruidoso é recuperável, um alerta que não chega a ninguém não é.
UPDATE alert_rules SET channel_ids = COALESCE((
    SELECT jsonb_agg(v) FROM jsonb_array_elements(channel_ids) AS v
    WHERE (v #>> '{}')::bigint IN (SELECT id FROM notification_channels)
), '[]'::jsonb)
WHERE EXISTS (
    SELECT 1 FROM jsonb_array_elements(channel_ids) AS v
    WHERE (v #>> '{}')::bigint NOT IN (SELECT id FROM notification_channels)
);

UPDATE site_checks SET channel_ids = COALESCE((
    SELECT jsonb_agg(v) FROM jsonb_array_elements(channel_ids) AS v
    WHERE (v #>> '{}')::bigint IN (SELECT id FROM notification_channels)
), '[]'::jsonb)
WHERE EXISTS (
    SELECT 1 FROM jsonb_array_elements(channel_ids) AS v
    WHERE (v #>> '{}')::bigint NOT IN (SELECT id FROM notification_channels)
);

-- Freio da auto-atualização dos agentes (por chave). O interruptor GLOBAL (desligar
-- tudo / fixar versão) mora em app_settings; esta tabela é o "segure ESTE host".
--
-- Por que existe: o deploy do painel é automático (git push → runner publica o binário
-- em dist/), e todo agente com panel_url pergunta de hora em hora se há versão nova.
-- Sem freio, uma versão que quebre a coleta num Debian específico se espalha pela frota
-- inteira dentro de ~1h e não há botão para segurar os demais enquanto se investiga.
--
-- Tabela separada (em vez de coluna em `agents`) de propósito: `agents` é criada e
-- mantida pelo schema do GATEWAY, e o server não pode depender da ordem de boot dos
-- dois processos para ter a coluna. Aqui a chave é a serverkey, sem FK — apagar uma
-- chave limpa a linha explicitamente (store.DeleteAgentUpdatePolicy).
CREATE TABLE IF NOT EXISTS agent_update_policy (
    serverkey   TEXT PRIMARY KEY,
    hold        BOOLEAN NOT NULL DEFAULT FALSE,
    hold_reason TEXT NOT NULL DEFAULT '',
    report      JSONB,                 -- último relato do agente (o que ele tentou e deu)
    reported_at TIMESTAMPTZ,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- AUTO-DESINSTALAÇÃO DO AGENTE (ordem de remoção pendente, por chave).
--
-- Apagar um servidor pelo painel removia os DADOS, mas o agente continuava rodando na
-- máquina — e só o SSH o tirava de lá. Quem instalou à mão e não tem credencial
-- guardada ficava com um processo órfão batendo no gateway e levando 401 para sempre.
--
-- A ordem viaja pelo canal que já existe: o agente pergunta de hora em hora em
-- /api/agent/update-check "qual versão eu deveria rodar?", e a resposta passa a poder
-- dizer "nenhuma — desinstale-se". O canal já entrega BINÁRIO para o agente executar,
-- então mandar remover é estritamente menos poder do que ele já tem.
--
-- Por que a ordem mora AQUI e não numa coluna de `agents`: `agents` é do schema do
-- GATEWAY (ver agent_update_policy acima, mesma decisão e mesmo motivo).
--
-- A chave da ingestão é REVOGADA no instante da ordem (o host não pode voltar a
-- aparecer enquanto o agente não morre), mas a linha da chave SOBREVIVE até a
-- desinstalação terminar: sem a chave o agente não consegue nem perguntar, e a ordem
-- nunca chegaria nele. É a inversão que a feature exige.
CREATE TABLE IF NOT EXISTS agent_uninstalls (
    serverkey   TEXT PRIMARY KEY,
    hostname    TEXT NOT NULL DEFAULT '',
    ordered_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    ordered_by  TEXT NOT NULL DEFAULT '',   -- quem mandou (trilha de auditoria já guarda o resto)
    sent_at     TIMESTAMPTZ,                -- quando o painel de fato ENTREGOU a ordem ao agente
    reported_at TIMESTAMPTZ,                -- quando o agente confirmou que executou
    result      TEXT NOT NULL DEFAULT ''    -- o que o agente relatou ('ok', ou o erro)
);
CREATE INDEX IF NOT EXISTS agent_uninstalls_ordered_idx ON agent_uninstalls (ordered_at);

-- Containers ignorados pelo alerta: parados de propósito (desativados, reserva) e que
-- não devem mais alertar. Vale para TODA regra que olha o container (host+container):
-- o avaliador pula a série e o alerta aberto é encerrado calado. O container não é
-- tocado no servidor. Apagar a linha = voltar a vigiar. Idempotente/aditiva.
CREATE TABLE IF NOT EXISTS ignored_containers (
    tenant_id  TEXT NOT NULL DEFAULT 'default',
    hostname   TEXT NOT NULL,
    container  TEXT NOT NULL,
    created_by TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, hostname, container)
);

