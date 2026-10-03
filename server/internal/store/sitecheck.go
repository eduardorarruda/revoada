package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// SiteCheck é uma sonda sintética de website/LP.
type SiteCheck struct {
	ID               int64      `json:"id"`
	TenantID         string     `json:"tenant_id"`
	Name             string     `json:"name"`
	URL              string     `json:"url"`
	Tier             string     `json:"tier"`
	ExpectStatus     int        `json:"expect_status"`
	Keyword          string     `json:"keyword"`
	MaxLatencyMS     int        `json:"max_latency_ms"`
	Enabled          bool       `json:"enabled"`
	State            string     `json:"state"`
	ConsecutiveFails int        `json:"consecutive_fails"`
	LastCheckedAt    *time.Time `json:"last_checked_at"`
	NextCheckAt      *time.Time `json:"next_check_at"`
	LastDiagnosis    string     `json:"last_diagnosis"`
	DownSince        *time.Time `json:"down_since"`
	CertAlertStage   int        `json:"cert_alert_stage"`
	GroupName        string     `json:"group_name"`      // agrupamento na status page (P6.4)
	ProbeLocations   []string   `json:"probe_locations"` // sondas designadas (P6.3)
	Kind             string     `json:"kind"`            // http | sitemap (P6.5)
	ParentID         *int64     `json:"parent_id"`       // filho de um check sitemap
	Alerting         bool       `json:"alerting"`        // dispara notificações (filhos = false)
	// ChannelIDs são os canais de notificação que este check avisa quando cai/degrada.
	// Vazio = usa as Rotas de notificação genéricas (retrocompatível). Ver checker.emit.
	ChannelIDs []int64 `json:"channel_ids"`
	// Public decide se este check aparece na status page PÚBLICA, com a URL completa.
	// O default é FALSE porque o painel monitora endpoint interno e de staging: publicar
	// a URL monitorada (com caminho e query) sem login é reconhecimento pronto para quem
	// quiser sondar o domínio. Publicar é decisão consciente, não herança.
	Public bool `json:"public"`
	// TimeoutMS é o limite de resposta DESTE check; 0 = o default do pacote (20 s).
	TimeoutMS int `json:"timeout_ms"`
}

// TierInterval devolve o intervalo de sondagem de um tier.
func TierInterval(tier string) time.Duration {
	switch tier {
	case "critico":
		return 60 * time.Second
	case "basico":
		return 600 * time.Second
	default:
		return 300 * time.Second
	}
}

