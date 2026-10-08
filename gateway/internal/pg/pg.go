// Package pg encapsula o acesso ao PostgreSQL (metadados): autenticação de
// agentes e inventário de hosts.
package pg

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/eduardorarruda/revoada/gateway/internal/config"
	"github.com/eduardorarruda/revoada/gateway/internal/model"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// touchEvery é o intervalo mínimo entre escritas de last_seen por serverkey. A
// autenticação ocorre a cada requisição de ingestão (muitas por segundo); sem o
// throttle isso viraria uma tempestade de UPDATE. 60s dá um "última atividade"
// preciso o bastante para a tela de chaves sem pressionar o Postgres.
const touchEvery = 60 * time.Second

// schema.sql é a FONTE ÚNICA do schema de metadados (aplicado on-connect, idempotente).
// Quando a Fase 2 introduzir um migrator Postgres (golang-migrate), este embed vira a
// migration 001 e sai daqui — até lá, não duplicar este DDL em outro lugar.
//
//go:embed schema.sql
var schemaSQL string

// Store é o acesso ao PostgreSQL.
type Store struct {
	pool  *pgxpool.Pool
	cache *authCache

	// throttle de last_seen: última vez que gravamos last_seen de cada serverkey.
	touchMu   sync.Mutex
	touchedAt map[string]time.Time
}

// Connect abre o pool (dimensionado explicitamente) e garante o schema.
func Connect(ctx context.Context, dsn string) (*Store, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	// Tuning explícito do pool: com o cache de auth a pressão de leitura cai,
	// mas ainda assim limitamos conexões e reciclamos as ociosas/antigas.
	cfg.MaxConns = int32(config.PGMaxConns())
	cfg.MaxConnLifetime = time.Hour
	cfg.MaxConnIdleTime = 30 * time.Minute
	cfg.HealthCheckPeriod = time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, err
	}
	s := &Store{pool: pool, cache: newAuthCache(config.AuthCacheTTL()), touchedAt: map[string]time.Time{}}
	if err := s.ensureSchema(ctx); err != nil {
		pool.Close()
		return nil, err
	}
	// A faxina em memória sobe COM o Store, e não por uma chamada extra de quem
	// conecta. Era assim que ela ficava de fora: Run existia, estava testada e nunca
	// era chamada do main — o cache de auth e o mapa de throttle voltavam a crescer
	// sem fim, exatamente o vazamento que ela fecha. O ctx aqui é o do processo
	// (cancelado no encerramento), o mesmo que governa o pool.
	go s.Run(ctx)
	return s, nil
}

// travaMigracoesPainel é a MESMA trava de server/internal/store (migrar.go,
// travaMigracoes "REVMIG"). Sem ela, na primeira subida o gateway criava agents/hosts
// ao mesmo tempo em que o painel aplicava a migration 0001 (que cria as mesmas tabelas),
// e os dois CREATE TABLE IF NOT EXISTS colidiam no catálogo do Postgres
// (pg_type_typname_nsp_index): o painel caía e só subia no restart. Medido no
// docker compose da raiz, banco novo.
const travaMigracoesPainel = 0x52_45_56_4D_49_47

func (s *Store) ensureSchema(ctx context.Context) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(travaMigracoesPainel)); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, schemaSQL)
		return err
	})
}

// Close fecha o pool.
func (s *Store) Close() { s.pool.Close() }

// Run mantém as estruturas em memória do Store enxutas até o ctx ser cancelado:
// varre o cache de autenticação (entradas vencidas) e o mapa de throttle de last_seen.
// Sem isto, ambos só crescem — foi o que fez 120 mil chaves inventadas levarem o
// gateway de 32 para 102 MiB numa rajada.
func (s *Store) Run(ctx context.Context) {
	go s.cache.Run(ctx)
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			cutoff := time.Now().Add(-24 * time.Hour)
			s.touchMu.Lock()
			for k, v := range s.touchedAt {
				if v.Before(cutoff) {
					delete(s.touchedAt, k)
				}
			}
			s.touchMu.Unlock()
		}
	}
}

// Ping valida a conexão (usado no readiness).
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// Agent é o resultado da autenticação por serverkey.
type Agent struct {
	TenantID string
	Revoked  bool
	// Hostname vinculado à chave (coluna agents.hostname). Pode estar vazio ou ser
	// um rótulo cosmético (nome amigável) em chaves antigas — por isso a amarração
	// de host (anti-spoofing) só age quando explicitamente habilitada. Ver hostbind.
	Hostname string
}

