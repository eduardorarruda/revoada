package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Este arquivo reúne as remoções POR HOST (tenant_id + hostname), usadas pela
// feature "Apagar servidor" (pacote internal/hostadmin). Cada função apaga só o
// recorte daquele host — nunca a linha default/global.

// DeleteHost apaga a linha de inventário do host. A tabela `hosts` é criada pelo
// gateway, mas mora no mesmo Postgres — o server pode removê-la.
func (s *Store) DeleteHost(ctx context.Context, tenant, hostname string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM hosts WHERE tenant_id=$1 AND hostname=$2`, tenant, hostname)
	return err
}

// SetHostDisplayName grava o alias amigável do host na tabela `hosts`, sobrevivendo
// ao gateway: a PK é (tenant_id, hostname) e o UpsertHost do gateway NÃO toca
// display_name, então o alias definido aqui é preservado nos upserts de inventário.
// No-op quando hostname ou displayName são vazios (só grava quando há nome amigável).
func (s *Store) SetHostDisplayName(ctx context.Context, tenant, hostname, displayName string) error {
	if hostname == "" || displayName == "" {
		return nil
	}
	if tenant == "" {
		tenant = "default"
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO hosts (tenant_id, hostname, display_name)
		VALUES ($1,$2,$3)
		ON CONFLICT (tenant_id, hostname) DO UPDATE SET display_name = EXCLUDED.display_name`,
		tenant, hostname, displayName)
	return err
}

// DeleteHostServices apaga os serviços descobertos do host.
func (s *Store) DeleteHostServices(ctx context.Context, tenant, hostname string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM host_services WHERE tenant_id=$1 AND hostname=$2`, tenant, hostname)
	return err
}

// DeleteHostThresholds apaga SÓ os overrides de limiar do host. Nunca apaga a linha
// default global (hostname=”): o chamador garante hostname != "".
func (s *Store) DeleteHostThresholds(ctx context.Context, tenant, hostname string) error {
	if hostname == "" {
		return nil // proteção extra: '' é o default global
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM host_thresholds WHERE tenant_id=$1 AND hostname=$2`, tenant, hostname)
	return err
}

// DeleteAgentsByHost apaga as serverkeys cujo hostname bate com algum dos nomes do
// host (nome técnico e/ou apelido). Corta a ingestão para o host não "reaparecer".
// Devolve quantas chaves foram apagadas.
func (s *Store) DeleteAgentsByHost(ctx context.Context, tenant string, names []string) (int64, error) {
	if len(names) == 0 {
		return 0, nil
	}
	ct, err := s.pool.Exec(ctx,
		`DELETE FROM agents WHERE tenant_id=$1 AND hostname = ANY($2::text[])`, tenant, names)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// AgentKeysByHost lista as serverkeys de um host (nome técnico e/ou apelido). É o
// espelho de DeleteAgentsByHost: quando a exclusão vai pedir ao agente que se
// desinstale, a chave não pode ser apagada — ela é o crachá com que ele volta para
// receber a ordem.
func (s *Store) AgentKeysByHost(ctx context.Context, tenant string, names []string) ([]string, error) {
	if len(names) == 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT serverkey FROM agents WHERE tenant_id=$1 AND hostname = ANY($2::text[])`, tenant, names)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

// DeleteProvisionTarget apaga o alvo de provisionamento SSH (após desinstalar o
// agente). Idempotente: 0 linhas = já não existia.
func (s *Store) DeleteProvisionTarget(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM provision_targets WHERE id=$1`, id)
	return err
}