func (s *Store) CreateSiteCheck(ctx context.Context, c SiteCheck) (int64, error) {
	locs, _ := json.Marshal(orSlice(c.ProbeLocations))
	chans, _ := json.Marshal(orInts(c.ChannelIDs))
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO site_checks (tenant_id, name, url, tier, expect_status, keyword, max_latency_ms, group_name, probe_locations, kind, parent_id, alerting, channel_ids, timeout_ms)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) RETURNING id`,
		orStr(c.TenantID, "default"), c.Name, c.URL, orStr(c.Tier, "padrao"),
		orInt(c.ExpectStatus, 200), c.Keyword, c.MaxLatencyMS, orStr(c.GroupName, "Geral"), locs,
		orStr(c.Kind, "http"), c.ParentID, c.Alerting, chans, c.TimeoutMS).Scan(&id)
	return id, err
}

func orSlice(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

// orInts normaliza um slice nil para `[]` (nunca `null`) no JSONB de channel_ids.
func orInts(s []int64) []int64 {
	if s == nil {
		return []int64{}
	}
	return s
}

// UpdateSiteCheck atualiza apenas as colunas de CONFIGURAÇÃO do check (não toca
// nas colunas de estado de runtime, que ficam a cargo de UpdateSiteCheckState).
// timeoutMS nil mantém o limite gravado: o formulário da tela não manda o campo, e
// zerá-lo a cada edição apagaria o timeout configurado pela API.
func (s *Store) UpdateSiteCheck(ctx context.Context, id int64, c SiteCheck, timeoutMS *int) error {
	locs, _ := json.Marshal(orSlice(c.ProbeLocations))
	chans, _ := json.Marshal(orInts(c.ChannelIDs))
	ct, err := s.pool.Exec(ctx, `
		UPDATE site_checks SET name=$2, url=$3, tier=$4, expect_status=$5, keyword=$6,
		       max_latency_ms=$7, group_name=$8, probe_locations=$9, kind=$10, channel_ids=$11,
		       timeout_ms=COALESCE($12, timeout_ms) WHERE id=$1`,
		id, c.Name, c.URL, orStr(c.Tier, "padrao"), orInt(c.ExpectStatus, 200),
		c.Keyword, c.MaxLatencyMS, orStr(c.GroupName, "Geral"), locs, orStr(c.Kind, "http"), chans, timeoutMS)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ListSiteChecks devolve os checks "de topo" (pais e páginas avulsas), sem os
// filhos descobertos por sitemaps — estes só aparecem no overview e no detalhe.
func (s *Store) ListSiteChecks(ctx context.Context) ([]SiteCheck, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, name, url, tier, expect_status, COALESCE(keyword,''), max_latency_ms, enabled,
		       state, consecutive_fails, last_checked_at, next_check_at, COALESCE(last_diagnosis,''), down_since, cert_alert_stage,
		       COALESCE(group_name,'Geral'), COALESCE(probe_locations,'[]'), kind, parent_id, alerting, COALESCE(channel_ids,'[]'),
		       COALESCE(public,false), COALESCE(timeout_ms,0)
		FROM site_checks WHERE parent_id IS NULL ORDER BY name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChecks(rows)
}

// ListSiteChecksTree devolve TODOS os checks (pais e filhos de sitemap), para a
// tela de visão geral. Filhos vêm logo após o pai (ordenados por parent_id, name).
func (s *Store) ListSiteChecksTree(ctx context.Context) ([]SiteCheck, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, name, url, tier, expect_status, COALESCE(keyword,''), max_latency_ms, enabled,
		       state, consecutive_fails, last_checked_at, next_check_at, COALESCE(last_diagnosis,''), down_since, cert_alert_stage,
		       COALESCE(group_name,'Geral'), COALESCE(probe_locations,'[]'), kind, parent_id, alerting, COALESCE(channel_ids,'[]'),
		       COALESCE(public,false), COALESCE(timeout_ms,0)
		FROM site_checks ORDER BY COALESCE(parent_id, id), (parent_id IS NOT NULL), name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChecks(rows)
}

// SiteCheckByID devolve um check pelo id. Necessário para calcular a COBERTURA do
// uptime numa consulta a um check só: as amostras esperadas dependem do tier.
func (s *Store) SiteCheckByID(ctx context.Context, id int64) (SiteCheck, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, name, url, tier, expect_status, COALESCE(keyword,''), max_latency_ms, enabled,
		       state, consecutive_fails, last_checked_at, next_check_at, COALESCE(last_diagnosis,''), down_since, cert_alert_stage,
		       COALESCE(group_name,'Geral'), COALESCE(probe_locations,'[]'), kind, parent_id, alerting, COALESCE(channel_ids,'[]'),
		       COALESCE(public,false), COALESCE(timeout_ms,0)
		FROM site_checks WHERE id=$1`, id)
	if err != nil {
		return SiteCheck{}, err
	}
	defer rows.Close()
	out, err := scanChecks(rows)
	if err != nil {
		return SiteCheck{}, err
	}
	if len(out) == 0 {
		return SiteCheck{}, ErrNotFound
	}
	return out[0], nil
}

