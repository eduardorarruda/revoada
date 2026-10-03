package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// settingWhatsApp é a chave da linha singleton em app_settings que guarda a
// integração global do WhatsApp (Evolution API), reusada por todos os canais.
const settingWhatsApp = "whatsapp_integration"

// settingLogsLimit é a chave da linha singleton em app_settings com o teto de
// armazenamento de logs (em bytes) que o admin define.
const settingLogsLimit = "logs_storage_limit"

// DefaultLogsStorageLimitBytes é o teto PADRÃO de armazenamento de logs (4 GiB),
// aplicado quando o admin nunca configurou um limite — assim todo ambiente já
// vem com o medidor ativo. O admin pode alterar ou zerar (0 = sem limite).
const DefaultLogsStorageLimitBytes int64 = 4 * 1024 * 1024 * 1024

// GetLogsStorageLimit devolve o teto de armazenamento de logs em bytes. Se o admin
// nunca definiu (linha ausente), devolve o padrão de 4 GiB. Um valor explícito de 0
// (o admin removeu o limite) é preservado como "sem limite".
func (s *Store) GetLogsStorageLimit(ctx context.Context) (int64, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, settingLogsLimit).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return DefaultLogsStorageLimitBytes, nil
	}
	if err != nil {
		return 0, err
	}
	var v struct {
		Bytes int64 `json:"bytes"`
	}
	_ = json.Unmarshal(raw, &v)
	return v.Bytes, nil
}