// DeleteHostPerms apaga as permissões DIRETAS (usuário → servidor) daquele hostname.
//
// Sem isto, a permissão sobrevivia ao servidor: `user_server_perms` tem chave
// (user_id, hostname) e ninguém a limpava. Um hostname que volta — reinstalação da
// mesma máquina, ou outra batizada igual — reativava sozinho quem podia ver, editar
// e receber alerta dele, sem ninguém decidir isso. Medido em produção antes do
// conserto: uma linha órfã de `DESKTOP-177OUGM`, host que não existe mais.
func (s *Store) DeleteHostPerms(ctx context.Context, hostname string) (int64, error) {
	if hostname == "" {
		return 0, nil
	}
	ct, err := s.pool.Exec(ctx, `DELETE FROM user_server_perms WHERE hostname=$1`, hostname)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// DeleteHostFromGroups tira o hostname de todos os grupos de servidores. Mesma razão
// de DeleteHostPerms: a permissão herdada do grupo também voltaria com o nome.
func (s *Store) DeleteHostFromGroups(ctx context.Context, hostname string) (int64, error) {
	if hostname == "" {
		return 0, nil
	}
	ct, err := s.pool.Exec(ctx, `DELETE FROM server_group_hosts WHERE hostname=$1`, hostname)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// DeleteOrphanAgentUpdatePolicies apaga políticas de atualização cuja serverkey não
// existe mais. É por varredura, e não por chave: a exclusão do host remove as chaves
// em mais de um caminho (por hostname e pelos alvos de provisionamento), e uma lista
// montada à mão esqueceria justamente a que veio pelo caminho menos óbvio.
func (s *Store) DeleteOrphanAgentUpdatePolicies(ctx context.Context) (int64, error) {
	ct, err := s.pool.Exec(ctx,
		`DELETE FROM agent_update_policy p WHERE NOT EXISTS (SELECT 1 FROM agents a WHERE a.serverkey = p.serverkey)`)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// HostExists diz se a linha de inventário do host existe. Usado pela varredura de
// rescaldo para saber se o host RESSUSCITOU depois de apagado.
func (s *Store) HostExists(ctx context.Context, tenant, hostname string) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM hosts WHERE tenant_id=$1 AND hostname=$2`, tenant, hostname).Scan(&n)
	return n > 0, err
}

// settingHostSweeps é a chave em app_settings com as varreduras de rescaldo ainda
// devidas. Fica no banco, e não só em memória, porque o servidor pode reiniciar
// dentro da janela (um deploy, por exemplo) — e aí a limpeza pendente se perderia
// justamente no minuto em que ela é necessária.
const settingHostSweeps = "hosts.sweep.pending"

// PendingHostSweep é um host apagado que ainda precisa de uma segunda varredura.
type PendingHostSweep struct {
	Hostname string    `json:"host"`
	DueAt    time.Time `json:"em"`
}

// ListPendingHostSweeps devolve as varreduras devidas (pode estar vazia).
func (s *Store) ListPendingHostSweeps(ctx context.Context) ([]PendingHostSweep, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, settingHostSweeps).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var v struct {
		Itens []PendingHostSweep `json:"itens"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, fmt.Errorf("varreduras de rescaldo ilegíveis em app_settings/%s: %w", settingHostSweeps, err)
	}
	return v.Itens, nil
}

// AddPendingHostSweep registra (ou renova) a varredura devida de um host.
func (s *Store) AddPendingHostSweep(ctx context.Context, hostname string, dueAt time.Time) error {
	return s.mexerNasVarreduras(ctx, func(atuais []PendingHostSweep) []PendingHostSweep {
		novo := semOHost(atuais, hostname)
		return append(novo, PendingHostSweep{Hostname: hostname, DueAt: dueAt})
	})
}

// ClearPendingHostSweep tira o host da lista de devidas (varredura concluída).
func (s *Store) ClearPendingHostSweep(ctx context.Context, hostname string) error {
	return s.mexerNasVarreduras(ctx, func(atuais []PendingHostSweep) []PendingHostSweep {
		return semOHost(atuais, hostname)
	})
}

func semOHost(itens []PendingHostSweep, hostname string) []PendingHostSweep {
	novo := make([]PendingHostSweep, 0, len(itens)+1)
	for _, p := range itens {
		if p.Hostname != hostname {
			novo = append(novo, p)
		}
	}
	return novo
}

// mexerNasVarreduras faz ler-alterar-gravar da lista SOB TRAVA.
//
// A lista inteira mora num único registro JSON de app_settings, então duas mexidas
// concorrentes sem trava viram "a última gravação vence": provado contra o Postgres
// com três remoções simultâneas de A, B e C — sobraram A e B, ou seja, duas
// varreduras dadas por concluídas voltaram à lista. Nas duas direções o estrago é
// real: uma varredura PERDIDA deixa para sempre no inventário um servidor que
// ressuscitou pela janela do cache do gateway, e uma varredura RESSUSCITADA volta a
// rodar a cada boot, para sempre, contra um host que já não existe. E o caso
// concorrente é o normal: ResumeSweeps dispara uma goroutine por host pendente, e
// todas terminam chamando Clear.
//
// `SELECT … FOR UPDATE` só trava se a linha existir; o INSERT de baixo garante que
// ela exista a partir da primeira gravação, e a corrida do primeiro INSERT é
// resolvida pelo próprio ON CONFLICT.
func (s *Store) mexerNasVarreduras(ctx context.Context, muda func([]PendingHostSweep) []PendingHostSweep) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var raw []byte
	err = tx.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1 FOR UPDATE`, settingHostSweeps).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	var atuais []PendingHostSweep
	if len(raw) > 0 {
		var v struct {
			Itens []PendingHostSweep `json:"itens"`
		}
		// Valor ilegível NÃO vira lista vazia em silêncio: apagar todas as varreduras
		// pendentes porque um JSON quebrou é exatamente o tipo de perda muda que esta
		// tabela existe para evitar.
		if err := json.Unmarshal(raw, &v); err != nil {
			return fmt.Errorf("varreduras de rescaldo ilegíveis em app_settings/%s: %w", settingHostSweeps, err)
		}
		atuais = v.Itens
	}

	val, err := json.Marshal(struct {
		Itens []PendingHostSweep `json:"itens"`
	}{Itens: muda(atuais)})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO app_settings (key, value, updated_at) VALUES ($1,$2,now())
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`,
		settingHostSweeps, val); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RemoveHostFromAlertRules tira o hostname do escopo (array `hosts`) de toda regra