// ChildURLs devolve as URLs dos filhos de um check sitemap (para reconciliação).
func (s *Store) ChildURLs(ctx context.Context, parentID int64) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, url FROM site_checks WHERE parent_id=$1`, parentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var id int64
		var u string
		if err := rows.Scan(&id, &u); err != nil {
			return nil, err
		}
		out[u] = id
	}
	return out, rows.Err()
}

// SyncSitemapChildren reconcilia os filhos de um check sitemap com o conjunto de
// URLs descobertas: cria os que faltam (kind='http', alerting=false, herdando
// tier/grupo do pai) e apaga os que sumiram do sitemap. Devolve (criados, removidos).
func (s *Store) SyncSitemapChildren(ctx context.Context, parent SiteCheck, urls []string) (int, int, error) {
	existing, err := s.ChildURLs(ctx, parent.ID)
	if err != nil {
		return 0, 0, err
	}
	// filhos agrupam sob o nome do sitemap por padrão (fica melhor no dashboard).
	childGroup := parent.GroupName
	if childGroup == "" || childGroup == "Geral" {
		childGroup = parent.Name
	}
	want := make(map[string]bool, len(urls))
	added := 0
	for _, u := range urls {
		want[u] = true
		if _, ok := existing[u]; ok {
			continue
		}
		name := pageName(u)
		// espalha o primeiro next_check_at ao longo do intervalo, evitando thundering herd.
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO site_checks (tenant_id, name, url, tier, expect_status, group_name, kind, parent_id, alerting, next_check_at)
			VALUES ($1,$2,$3,$4,$5,$6,'http',$7,false, now() + (random() * interval '1 second') * $8)`,
			orStr(parent.TenantID, "default"), name, u, orStr(parent.Tier, "padrao"),
			orInt(parent.ExpectStatus, 200), childGroup, parent.ID,
			int(TierInterval(parent.Tier).Seconds())); err != nil {
			return added, 0, err
		}
		added++
	}
	removed := 0
	for u, id := range existing {
		if want[u] {
			continue
		}
		if _, err := s.pool.Exec(ctx, `DELETE FROM site_checks WHERE id=$1`, id); err != nil {
			return added, removed, err
		}
		removed++
	}
	return added, removed, nil
}

// pageName deriva um rótulo legível de uma URL (o path, ou o host para a raiz).
func pageName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Path == "" || u.Path == "/" {
		if err == nil && u.Host != "" {
			return u.Host + "/"
		}
		return raw
	}
	p := strings.TrimRight(u.Path, "/")
	if p == "" {
		return u.Host + "/"
	}
	return p
}

// CountChildStates resume os estados dos filhos de um sitemap (para o pai agregar).
func (s *Store) CountChildStates(ctx context.Context, parentID int64) (total, down, degraded int, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT count(*),
		       count(*) FILTER (WHERE state='DOWN'),
		       count(*) FILTER (WHERE state IN ('DEGRADADO','SUSPEITO'))
		FROM site_checks WHERE parent_id=$1`, parentID).Scan(&total, &down, &degraded)
	return
}

// DueSiteChecks devolve os checks habilitados prontos para rodar (next_check_at nulo ou vencido).
func (s *Store) DueSiteChecks(ctx context.Context, now time.Time) ([]SiteCheck, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, name, url, tier, expect_status, COALESCE(keyword,''), max_latency_ms, enabled,
		       state, consecutive_fails, last_checked_at, next_check_at, COALESCE(last_diagnosis,''), down_since, cert_alert_stage,
		       COALESCE(group_name,'Geral'), COALESCE(probe_locations,'[]'), kind, parent_id, alerting, COALESCE(channel_ids,'[]'),
		       COALESCE(public,false), COALESCE(timeout_ms,0)
		FROM site_checks WHERE enabled AND (next_check_at IS NULL OR next_check_at <= $1)`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanChecks(rows)
}

