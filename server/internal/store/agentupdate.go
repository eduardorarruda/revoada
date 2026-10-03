package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Persistência do freio da auto-atualização dos agentes.
//
// O problema que isto resolve foi medido no código, não imaginado: o handler de
// atualização (agents.NewUpdateHandler) era construído com FonteDePolitica = nil, e
// `nil` significa "atualize sempre que houver versão nova". Como o deploy do painel é
// automático (git push → o runner publica o binário novo em dist/), o gatilho de uma
// troca de binário em TODA a frota era um push — sem canário, sem pin, sem botão de
// pânico. Se a versão nova quebrasse a coleta num Debian específico, não havia como
// segurar os outros hosts enquanto se investigava.
//
// São dois níveis, de propósito:
//   - GLOBAL (app_settings): desliga a auto-atualização da frota ou fixa uma versão;
//   - POR CHAVE (agent_update_policy): segura UM host, sem parar os demais — o caso
//     do servidor de cliente que não pode ser tocado no horário comercial.

// settingAgentAutoUpdate é a chave singleton da política global.
const settingAgentAutoUpdate = "agent_auto_update"

// AgentUpdateSettings é a política GLOBAL da auto-atualização.
type AgentUpdateSettings struct {
	// Off desliga a auto-atualização de toda a frota.
	Off bool `json:"off"`
	// Pin fixa a frota numa versão ("0.8.5"). Vazio = sem pin.
	Pin string `json:"pin"`
	// Motivo é o texto que o agente recebe (e o painel mostra) quando é segurado.
	// Existe porque "não atualizou" sem motivo é indistinguível de "o atualizador
	// está quebrado" — e essa confusão custa um chamado.
	Motivo string `json:"motivo"`
	// UpdatedAt/UpdatedBy respondem "quem foi que segurou a frota, e quando".
	UpdatedAt time.Time `json:"updated_at"`
	UpdatedBy string    `json:"updated_by"`
}

// AgentUpdateReport é o último relato de atualização de um agente (o que ele tentou
// e no que deu). Espelha agents.RelatoAgente; fica aqui para o store não depender do
// pacote agents (que já depende do store).
type AgentUpdateReport struct {
	VersaoAtual string `json:"versao_atual"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	// Hostname é o nome que o AGENTE diz ter — o mesmo que ele usa ao enviar
	// métricas, e portanto a única ponte confiável entre uma serverkey e uma linha
	// da tabela `hosts`.
	//
	// Por que isto precisa existir: `agents.hostname` é apelido digitado por gente
	// ao criar a chave ("Loja Exemplo", "Teste traces"), não identidade de máquina;
	// medido em produção, nenhuma das 8 chaves casava com nenhum dos 5 hosts reais.
	// Sem este campo, a tela de atualização não consegue dizer "este freio age
	// NESTE servidor" — e um freio que pode agir no host errado é pior que nenhum.
	//
	// Fica vazio enquanto o agente não preencher `hostname` no POST
	// /api/agent/update-check (a frota 0.7.0 não preenche). Vazio é tratado como
	// "não sei", nunca como "não casa".
	Hostname       string    `json:"hostname,omitempty"`
	Estado         string    `json:"estado,omitempty"`
	Motivo         string    `json:"motivo,omitempty"`
	Erro           string    `json:"erro,omitempty"`
	VersaoDesejada string    `json:"versao_desejada,omitempty"`
	Quando         time.Time `json:"quando,omitempty"`
}

// AgentUpdatePolicy é a política de UMA chave.
type AgentUpdatePolicy struct {
	Hold       bool               `json:"hold"`
	HoldReason string             `json:"hold_reason,omitempty"`
	Report     *AgentUpdateReport `json:"report,omitempty"`
	ReportedAt *time.Time         `json:"reported_at,omitempty"`
}

// GetAgentUpdateSettings devolve a política global. Linha ausente = tudo ligado, sem
// pin (o comportamento que a frota tem hoje) — ler a política nunca inventa um freio
// que ninguém pediu.
func (s *Store) GetAgentUpdateSettings(ctx context.Context) (AgentUpdateSettings, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, settingAgentAutoUpdate).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentUpdateSettings{}, nil
	}
	if err != nil {
		return AgentUpdateSettings{}, err
	}
	var v AgentUpdateSettings
	_ = json.Unmarshal(raw, &v)
	return v, nil
}

// SetAgentUpdateSettings grava (upsert) a política global.
func (s *Store) SetAgentUpdateSettings(ctx context.Context, v AgentUpdateSettings, quem string) error {
	v.Pin = strings.TrimSpace(v.Pin)
	v.Motivo = strings.TrimSpace(v.Motivo)
	v.UpdatedAt = time.Now().UTC()
	v.UpdatedBy = quem
	val, err := json.Marshal(v)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO app_settings (key, value, updated_at) VALUES ($1,$2,now())
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, settingAgentAutoUpdate, val)
	return err
}

// AgentUpdatePolicyFor devolve a política de UMA chave (linha ausente = sem hold).
func (s *Store) AgentUpdatePolicyFor(ctx context.Context, serverkey string) (AgentUpdatePolicy, error) {
	var p AgentUpdatePolicy
	var raw []byte
	err := s.pool.QueryRow(ctx,
		`SELECT hold, hold_reason, report, reported_at FROM agent_update_policy WHERE serverkey=$1`,
		serverkey).Scan(&p.Hold, &p.HoldReason, &raw, &p.ReportedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return AgentUpdatePolicy{}, nil
	}
	if err != nil {
		return AgentUpdatePolicy{}, err
	}
	if len(raw) > 0 {
		var r AgentUpdateReport
		if json.Unmarshal(raw, &r) == nil {
			p.Report = &r
		}
	}
	return p, nil
}

// ListAgentUpdatePolicies devolve as políticas por chave, indexadas pela serverkey.
// A frota tem dezenas de chaves: uma varredura é mais barata que N consultas.
func (s *Store) ListAgentUpdatePolicies(ctx context.Context) (map[string]AgentUpdatePolicy, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT serverkey, hold, hold_reason, report, reported_at FROM agent_update_policy`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]AgentUpdatePolicy{}
	for rows.Next() {
		var key string
		var p AgentUpdatePolicy
		var raw []byte
		if err := rows.Scan(&key, &p.Hold, &p.HoldReason, &raw, &p.ReportedAt); err != nil {
			return nil, err
		}
		if len(raw) > 0 {
			var r AgentUpdateReport
			if json.Unmarshal(raw, &r) == nil {
				p.Report = &r
			}
		}
		out[key] = p
	}
	return out, rows.Err()
}