// que o continha. Se a regra ficar com escopo VAZIO por causa disso (só tinha este
// host), ela é DESATIVADA (enabled=false) — senão viraria global (escopo vazio =
// todos os hosts) sem intenção. Devolve quantas regras foram afetadas.
func (s *Store) RemoveHostFromAlertRules(ctx context.Context, hostname string) (int64, error) {
	ct, err := s.pool.Exec(ctx, `
		UPDATE alert_rules
		SET hosts = COALESCE(
			(SELECT jsonb_agg(x) FROM jsonb_array_elements_text(hosts) x WHERE x <> $1),
			'[]'::jsonb),
		    enabled = CASE
			WHEN (SELECT count(*) FROM jsonb_array_elements_text(hosts) x WHERE x <> $1) = 0
			THEN FALSE ELSE enabled END
		WHERE hosts @> to_jsonb($1::text)`, hostname)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}

// CloseAlertsOfHost encerra, em silêncio, os alertas ABERTOS de um servidor apagado.
//
// Sem isto o alerta fica órfão e vira uma mentira com prazo: a regra continua existindo
// e habilitada (só o hostname sai do escopo dela), então o avaliador não a trata como
// removida — ele para de receber dado daquele host e, passada a carência de ausência
// (max(90 s, 3× a janela da regra), ou seja 15 min na janela padrão), publica "📡 SEM
// DADOS" para um servidor que o próprio operador apagou. Até lá, a tela de Alertas
// mostra um incidente ativo cujo link leva a um host que não existe mais.
//
// Encerrar aqui é silencioso de propósito: não houve mudança no mundo monitorado, houve
// remoção do que se monitorava. `resolved_by` guarda o motivo para a auditoria não ler
// isso como alguém fechando alerta à mão.
//
// O casamento é pelo rótulo `host` — o mesmo que o avaliador usa para montar o
// fingerprint, e o mesmo que a tela usa no deep link.
func (s *Store) CloseAlertsOfHost(ctx context.Context, hostname string) (int64, error) {
	ct, err := s.pool.Exec(ctx, `
		UPDATE alert_events SET state='resolved', ended_at=now(),
		       resolved_by='servidor removido do painel'
		WHERE ended_at IS NULL AND labels->>'host' = $1`, hostname)
	if err != nil {
		return 0, err
	}
	return ct.RowsAffected(), nil
}