func scanChecks(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]SiteCheck, error) {
	var out []SiteCheck
	for rows.Next() {
		var c SiteCheck
		var locs, chans []byte
		if err := rows.Scan(&c.ID, &c.TenantID, &c.Name, &c.URL, &c.Tier, &c.ExpectStatus, &c.Keyword,
			&c.MaxLatencyMS, &c.Enabled, &c.State, &c.ConsecutiveFails, &c.LastCheckedAt, &c.NextCheckAt,
			&c.LastDiagnosis, &c.DownSince, &c.CertAlertStage, &c.GroupName, &locs,
			&c.Kind, &c.ParentID, &c.Alerting, &chans, &c.Public, &c.TimeoutMS); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(locs, &c.ProbeLocations)
		_ = json.Unmarshal(chans, &c.ChannelIDs)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) DeleteSiteCheck(ctx context.Context, id int64) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM site_checks WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateSiteCheckState persiste o resultado da máquina de estados após uma sondagem.
func (s *Store) UpdateSiteCheckState(ctx context.Context, c SiteCheck) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE site_checks SET state=$2, consecutive_fails=$3, last_checked_at=$4, next_check_at=$5,
		       last_diagnosis=$6, down_since=$7, cert_alert_stage=$8 WHERE id=$1`,
		c.ID, c.State, c.ConsecutiveFails, c.LastCheckedAt, c.NextCheckAt, c.LastDiagnosis, c.DownSince, c.CertAlertStage)
	return err
}

// SiteCheckResult é o registro de uma sondagem.
type SiteCheckResult struct {
	CheckID      int64
	TS           time.Time
	OK           bool
	Status       int
	Diagnosis    string
	DNSMs        float64
	ConnectMs    float64
	TLSMs        float64
	TTFBMs       float64
	TotalMs      float64
	CertDaysLeft int
}

func (s *Store) InsertSiteCheckResult(ctx context.Context, r SiteCheckResult) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO site_check_results (check_id, ok, status, diagnosis, dns_ms, connect_ms, tls_ms, ttfb_ms, total_ms, cert_days_left)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		r.CheckID, r.OK, r.Status, r.Diagnosis, r.DNSMs, r.ConnectMs, r.TLSMs, r.TTFBMs, r.TotalMs, r.CertDaysLeft)
	return err
}

// SiteCheckResultView é uma linha do histórico (para a tela de detalhe).
type SiteCheckResultView struct {
	TS           string  `json:"ts"`
	OK           bool    `json:"ok"`
	Status       int     `json:"status"`
	Diagnosis    string  `json:"diagnosis"`
	DNSMs        float64 `json:"dns_ms"`
	ConnectMs    float64 `json:"connect_ms"`
	TLSMs        float64 `json:"tls_ms"`
	TTFBMs       float64 `json:"ttfb_ms"`
	TotalMs      float64 `json:"total_ms"`
	CertDaysLeft int     `json:"cert_days_left"`
}

// siteHistSelect são as colunas do histórico (compartilhadas pelas consultas de
// histórico recente e por intervalo de datas).
const siteHistSelect = `
	SELECT ts, ok, COALESCE(status,0), COALESCE(diagnosis,''), COALESCE(dns_ms,0), COALESCE(connect_ms,0),
	       COALESCE(tls_ms,0), COALESCE(ttfb_ms,0), COALESCE(total_ms,0), COALESCE(cert_days_left,0)
	FROM site_check_results`

func (s *Store) siteCheckHistoryScan(ctx context.Context, query string, args ...any) ([]SiteCheckResultView, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SiteCheckResultView
	for rows.Next() {
		var v SiteCheckResultView
		var ts time.Time
		if err := rows.Scan(&ts, &v.OK, &v.Status, &v.Diagnosis, &v.DNSMs, &v.ConnectMs, &v.TLSMs,
			&v.TTFBMs, &v.TotalMs, &v.CertDaysLeft); err != nil {
			return nil, err
		}
		v.TS = ts.Format(time.RFC3339)
		out = append(out, v)
	}
	return out, rows.Err()
}

// SiteCheckHistory devolve as sondagens mais recentes (até `limit`).
func (s *Store) SiteCheckHistory(ctx context.Context, checkID int64, limit int) ([]SiteCheckResultView, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.siteCheckHistoryScan(ctx, siteHistSelect+` WHERE check_id=$1 ORDER BY ts DESC LIMIT $2`, checkID, limit)
}

// SiteCheckHistoryRange devolve as sondagens de um check dentro de [from, to)
// (para o filtro "ver um dia específico" no histórico). Ordena da mais recente
// para a mais antiga, limitado a `limit` (default/teto maiores que o recente,
// pois um dia inteiro rende mais linhas).
func (s *Store) SiteCheckHistoryRange(ctx context.Context, checkID int64, from, to time.Time, limit int) ([]SiteCheckResultView, error) {
	if limit <= 0 || limit > 2000 {
		limit = 500
	}
	return s.siteCheckHistoryScan(ctx,
		siteHistSelect+` WHERE check_id=$1 AND ts >= $2 AND ts < $3 ORDER BY ts DESC LIMIT $4`,
		checkID, from, to, limit)
}

// SiteCheckEarliest devolve o instante da sondagem mais antiga ainda retida para
// o check (para limitar o seletor de dia). ok=false quando não há histórico.
func (s *Store) SiteCheckEarliest(ctx context.Context, checkID int64) (time.Time, bool, error) {
	var ts *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT min(ts) FROM site_check_results WHERE check_id=$1`, checkID).Scan(&ts); err != nil {
		return time.Time{}, false, err
	}
	if ts == nil {
		return time.Time{}, false, nil
	}
	return *ts, true, nil
}