// SetAgentUpdateHold liga/desliga o freio de UMA chave.
func (s *Store) SetAgentUpdateHold(ctx context.Context, serverkey string, hold bool, motivo string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO agent_update_policy (serverkey, hold, hold_reason, updated_at)
		VALUES ($1,$2,$3,now())
		ON CONFLICT (serverkey) DO UPDATE SET hold=EXCLUDED.hold, hold_reason=EXCLUDED.hold_reason, updated_at=now()`,
		serverkey, hold, strings.TrimSpace(motivo))
	return err
}

// SaveAgentUpdateReport guarda o último relato do agente. Preserva o hold: o relato
// chega a cada hora e não pode desfazer o que o operador travou.
//
// Preserva TAMBÉM o hostname já conhecido quando o relato novo vem sem ele. O relato
// é sobrescrito de hora em hora; se um agente que informava o hostname passar a não
// informar (downgrade, versão que ainda não manda o campo), a ponte serverkey→servidor
// sumiria da tela sem nada ter mudado no ambiente — e a tela de atualização voltaria a
// dizer "não sei em qual servidor este freio age". Hostname é fato de identidade, não
// estado do momento: só é substituído por outro hostname, nunca por vazio.
func (s *Store) SaveAgentUpdateReport(ctx context.Context, serverkey string, r AgentUpdateReport) error {
	r.Hostname = strings.TrimSpace(r.Hostname)
	val, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO agent_update_policy (serverkey, report, reported_at, updated_at)
		VALUES ($1,$2,now(),now())
		ON CONFLICT (serverkey) DO UPDATE SET
			report = CASE
				WHEN COALESCE(EXCLUDED.report->>'hostname','') <> '' THEN EXCLUDED.report
				WHEN COALESCE(agent_update_policy.report->>'hostname','') <> ''
					THEN EXCLUDED.report || jsonb_build_object('hostname', agent_update_policy.report->'hostname')
				ELSE EXCLUDED.report
			END,
			reported_at = now(), updated_at = now()`,
		serverkey, val)
	return err
}

// DeleteAgentUpdatePolicy remove a linha (chamado ao apagar a chave).
func (s *Store) DeleteAgentUpdatePolicy(ctx context.Context, serverkey string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM agent_update_policy WHERE serverkey=$1`, serverkey)
	return err
}
