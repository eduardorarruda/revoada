package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ProvisionTarget é um alvo de onboarding SSH (Fase G). SecretBlob é a credencial
// SSH CIFRADA (base64 do blob secretbox nonce||ciphertext) — nunca a credencial em
// claro. Nenhum campo em claro da credencial existe aqui; o decrypt só ocorre em
// memória, no momento de (re)provisionar, no pacote internal/provision.
type ProvisionTarget struct {
	ID                int64      `json:"id"`
	TenantID          string     `json:"tenant_id"`
	Name              string     `json:"name"`
	Host              string     `json:"host"`
	Hostname          string     `json:"hostname"` // hostname REAL da máquina (capturado por SSH); casa com `hosts`
	SSHPort           int        `json:"ssh_port"`
	SSHUser           string     `json:"ssh_user"`
	AuthType          string     `json:"auth_type"`
	SecretBlob        string     `json:"-"` // NUNCA serializado para o cliente
	HostKeyFP         string     `json:"host_key_fp"`
	Serverkey         string     `json:"serverkey"`
	Status            string     `json:"status"`
	LastProvisionedAt *time.Time `json:"last_provisioned_at"`
	CreatedAt         time.Time  `json:"created_at"`
}

// CreateProvisionTarget insere um novo alvo e devolve o id gerado.
func (s *Store) CreateProvisionTarget(ctx context.Context, t ProvisionTarget) (int64, error) {
	if t.TenantID == "" {
		t.TenantID = "default"
	}
	if t.SSHPort == 0 {
		t.SSHPort = 22
	}
	if t.Status == "" {
		t.Status = "pending"
	}
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO provision_targets
			(tenant_id, name, host, hostname, ssh_port, ssh_user, auth_type, secret_blob, host_key_fp, serverkey, status, last_provisioned_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		RETURNING id`,
		t.TenantID, t.Name, t.Host, nullStr(t.Hostname), t.SSHPort, t.SSHUser, t.AuthType, t.SecretBlob,
		nullStr(t.HostKeyFP), nullStr(t.Serverkey), t.Status, t.LastProvisionedAt,
	).Scan(&id)
	return id, err
}

// UpsertProvisionTarget deduplica por (tenant_id, host): se já existe um alvo para
// aquele host, ATUALIZA-o (name, hostname, credencial, host key, serverkey, status,
// carimbo); senão, cria um novo. Assim o (re)provisionamento não acumula alvos
// duplicados do mesmo host, e o hostname REAL captado por SSH sempre acaba gravado.
func (s *Store) UpsertProvisionTarget(ctx context.Context, t ProvisionTarget) (int64, error) {
	if t.TenantID == "" {
		t.TenantID = "default"
	}
	if t.SSHPort == 0 {
		t.SSHPort = 22
	}
	if t.Status == "" {
		t.Status = "pending"
	}
	var id int64
	err := s.pool.QueryRow(ctx,
		`SELECT id FROM provision_targets WHERE tenant_id=$1 AND host=$2 ORDER BY id LIMIT 1`,
		t.TenantID, t.Host,
	).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return s.CreateProvisionTarget(ctx, t)
	}
	if err != nil {
		return 0, err
	}
	_, err = s.pool.Exec(ctx, `
		UPDATE provision_targets
		SET name=$2, hostname=$3, ssh_port=$4, ssh_user=$5, auth_type=$6, secret_blob=$7,
		    host_key_fp=$8, serverkey=$9, status=$10, last_provisioned_at=$11
		WHERE id=$1`,
		id, t.Name, nullStr(t.Hostname), t.SSHPort, t.SSHUser, t.AuthType, t.SecretBlob,
		nullStr(t.HostKeyFP), nullStr(t.Serverkey), t.Status, t.LastProvisionedAt,
	)
	if err != nil {
		return 0, err
	}
	return id, nil
}

// GetProvisionTarget devolve um alvo por id (inclui secret_blob cifrado, para
// decrypt em memória no reprovisionamento — o handler nunca o expõe ao cliente).
func (s *Store) GetProvisionTarget(ctx context.Context, id int64) (ProvisionTarget, error) {
	var t ProvisionTarget
	var fp, sk, hn *string
	err := s.pool.QueryRow(ctx, `
		SELECT id, tenant_id, name, host, hostname, ssh_port, ssh_user, auth_type, secret_blob,
		       host_key_fp, serverkey, status, last_provisioned_at, created_at
		FROM provision_targets WHERE id=$1`, id,
	).Scan(&t.ID, &t.TenantID, &t.Name, &t.Host, &hn, &t.SSHPort, &t.SSHUser, &t.AuthType, &t.SecretBlob,
		&fp, &sk, &t.Status, &t.LastProvisionedAt, &t.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	if err != nil {
		return t, err
	}
	if hn != nil {
		t.Hostname = *hn
	}
	if fp != nil {
		t.HostKeyFP = *fp
	}
	if sk != nil {
		t.Serverkey = *sk
	}
	return t, nil
}

// ListProvisionTargets devolve os alvos, mais novos primeiro. secret_blob vem junto
// mas o handler o descarta antes de responder (a struct tem json:"-").
func (s *Store) ListProvisionTargets(ctx context.Context) ([]ProvisionTarget, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, name, host, hostname, ssh_port, ssh_user, auth_type, secret_blob,
		       host_key_fp, serverkey, status, last_provisioned_at, created_at
		FROM provision_targets ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProvisionTarget
	for rows.Next() {
		var t ProvisionTarget
		var fp, sk, hn *string
		if err := rows.Scan(&t.ID, &t.TenantID, &t.Name, &t.Host, &hn, &t.SSHPort, &t.SSHUser, &t.AuthType, &t.SecretBlob,
			&fp, &sk, &t.Status, &t.LastProvisionedAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		if hn != nil {
			t.Hostname = *hn
		}
		if fp != nil {
			t.HostKeyFP = *fp
		}
		if sk != nil {
			t.Serverkey = *sk
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ProvisionTargetsByHostname devolve todos os alvos cujo hostname REAL bate com o
// informado (o mais recente primeiro). Inclui secret_blob e serverkey — é usado
// pela exclusão de host para achar a credencial guardada e purgar as serverkeys
// mesmo sem o id do alvo vindo do frontend. hostname vazio => nada (nil, nil).
func (s *Store) ProvisionTargetsByHostname(ctx context.Context, tenant, hostname string) ([]ProvisionTarget, error) {
	if hostname == "" {
		return nil, nil
	}
	if tenant == "" {
		tenant = "default"
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, tenant_id, name, host, hostname, ssh_port, ssh_user, auth_type, secret_blob,
		       host_key_fp, serverkey, status, last_provisioned_at, created_at
		FROM provision_targets WHERE tenant_id=$1 AND hostname=$2 ORDER BY created_at DESC`, tenant, hostname)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ProvisionTarget
	for rows.Next() {
		var t ProvisionTarget
		var fp, sk, hn *string
		if err := rows.Scan(&t.ID, &t.TenantID, &t.Name, &t.Host, &hn, &t.SSHPort, &t.SSHUser, &t.AuthType, &t.SecretBlob,
			&fp, &sk, &t.Status, &t.LastProvisionedAt, &t.CreatedAt); err != nil {
			return nil, err
		}
		if hn != nil {
			t.Hostname = *hn
		}
		if fp != nil {
			t.HostKeyFP = *fp
		}
		if sk != nil {
			t.Serverkey = *sk
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteProvisionTargetsByHostname apaga TODOS os alvos daquele hostname REAL (sem
// deixar órfãos na exclusão de host). Devolve quantas linhas foram apagadas.
// hostname vazio => no-op (0, nil), para nunca varrer a tabela inteira.
func (s *Store) DeleteProvisionTargetsByHostname(ctx context.Context, tenant, hostname string) (int64, error) {
	if hostname == "" {
		return 0, nil
	}
	if tenant == "" {
		tenant = "default"
	}
	ct, err := s.pool.Exec(ctx,
		`DELETE FROM provision_targets WHERE tenant_id=$1 AND hostname=$2`, tenant, hostname)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// UpdateProvisionTargetAfterProvision grava o resultado de um (re)provisionamento
// bem-sucedido: host key pinado, serverkey associada, status e carimbo de tempo.
func (s *Store) UpdateProvisionTargetAfterProvision(ctx context.Context, id int64, hostKeyFP, serverkey, status string) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE provision_targets
		SET host_key_fp=$2, serverkey=$3, status=$4, last_provisioned_at=now()
		WHERE id=$1`, id, nullStr(hostKeyFP), nullStr(serverkey), status)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