// =========================================================================
// UPTIME COM COBERTURA
// =========================================================================
//
// O cálculo anterior era `count(ok)/count(*)` e, quando não havia NENHUMA
// sondagem, devolvia 100. Ausência de medição virava afirmação máxima — e essa
// afirmação era publicada numa status page pública, sem login. Medido no dev: o
// monitor ficou 12,6 dias parado, o check 7 tinha 20 sondagens nas últimas 24 h
// (esperadas 288 pelo tier padrão = 6,9% de cobertura) e 260 sondagens em 26 dias
// de histórico, e mesmo assim /api/status publicava uptime_day/month/90d = 100.
//
// Agora todo uptime vem acompanhado da COBERTURA (amostras observadas ÷ esperadas
// pelo tier no período efetivamente monitorado). Abaixo de MinUptimeCoverage o
// percentual é declarado insuficiente e a tela mostra "sem dados suficientes" —
// nunca 100%. Janela sem sondagem é DESCONHECIDA, não "no ar".

// MinUptimeCoverage é a cobertura mínima (%) para um percentual de uptime poder
// ser exibido como fato. 80% tolera a folga normal do agendador e reinícios
// curtos, mas recusa os 3,4% que o dev estava publicando como "100%".
const MinUptimeCoverage = 80.0

// diagsNaoMedidos lista diagnósticos que NÃO são medida do alvo. Ficam fora do
// numerador E do denominador do uptime: culpar o site por uma decisão nossa, ou por
// uma pergunta que não conseguimos responder, seria inventar indisponibilidade.
//
//   - `bloqueado_pelo_painel`: o nosso guard SSRF recusou o destino, a sondagem nem saiu;
//   - `keyword_indeterminado`: a sondagem saiu e respondeu, mas o corpo foi cortado no
//     teto de leitura antes de a palavra-chave aparecer. Não sabemos se ela estava lá.
//
// Espelha sitecheck.DiagnosisIsUnmeasured (store não pode importar sitecheck — ciclo).
var diagsNaoMedidos = []string{"bloqueado_pelo_painel", "keyword_indeterminado"}

// UptimeStat é o uptime de um check num período JUNTO com a prova de quanto do
// período foi de fato medido. Nunca use Percent sem checar Sufficient.
type UptimeStat struct {
	Percent    float64   `json:"percent"`     // % de sondagens OK entre as observadas
	Samples    int       `json:"samples"`     // sondagens observadas no período
	Expected   int       `json:"expected"`    // sondagens esperadas pelo tier
	Coverage   float64   `json:"coverage"`    // samples/expected * 100 (teto 100)
	Sufficient bool      `json:"sufficient"`  // cobertura ≥ MinUptimeCoverage
	WindowFull bool      `json:"window_full"` // o histórico cobre a janela inteira
	Since      time.Time `json:"-"`           // início do histórico deste check
}

