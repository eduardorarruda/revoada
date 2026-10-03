package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Agent é uma chave de ingestão (serverkey) de um agente. A tabela `agents` é
// criada e consumida pelo gateway (autenticação da ingestão); aqui o server só
// a gerencia (listar/gerar/revogar) para a tela de Chaves de agente.
type Agent struct {
	Serverkey string     `json:"serverkey"`
	TenantID  string     `json:"tenant_id"`
	Hostname  string     `json:"hostname"`
	Revoked   bool       `json:"revoked"`
	LastSeen  *time.Time `json:"last_seen"`
	CreatedAt time.Time  `json:"created_at"`
}

// AgentID é o identificador PÚBLICO de uma serverkey: os 16 primeiros hex do
// sha256 da chave. Existe porque a listagem de chaves passou a vir MASCARADA (uma
// sessão de admin comprometida colhia a frota inteira em claro num GET), e as telas
// ainda precisam apontar para uma chave específica — revogar, apagar, segurar a
// atualização, baixar o instalador. É derivado, não guardado: nenhuma migração, e
// continua valendo para as chaves que já existem.
func AgentID(serverkey string) string {
	sum := sha256.Sum256([]byte(serverkey))
	return hex.EncodeToString(sum[:8])
}

// ListAgents devolve todas as chaves, mais novas primeiro.
func (s *Store) ListAgents(ctx context.Context) ([]Agent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT serverkey, tenant_id, COALESCE(hostname,''), revoked, last_seen, created_at
		FROM agents ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agent
	for rows.Next() {
		var a Agent
		if err := rows.Scan(&a.Serverkey, &a.TenantID, &a.Hostname, &a.Revoked, &a.LastSeen, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateAgent cadastra uma nova serverkey (não revogada) para um host.
func (s *Store) CreateAgent(ctx context.Context, serverkey, tenant, hostname string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO agents (serverkey, tenant_id, hostname, revoked)
		VALUES ($1,$2,$3,FALSE)`, serverkey, tenant, hostname)
	return err
}

// SetAgentHostname corrige o hostname vinculado a uma chave para o hostname REAL da
// máquina (conhecido só após o install). Sem isto, agents.hostname fica no rótulo
// cosmético (nome amigável) da criação — e a amarração de host (anti-spoofing) não
// pode ser ligada com segurança. Best-effort: hostname vazio é ignorado.
func (s *Store) SetAgentHostname(ctx context.Context, serverkey, hostname string) error {
	if hostname == "" {
		return nil
	}
	_, err := s.pool.Exec(ctx, `UPDATE agents SET hostname=$2 WHERE serverkey=$1`, serverkey, hostname)
	return err
}

// SetAgentRevoked liga/desliga a revogação de uma chave.
func (s *Store) SetAgentRevoked(ctx context.Context, serverkey string, revoked bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	ct, err := tx.Exec(ctx, `UPDATE agents SET revoked=$2 WHERE serverkey=$1`, serverkey, revoked)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	// DESREVOGAR CANCELA A ORDEM DE AUTO-DESINSTALAÇÃO.
	//
	// Era o único desfazer que o painel oferecia e ele não desfazia nada: apagar o
	// servidor errado revoga a chave E registra a ordem; reativar a chave dois
	// minutos depois devolvia a ingestão, mas a ordem continuava lá, e o desvio da
	// consulta de update responde `desinstalar: true` mesmo para chave ATIVA. O
	// agente se removia na consulta seguinte — por até sete dias depois do "desfiz".
	if !revoked {
		if _, err := tx.Exec(ctx, `DELETE FROM agent_uninstalls WHERE serverkey = $1`, serverkey); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// AgentKeyActive diz se a serverkey existe e não está revogada — usada pelo
// reprovisionamento (Fase G) para não reinstalar um agente com chave morta.
func (s *Store) AgentKeyActive(ctx context.Context, serverkey string) (bool, error) {
	var revoked bool
	err := s.pool.QueryRow(ctx, `SELECT revoked FROM agents WHERE serverkey=$1`, serverkey).Scan(&revoked)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil
		}
		return false, err
	}
	return !revoked, nil
}

// DeleteAgent apaga a chave de vez.
func (s *Store) DeleteAgent(ctx context.Context, serverkey string) error {
	ct, err := s.pool.Exec(ctx, `DELETE FROM agents WHERE serverkey=$1`, serverkey)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
