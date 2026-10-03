// Package model define os tipos de telemetria compartilhados entre os módulos.
package model

import "time"

// Metric é um ponto de série temporal pronto para ir ao ClickHouse (tabela metrics).
type Metric struct {
	TenantID string            `json:"tenant_id"`
	Metric   string            `json:"metric"`
	Labels   map[string]string `json:"labels"`
	TS       time.Time         `json:"ts"`
	Value    float64           `json:"value"`
}

// Event é um evento discreto (tabela events): snapshot de processos, deploy, mudança de host.
type Event struct {
	TenantID string            `json:"tenant_id"`
	Kind     string            `json:"kind"`
	TS       time.Time         `json:"ts"`
	Title    string            `json:"title"`
	Body     string            `json:"body"`
	Labels   map[string]string `json:"labels"`
}

// LogRecord é uma linha de log pronta para o ClickHouse (tabela logs).
type LogRecord struct {
	TenantID    string            `json:"tenant_id"`
	TS          time.Time         `json:"ts"`
	Service     string            `json:"service"`
	Severity    string            `json:"severity"`
	SeverityNum uint8             `json:"severity_num"`
	Body        string            `json:"body"`
	Labels      map[string]string `json:"labels"`
	TraceID     string            `json:"trace_id"`
	SpanID      string            `json:"span_id"`
}

// Span é um span de trace pronto para o ClickHouse (tabela spans, P5.3).
type Span struct {
	TenantID   string            `json:"tenant_id"`
	TS         time.Time         `json:"ts"` // início do span
	TraceID    string            `json:"trace_id"`
	SpanID     string            `json:"span_id"`
	ParentID   string            `json:"parent_span_id"`
	Service    string            `json:"service"`
	Name       string            `json:"name"`
	Kind       string            `json:"kind"`
	DurationMs float64           `json:"duration_ms"`
	StatusCode string            `json:"status_code"` // UNSET|OK|ERROR
	StatusMsg  string            `json:"status_msg"`
	Labels     map[string]string `json:"labels"`
}

// Host é o inventário de um host monitorado (tabela hosts no PostgreSQL).
type Host struct {
	TenantID     string
	Hostname     string
	OS           string
	Kernel       string
	Arch         string
	CPUModel     string
	CPUCores     int
	IPs          string // lista "iface=ip" separada por vírgula
	AgentVersion string
	UptimeSecs   float64
	LastSeen     time.Time
}