// UptimeCount é a contagem crua de um check num período (insumo do UptimeStat).
type UptimeCount struct {
	Samples int
	OK      int
	// AvgMs é a duração média das sondagens contadas. Entra na conta da COBERTURA
	// porque o agendador só marca a próxima sondagem DEPOIS que a atual termina — ver
	// MakeUptimeStat.
	AvgMs float64
	// SegTotal e SegOK são o uptime PONDERADO PELO TEMPO: cada sondagem vale o
	// tempo até a seguinte (com teto, para um buraco no histórico não virar tempo
	// medido). Existem porque o checker reteste a cada 30 s durante uma queda — contar
	// amostras daria à falha um peso 10 vezes maior que o do tempo em que o site
	// esteve no ar. Zero = consulta sem pesos; MakeUptimeStat cai na conta antiga.
	SegTotal float64
	SegOK    float64
}

// pesoMaximoSQL é o teto (s) do peso de UMA sondagem, por tier: 1,5 × o intervalo
// do tier mais o tempo limite do check. Acima disso o intervalo não é cadência, é
// buraco — o checker parado ou o site pausado — e não pode contar como medido.
// Montado a partir de TierInterval para não divergir se um tier mudar.
func pesoMaximoSQL() string {
	seg := func(tier string) string {
		return strconv.FormatFloat(TierInterval(tier).Seconds()*1.5, 'f', 0, 64)
	}
	return "(CASE c.tier WHEN 'critico' THEN " + seg("critico") +
		" WHEN 'basico' THEN " + seg("basico") +
		" ELSE " + seg("padrao") + " END + GREATEST(c.timeout_ms, 20000) / 1000.0)"
}

// uptimeSQL conta as sondagens de check(s) desde $1 com os pesos de tempo. `filtro`
// entra no WHERE da base (ex.: "AND r.check_id = $3"). O LEAD roda DEPOIS do filtro
// de diagnósticos não medidos: uma sondagem abstida não interrompe a anterior, o
// intervalo dela passa a pertencer à medição que a precedeu (até o teto).
func uptimeSQL(filtro string) string {
	return `
		WITH base AS (
			SELECT r.check_id, r.ok, r.ts, r.total_ms,
			       LEAD(r.ts) OVER (PARTITION BY r.check_id ORDER BY r.ts) AS prox,
			       ` + pesoMaximoSQL() + ` AS teto
			FROM site_check_results r
			JOIN site_checks c ON c.id = r.check_id
			WHERE r.ts >= $1 AND NOT (COALESCE(r.diagnosis,'') = ANY($2)) ` + filtro + `
		), pesado AS (
			SELECT check_id, ok, total_ms,
			       GREATEST(LEAST(EXTRACT(EPOCH FROM (COALESCE(prox, now()) - ts)), teto), 0) AS peso
			FROM base
		)
		SELECT check_id, count(*), count(*) FILTER (WHERE ok), COALESCE(avg(total_ms),0),
		       COALESCE(sum(peso),0), COALESCE(sum(peso) FILTER (WHERE ok),0)
		FROM pesado GROUP BY check_id`
}

// passoDoAgendador é a granularidade do laço do checker (ele acorda de 5 em 5 s e
// pega o que venceu). Espelha o ticker de sitecheck.Checker.Run: um check nunca é
// sondado no instante exato em que vence, e essa folga é estrutural.
const passoDoAgendador = 5 * time.Second

