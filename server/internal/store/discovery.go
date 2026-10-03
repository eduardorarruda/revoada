package store

import (
	"context"
	"time"
)

// HostService é um serviço descoberto num host (P6.2, gravado pelo gateway).
type HostService struct {
	Hostname     string    `json:"hostname"`
	Kind         string    `json:"kind"`
	Detail       string    `json:"detail"`
	Source       string    `json:"source"`
	DiscoveredAt time.Time `json:"discovered_at"`
}

// ListHostServices lista os serviços descobertos (mais recentes primeiro).
func (s *Store) ListHostServices(ctx context.Context) ([]HostService, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT hostname, kind, COALESCE(detail,''), COALESCE(source,''), discovered_at
		FROM host_services ORDER BY hostname, kind`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []HostService
	for rows.Next() {
		var h HostService
		if err := rows.Scan(&h.Hostname, &h.Kind, &h.Detail, &h.Source, &h.DiscoveredAt); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// ProbeResult é o resultado recente de uma sonda para uma URL (P6.3).
type ProbeResult struct {
	Location   string    `json:"location"`
	Up         bool      `json:"up"`
	TotalMs    float64   `json:"total_ms"`
	Diagnostic string    `json:"diagnostic"`
	ReportedAt time.Time `json:"reported_at"`
	// Status é o código HTTP visto pela sonda remota (0 = não houve resposta).
	Status int `json:"status"`
	// Truncated: a sonda cortou o corpo no teto de leitura, então `total_ms` é um
	// PISO e a asserção de palavra-chave é indeterminada. Um reporte truncado que
	// falhou NÃO vota como falha no consenso — ver checker.votoDeFalha.
	Truncated bool `json:"truncated"`
}

// FreshProbeResults devolve os reportes de sonda de uma URL, do tenant dado,
// vindos das localizações designadas e reportados após `since`.
//
// Por que tenant E localizações (o defeito bloqueante que isto corrige):
//
// A consulta era `WHERE url=$1 AND reported_at >= $2`, sem mais nada. Qualquer
// linha fresca de probe_results para aquela URL virava voto — de qualquer tenant
// e de qualquer localização, inclusive de nomes que ninguém designou. Provado por
// exploração: com UMA ÚNICA serverkey foram enviados dois reportes de localidades
// inventadas (`caos-fake-tokyo`, `caos-fake-berlim`) contra um check de ORIGEM
// ÚNICA (probe_locations=[]) que a sonda central via no ar, e o painel gravou
// `9|DOWN|down em 2 sonda(s): caos-fake-berlim, caos-fake-tokyo`. Só não paginou
// porque o check de teste tinha alerting=false. Também medido: dois checks com
// configurações diferentes que compartilham `https://example.com/` votavam um no
// consenso do outro.
//
// `locations` vazio devolve lista vazia SEM ir ao banco: um check sem sondas
// designadas é de origem única por definição e não tem eleitorado nenhum — é
// exatamente o caso que a exploração forjou.
func (s *Store) FreshProbeResults(ctx context.Context, tenantID, url string, locations []string, since time.Time) ([]ProbeResult, error) {
	if len(locations) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT probe_location, up, total_ms, COALESCE(diagnostic,''), reported_at,
		       COALESCE(status,0), COALESCE(truncated,false)
		FROM probe_results
		WHERE tenant_id=$1 AND url=$2 AND reported_at >= $3 AND probe_location = ANY($4::text[])
		ORDER BY probe_location`, orStr(tenantID, "default"), url, since, locations)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProbeResult
	for rows.Next() {
		var p ProbeResult
		if err := rows.Scan(&p.Location, &p.Up, &p.TotalMs, &p.Diagnostic, &p.ReportedAt,
			&p.Status, &p.Truncated); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// LocationHealth resume como uma localização de sonda está se saindo com TODOS os
// alvos dela na janela.
type LocationHealth struct {
	Targets int // alvos com reporte fresco desta localização
	Failing int // quantos deles a localização declarou fora do ar
}

// MinTargetsToDisqualify é o mínimo de alvos para uma localização poder ser
// desqualificada por "falhou em todos". Com um alvo só, "falhou em todos" é
// simplesmente "o alvo caiu" — não há como distinguir.
const MinTargetsToDisqualify = 2

// SuspectProbeLocations devolve as localizações que estão reportando falha para
// TODOS os alvos delas na janela — o sinal de que quem quebrou foi a sonda, não o
// parque inteiro.
//
// Por que existe: o consenso lia só o booleano `up` de cada reporte. Uma sonda que
// perdeu a rota (ou caiu num DNS quebrado, ou está atrás de um firewall novo)
// passa a declarar TUDO fora do ar; com duas sondas assim, ou uma somada a
// qualquer oscilação, o painel derruba e notifica o parque inteiro de uma vez.
// Falha correlacionada em 100% dos alvos é a assinatura da sonda doente: o voto
// dela é descartado e a localização aparece como suspeita, não como verdade.
func (s *Store) SuspectProbeLocations(ctx context.Context, tenantID string, since time.Time) (map[string]LocationHealth, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT probe_location, count(*), count(*) FILTER (WHERE NOT up)
		FROM probe_results
		WHERE tenant_id=$1 AND reported_at >= $2
		GROUP BY probe_location`, orStr(tenantID, "default"), since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]LocationHealth{}
	for rows.Next() {
		var loc string
		var h LocationHealth
		if err := rows.Scan(&loc, &h.Targets, &h.Failing); err != nil {
			return nil, err
		}
		if IsSuspectLocation(h) {
			out[loc] = h
		}
	}
	return out, rows.Err()
}

// IsSuspectLocation aplica a regra de desqualificação (função pura, testável sem
// banco): falhou em TODOS os alvos e tinha alvos suficientes para isso significar
// algo.
func IsSuspectLocation(h LocationHealth) bool {
	return h.Targets >= MinTargetsToDisqualify && h.Failing == h.Targets
}

// DashboardExists diz se um dashboard com o uid já existe (para sugestões).
func (s *Store) DashboardExists(ctx context.Context, uid string) bool {
	var one int
	err := s.pool.QueryRow(ctx, `SELECT 1 FROM dashboards WHERE uid=$1`, uid).Scan(&one)
	return err == nil
}