// ErrUnknownAgent indica serverkey não cadastrada.
var ErrUnknownAgent = errors.New("serverkey desconhecida")

// Authenticate resolve a serverkey. Devolve ErrUnknownAgent se não existir.
// Atendido por um cache em memória com TTL curto (ver authCache) — a semântica de
// retorno para os chamadores é idêntica à da consulta direta.
func (s *Store) Authenticate(ctx context.Context, serverkey string) (Agent, error) {
	a, err := s.cache.lookup(serverkey, func() (Agent, error) {
		return s.queryAgent(ctx, serverkey)
	})
	// Registra atividade da chave (last_seen) em toda ingestão autenticada e válida.
	// Assíncrono e com throttle para não pesar no caminho quente da ingestão.
	if err == nil && !a.Revoked {
		s.maybeTouch(serverkey)
	}
	return a, err
}

// maybeTouch grava last_seen do serverkey no máximo uma vez por `touchEvery`, num
// goroutine com contexto próprio — a ingestão nunca espera pelo UPDATE.
func (s *Store) maybeTouch(serverkey string) {
	now := time.Now()
	s.touchMu.Lock()
	if last, ok := s.touchedAt[serverkey]; ok && now.Sub(last) < touchEvery {
		s.touchMu.Unlock()
		return
	}
	s.touchedAt[serverkey] = now
	s.touchMu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = s.TouchAgent(ctx, serverkey, now)
	}()
}

// queryAgent faz a consulta direta ao PostgreSQL (fonte da verdade do cache).
func (s *Store) queryAgent(ctx context.Context, serverkey string) (Agent, error) {
	var a Agent
	err := s.pool.QueryRow(ctx,
		`SELECT tenant_id, revoked, COALESCE(hostname,'') FROM agents WHERE serverkey = $1`, serverkey,
	).Scan(&a.TenantID, &a.Revoked, &a.Hostname)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrUnknownAgent
	}
	return a, err
}