// MakeUptimeStat monta o UptimeStat a partir das contagens cruas. Função pura
// (sem banco) para o cálculo de cobertura poder ser testado direto.
//
// `earliest` é a sondagem mais antiga retida do check: sem ela, um check criado
// hoje mostraria 3% de cobertura em 30 dias e seria declarado insuficiente para
// sempre. O período esperado é a interseção entre a janela pedida e o tempo em
// que o check de fato existiu.
func MakeUptimeStat(c UptimeCount, earliest, since, now time.Time, tier string) UptimeStat {
	st := UptimeStat{Samples: c.Samples, Since: earliest}
	if c.SegTotal > 0 {
		st.Percent = c.SegOK / c.SegTotal * 100
	} else if c.Samples > 0 {
		st.Percent = float64(c.OK) / float64(c.Samples) * 100
	}
	if earliest.IsZero() {
		return st // nunca sondou: cobertura 0, insuficiente. Nada a afirmar.
	}
	st.WindowFull = !earliest.After(since)
	inicio := since
	if earliest.After(inicio) {
		inicio = earliest
	}
	// A CADÊNCIA REAL, e não a nominal.
	//
	// O agendador faz `próxima = agora + intervalo` DEPOIS de sondar, e o laço acorda
	// a cada 5 s. Então o período de verdade é `intervalo + duração + granularidade`,
	// nunca `intervalo` — o denominador nominal é inatingível por construção, e a
	// cobertura tinha teto estrutural: medido em dev, mediana de 65,0 s num tier de
	// 60 s (máximo possível 92%).
	//
	// Isso não era detalhe acadêmico: um alvo que estoura o tempo limite a cada ciclo
	// gasta 20 s por sondagem, o que derrubava a cobertura para ~70% e cruzava o piso
	// de MinUptimeCoverage. Resultado perverso — o site MAIS quebrado do parque, o
	// único que consome o timeout inteiro, era exatamente aquele para o qual o painel
	// parava de publicar o uptime e escrevia "sem dados suficientes", quando as
	// amostras existiam e todas diziam 0%. Deixar de acusar quem deveria ser acusado.
	intervalo := TierInterval(tier) + passoDoAgendador +
		time.Duration(c.AvgMs)*time.Millisecond
	esperadas := int(now.Sub(inicio) / intervalo)
	if esperadas < 1 {
		esperadas = 1
	}
	st.Expected = esperadas
	st.Coverage = float64(c.Samples) / float64(esperadas) * 100
	// Com pesos, a cobertura é TEMPO medido ÷ tempo do período: a contagem de
	// amostras, com retestes de 30 s, chegaria a 100% com o dia inteiro sem medição
	// fora das quedas.
	if periodo := now.Sub(inicio).Seconds(); c.SegTotal > 0 && periodo > 0 {
		st.Coverage = c.SegTotal / periodo * 100
	}
	if st.Coverage > 100 {
		st.Coverage = 100
	}
	st.Sufficient = c.Samples > 0 && st.Coverage >= MinUptimeCoverage
	return st
}

// UptimeCountBatch conta sondagens/ok de TODOS os checks desde `since` numa só
// query. Checks sem sondagem no período ficam de fora do mapa (e, com o cálculo
// de cobertura, isso vira "sem dados", não 100%).
func (s *Store) UptimeCountBatch(ctx context.Context, since time.Time) (map[int64]UptimeCount, error) {
	rows, err := s.pool.Query(ctx, uptimeSQL(""), since, diagsNaoMedidos)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]UptimeCount{}
	for rows.Next() {
		var id int64
		var c UptimeCount
		if err := rows.Scan(&id, &c.Samples, &c.OK, &c.AvgMs, &c.SegTotal, &c.SegOK); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}