// SetLogsStorageLimit grava (upsert) o teto em bytes. 0 remove o limite (mantém a
// linha com bytes=0, interpretado como "sem limite").
func (s *Store) SetLogsStorageLimit(ctx context.Context, bytes int64) error {
	if bytes < 0 {
		bytes = 0
	}
	val, _ := json.Marshal(struct {
		Bytes int64 `json:"bytes"`
	}{Bytes: bytes})
	_, err := s.pool.Exec(ctx, `
		INSERT INTO app_settings (key, value, updated_at) VALUES ($1,$2,now())
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, settingLogsLimit, val)
	return err
}

// settingAgentResources é a chave em app_settings com os limites de recurso PADRÃO
// aplicados aos agentes provisionados (a "cerca dura" da unit systemd + o soft cap
// que o agente aplica). Editável pelo admin; injetado no comando de instalação.
const settingAgentResources = "agent_resource_limits"

// AgentResourceLimits são os tetos de recurso do agente, editáveis no painel.
// Campos em MB/percentual/inteiro para casar com as flags do install.sh.
type AgentResourceLimits struct {
	MemoryMaxMB  int `json:"memory_max_mb"`  // MemoryMax da unit (OOM ao estourar)
	MemoryHighMB int `json:"memory_high_mb"` // MemoryHigh (pressão antes do teto)
	CPUQuotaPct  int `json:"cpu_quota_pct"`  // CPUQuota em % de UM núcleo
	Nice         int `json:"nice"`           // Nice (prioridade de CPU)
	TasksMax     int `json:"tasks_max"`      // TasksMax (teto de threads)
	MemSoftMB    int `json:"mem_soft_mb"`    // GOMEMLIMIT do agente (soft), em MB
	MaxProcs     int `json:"max_procs"`      // GOMAXPROCS (0 = auto)
}

// DefaultAgentResourceLimits espelha EXATAMENTE os defaults do install.sh — assim o
// que o painel mostra bate com o que um install sem flags aplica.
func DefaultAgentResourceLimits() AgentResourceLimits {
	return AgentResourceLimits{
		MemoryMaxMB: 256, MemoryHighMB: 200, CPUQuotaPct: 40,
		Nice: 10, TasksMax: 128, MemSoftMB: 220, MaxProcs: 2,
	}
}

// GetAgentResourceLimits devolve os limites configurados ou os defaults se o admin
// nunca definiu. Campos zerados/ausentes no JSON caem no default correspondente,
// para uma linha parcial (versão antiga) nunca virar "0" perigoso.
func (s *Store) GetAgentResourceLimits(ctx context.Context) (AgentResourceLimits, error) {
	def := DefaultAgentResourceLimits()
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, settingAgentResources).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return def, nil
	}
	if err != nil {
		return def, err
	}
	v := def
	_ = json.Unmarshal(raw, &v)
	return v, nil
}

// SetAgentResourceLimits grava (upsert) os limites. Valores <=0 caem no default
// (exceto MaxProcs, onde 0 = auto é legítimo), evitando gravar uma cerca inválida.
func (s *Store) SetAgentResourceLimits(ctx context.Context, v AgentResourceLimits) error {
	def := DefaultAgentResourceLimits()
	if v.MemoryMaxMB <= 0 {
		v.MemoryMaxMB = def.MemoryMaxMB
	}
	if v.MemoryHighMB <= 0 {
		v.MemoryHighMB = def.MemoryHighMB
	}
	if v.CPUQuotaPct <= 0 {
		v.CPUQuotaPct = def.CPUQuotaPct
	}
	if v.TasksMax <= 0 {
		v.TasksMax = def.TasksMax
	}
	if v.MemSoftMB <= 0 {
		v.MemSoftMB = def.MemSoftMB
	}
	if v.MaxProcs < 0 {
		v.MaxProcs = def.MaxProcs
	}
	val, _ := json.Marshal(v)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO app_settings (key, value, updated_at) VALUES ($1,$2,now())
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, settingAgentResources, val)
	return err
}

// WhatsAppIntegration é a configuração ÚNICA do wuzapi: definida uma vez e
// reusada por todo canal de WhatsApp, que passa a guardar só os destinatários.
// Substituiu a config da Evolution API (base_url/instance/apikey) em 29/07/2026.
type WhatsAppIntegration struct {
	BaseURL string `json:"base_url"`
	Token   string `json:"token"`
}

// GetWhatsAppIntegration devolve a config global (sem redação — uso interno do
// sender). Ausente = struct zerada, sem erro.
func (s *Store) GetWhatsAppIntegration(ctx context.Context) (WhatsAppIntegration, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, settingWhatsApp).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return WhatsAppIntegration{}, nil
	}
	if err != nil {
		return WhatsAppIntegration{}, err
	}
	var w WhatsAppIntegration
	_ = json.Unmarshal(raw, &w)
	return w, nil
}

// SetWhatsAppIntegration grava (upsert) a config global. Um token vazio ou com o
// marcador de redação PRESERVA o atual — assim a UI pode reenviar o campo em branco
// sem apagar o segredo (mesma semântica dos canais).
func (s *Store) SetWhatsAppIntegration(ctx context.Context, w WhatsAppIntegration) error {
	if w.Token == "" || w.Token == "••••••" {
		cur, err := s.GetWhatsAppIntegration(ctx)
		if err != nil {
			return err
		}
		w.Token = cur.Token
	}
	val, _ := json.Marshal(w)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO app_settings (key, value, updated_at) VALUES ($1,$2,now())
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, settingWhatsApp, val)
	return err
}

// GetSettingJSON lê a linha `key` de app_settings em `dst`. Devolve found=false
// (sem erro) quando a linha não existe — o chamador decide o padrão.
func (s *Store) GetSettingJSON(ctx context.Context, key string, dst any) (bool, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT value FROM app_settings WHERE key=$1`, key).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return false, fmt.Errorf("app_settings %s: %w", key, err)
	}
	return true, nil
}

// SetSettingJSON grava `v` na linha `key` de app_settings (upsert).
func (s *Store) SetSettingJSON(ctx context.Context, key string, v any) error {
	val, err := json.Marshal(v)
	if err != nil {
		return fmt.Errorf("app_settings %s: %w", key, err)
	}
	_, err = s.pool.Exec(ctx, `
		INSERT INTO app_settings (key, value, updated_at) VALUES ($1,$2,now())
		ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value, updated_at=now()`, key, val)
	return err
}