// TouchAgent atualiza last_seen do agente.
func (s *Store) TouchAgent(ctx context.Context, serverkey string, t time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE agents SET last_seen = $2 WHERE serverkey = $1`, serverkey, t)
	return err
}

// AssignedURL é uma URL que uma sonda deve monitorar (site_check com esta location).
type AssignedURL struct {
	URL          string `json:"url"`
	ExpectStatus int    `json:"expect_status"`
	Keyword      string `json:"keyword"`
	// TimeoutMS é o limite do check. Zero = "use o seu default" — o caso normal
	// hoje, já que a coluna nasce em 0. Viaja aqui porque a régua não pode morar em
	// duas constantes compiladas: quando a da central mudou e a do agente não, o
	// consenso passou a ler a diferença de RELÓGIO como queda do alvo.
	TimeoutMS int `json:"timeout_ms"`
}

// AssignedChecks devolve as URLs cujos site_checks designam esta location em
// probe_locations (P6.3). Assim, criar um check já faz as sondas o monitorarem.
func (s *Store) AssignedChecks(ctx context.Context, tenant, location string) ([]AssignedURL, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT url, expect_status, COALESCE(keyword,'') FROM site_checks
		WHERE tenant_id=$1 AND enabled AND probe_locations ? $2`, tenant, location)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AssignedURL
	for rows.Next() {
		var a AssignedURL
		if err := rows.Scan(&a.URL, &a.ExpectStatus, &a.Keyword); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// probeRebindAfter é quanto tempo um vínculo chave↔localização precisa ficar PARADO
// para poder ser trocado.
//
// Dimensionamento: a sonda reporta a cada 30 s (agent/internal/probe.Run), e o
// consenso só olha reportes frescos (server: ProbeFreshness). 1 h é ~120 ciclos de
// silêncio — nenhuma sonda viva chega perto disso, e é curto o bastante para o
// operador que trocou o `probe_location` no config não ficar preso: reinicia a sonda,
// espera o vínculo antigo esfriar e o novo nome pega. O que a janela IMPEDE é o que
// interessa: uma chave não consegue votar por dois nomes ao mesmo tempo, porque para
// assumir o segundo ela tem de ter parado de reportar pelo primeiro por uma hora — e
// aí o voto do primeiro já não é fresco para ninguém.
const probeRebindAfter = time.Hour

// ErrProbeLocationDenied indica que a serverkey não pode reportar por essa localização
// (ou ela pertence a outra chave, ou esta chave já está vinculada a outro nome).
var ErrProbeLocationDenied = errors.New("localização de sonda não registrada para esta chave")

// BindProbeLocation garante o vínculo 1:1 entre a serverkey e a localização de sonda,
// e devolve ErrProbeLocationDenied quando o reporte é um voto forjado.
//
// POR QUE (medido): o endpoint aceitava `probe_location` livre de QUALQUER serverkey
// válida. Com uma única chave foram forjados dois votos — `caos-fake-tokyo` e
// `caos-fake-berlim` — e um check de origem única, que a sonda central via no ar,
// virou `DOWN — down em 2 sonda(s)`. O lado da LEITURA já foi corrigido no servidor
// (só conta localização designada no check); esta é a outra metade: a escrita.
//
// A transação lê as duas linhas que interessam (a da chave e a da localização) numa
// única consulta com ORDER BY, para que o FOR UPDATE pegue as linhas sempre na mesma
// ordem — dois reportes concorrentes não travam um no outro.
func (s *Store) BindProbeLocation(ctx context.Context, tenant, serverkey, location string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `
		SELECT serverkey, probe_location, last_report FROM probe_agents
		WHERE tenant_id=$1 AND (serverkey=$2 OR probe_location=$3)
		ORDER BY serverkey
		FOR UPDATE`, tenant, serverkey, location)
	if err != nil {
		return err
	}
	type binding struct {
		key, loc string
		last     time.Time
	}
	var found []binding
	for rows.Next() {
		var b binding
		if err := rows.Scan(&b.key, &b.loc, &b.last); err != nil {
			rows.Close()
			return err
		}
		found = append(found, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	now := time.Now()
	for _, b := range found {
		switch {
		case b.key == serverkey && b.loc == location:
			// Vínculo já é este: segue.
		case b.key == serverkey:
			// A chave já reporta por OUTRO nome. Só troca se o antigo estiver parado.
			if now.Sub(b.last) < probeRebindAfter {
				return ErrProbeLocationDenied
			}
		default:
			// A localização pertence a OUTRA chave. Só é liberada se aquela sonda
			// estiver muda há tempo (senão qualquer chave sequestra o nome da sonda boa).
			if now.Sub(b.last) < probeRebindAfter {
				return ErrProbeLocationDenied
			}
			if _, err := tx.Exec(ctx,
				`DELETE FROM probe_agents WHERE tenant_id=$1 AND probe_location=$2`, tenant, b.loc); err != nil {
				return err
			}
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO probe_agents (tenant_id, serverkey, probe_location, bound_at, last_report)
		VALUES ($1,$2,$3, now(), now())
		ON CONFLICT (tenant_id, serverkey) DO UPDATE SET
			probe_location = EXCLUDED.probe_location,
			bound_at = CASE WHEN probe_agents.probe_location = EXCLUDED.probe_location
			                THEN probe_agents.bound_at ELSE now() END,
			last_report = now()`, tenant, serverkey, location); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ProbeResult é o reporte de uma sonda para uma URL (P6.3).
type ProbeResult struct {
	URL        string
	Location   string
	Up         bool
	TotalMs    float64
	Diagnostic string
	// Truncated e Status vêm do agente-sonda e eram DESCARTADOS antes de chegar aqui
	// (o probeReq nem declarava os campos). Sem eles o painel apresenta um total_ms
	// que é só um piso como se fosse a medida completa, e não sabe dizer POR QUE a
	// asserção falhou. As colunas já existem em probe_results e o servidor já as lê
	// (server/internal/store/discovery.go: FreshProbeResults).
	Truncated bool
	Status    int
}

// UpsertProbeResult grava o resultado mais recente de uma sonda para uma URL (P6.3).
func (s *Store) UpsertProbeResult(ctx context.Context, tenant string, r ProbeResult) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO probe_results (tenant_id, url, probe_location, up, total_ms, diagnostic, status, truncated, reported_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8, now())
		ON CONFLICT (tenant_id, url, probe_location) DO UPDATE SET
			up=EXCLUDED.up, total_ms=EXCLUDED.total_ms, diagnostic=EXCLUDED.diagnostic,
			status=EXCLUDED.status, truncated=EXCLUDED.truncated, reported_at=now()`,
		tenant, r.URL, r.Location, r.Up, r.TotalMs, r.Diagnostic, r.Status, r.Truncated)
	return err
}

// HostService é um serviço detectado no host (auto-discovery).
type HostService struct {
	Kind   string
	Detail string
	Source string
}

// SaveDiscovery substitui a descoberta de um host (delete + insert numa transação).
func (s *Store) SaveDiscovery(ctx context.Context, tenant, hostname string, services []HostService) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `DELETE FROM host_services WHERE tenant_id=$1 AND hostname=$2`, tenant, hostname); err != nil {
		return err
	}
	for _, sv := range services {
		if _, err := tx.Exec(ctx,
			`INSERT INTO host_services (tenant_id, hostname, kind, detail, source) VALUES ($1,$2,$3,$4,$5)`,
			tenant, hostname, sv.Kind, sv.Detail, sv.Source); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// UpsertHost grava/atualiza o inventário de um host.
func (s *Store) UpsertHost(ctx context.Context, h model.Host) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO hosts (tenant_id, hostname, os, kernel, arch, cpu_model, cpu_cores, ips, agent_version, uptime_secs, last_seen)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
		ON CONFLICT (tenant_id, hostname) DO UPDATE SET
			os=EXCLUDED.os, kernel=EXCLUDED.kernel, arch=EXCLUDED.arch,
			cpu_model=EXCLUDED.cpu_model, cpu_cores=EXCLUDED.cpu_cores, ips=EXCLUDED.ips,
			agent_version=EXCLUDED.agent_version, uptime_secs=EXCLUDED.uptime_secs, last_seen=EXCLUDED.last_seen`,
		h.TenantID, h.Hostname, h.OS, h.Kernel, h.Arch, h.CPUModel, h.CPUCores, h.IPs, h.AgentVersion, h.UptimeSecs, h.LastSeen)
	return err
}

// ScrapeTarget é um alvo de scrape Prometheus.
type ScrapeTarget struct {
	TenantID        string
	URL             string
	IntervalSeconds int
	Labels          map[string]string
}

// ListScrapeTargets devolve os alvos habilitados.
func (s *Store) ListScrapeTargets(ctx context.Context) ([]ScrapeTarget, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT tenant_id, url, interval_seconds, labels FROM scrape_targets WHERE enabled ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ScrapeTarget
	for rows.Next() {
		var t ScrapeTarget
		var labelsJSON []byte
		if err := rows.Scan(&t.TenantID, &t.URL, &t.IntervalSeconds, &labelsJSON); err != nil {
			return nil, err
		}
		if t.IntervalSeconds <= 0 {
			t.IntervalSeconds = 15
		}
		t.Labels = map[string]string{}
		if len(labelsJSON) > 0 {
			_ = json.Unmarshal(labelsJSON, &t.Labels)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// AddScrapeTarget cadastra/atualiza um alvo — usado em testes e provisionamento.
func (s *Store) AddScrapeTarget(ctx context.Context, t ScrapeTarget) error {
	labelsJSON, err := json.Marshal(t.Labels)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO scrape_targets (tenant_id, url, interval_seconds, labels, enabled)
		VALUES ($1,$2,$3,$4,TRUE)
		ON CONFLICT (tenant_id, url) DO UPDATE SET
			interval_seconds=EXCLUDED.interval_seconds, labels=EXCLUDED.labels, enabled=TRUE`,
		t.TenantID, t.URL, t.IntervalSeconds, labelsJSON)
	return err
}

// RegisterAgent cadastra (ou reativa) uma serverkey — usado em testes e provisionamento.
func (s *Store) RegisterAgent(ctx context.Context, serverkey, tenant, hostname string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO agents (serverkey, tenant_id, hostname, revoked)
		VALUES ($1,$2,$3,FALSE)
		ON CONFLICT (serverkey) DO UPDATE SET tenant_id=EXCLUDED.tenant_id, hostname=EXCLUDED.hostname, revoked=FALSE`,
		serverkey, tenant, hostname)
	return err
}