// EarliestBatch devolve a sondagem mais antiga retida de cada check (numa só
// query), para o cálculo de cobertura saber desde quando havia o que medir.
func (s *Store) EarliestBatch(ctx context.Context) (map[int64]time.Time, error) {
	rows, err := s.pool.Query(ctx, `SELECT check_id, min(ts) FROM site_check_results GROUP BY check_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]time.Time{}
	for rows.Next() {
		var id int64
		var ts time.Time
		if err := rows.Scan(&id, &ts); err != nil {
			return nil, err
		}
		out[id] = ts
	}
	return out, rows.Err()
}

// ChildStates resume o estado das páginas-filhas de um sitemap.
type ChildStates struct {
	Total    int `json:"total"`
	Down     int `json:"down"`
	Degraded int `json:"degraded"`
}

// Up é quantas páginas estão no ar.
func (c ChildStates) Up() int { return c.Total - c.Down - c.Degraded }

// ChildStatesBatch resume os filhos de TODOS os sitemaps numa só query — o pai
// não tem uptime próprio (não sonda nada), então a tela mostra "N/M páginas no
// ar" no lugar do percentual.
func (s *Store) ChildStatesBatch(ctx context.Context) (map[int64]ChildStates, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT parent_id, count(*),
		       count(*) FILTER (WHERE state='DOWN'),
		       count(*) FILTER (WHERE state IN ('DEGRADADO','SUSPEITO'))
		FROM site_checks WHERE parent_id IS NOT NULL GROUP BY parent_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]ChildStates{}
	for rows.Next() {
		var id int64
		var c ChildStates
		if err := rows.Scan(&id, &c.Total, &c.Down, &c.Degraded); err != nil {
			return nil, err
		}
		out[id] = c
	}
	return out, rows.Err()
}

// MinChildCertDaysLeft devolve o MENOR cert_days_left entre as leituras mais
// recentes das páginas-filhas de um sitemap.
//
// Sem isto, um sitemap inteiro nunca avisava que o certificado ia vencer: o pai
// não sonda (cert_days_left = 0) e todo filho nasce com alerting=false, que era o
// primeiro `return` de certExpiry. O aviso de expiração é a coisa mais útil que a
// sonda faz — o pai passa a herdar o pior caso dos filhos.
func (s *Store) MinChildCertDaysLeft(ctx context.Context, parentID int64) (int, bool, error) {
	var dias *int
	err := s.pool.QueryRow(ctx, `
		SELECT min(d) FROM (
			SELECT DISTINCT ON (r.check_id) r.cert_days_left AS d
			FROM site_check_results r
			JOIN site_checks c ON c.id = r.check_id
			WHERE c.parent_id = $1 AND COALESCE(r.cert_days_left,0) > 0
			ORDER BY r.check_id, r.ts DESC
		) t`, parentID).Scan(&dias)
	if err != nil {
		return 0, false, err
	}
	if dias == nil {
		return 0, false, nil
	}
	return *dias, true, nil
}

// LastLatencyBatch devolve a última latência total (ms) de cada check numa só query.
func (s *Store) LastLatencyBatch(ctx context.Context) (map[int64]float64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (check_id) check_id, COALESCE(total_ms,0)
		FROM site_check_results ORDER BY check_id, ts DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]float64{}
	for rows.Next() {
		var id int64
		var ms float64
		if err := rows.Scan(&id, &ms); err != nil {
			return nil, err
		}
		out[id] = ms
	}
	return out, rows.Err()
}

// UptimeSince calcula o uptime de UM check desde `since`, já com a cobertura.
//
// Substitui a antiga UptimePercent, cujo `if total == 0 { return 100, 0, nil }`
// era o zero silencioso invertido: nenhuma medição devolvia disponibilidade
// máxima. Aqui, nenhuma medição devolve Sufficient=false e Coverage=0.
func (s *Store) UptimeSince(ctx context.Context, checkID int64, since time.Time, tier string) (UptimeStat, error) {
	var c UptimeCount
	var id int64
	err := s.pool.QueryRow(ctx, uptimeSQL("AND r.check_id = $3"), since, diagsNaoMedidos, checkID).
		Scan(&id, &c.Samples, &c.OK, &c.AvgMs, &c.SegTotal, &c.SegOK)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return UptimeStat{}, err
	}
	earliest, _, err := s.SiteCheckEarliest(ctx, checkID)
	if err != nil {
		return UptimeStat{}, err
	}
	return MakeUptimeStat(c, earliest, since, time.Now(), tier), nil
}
