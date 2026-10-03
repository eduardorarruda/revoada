package chhttp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/eduardorarruda/revoada/gateway/internal/model"
)

// InsertMetrics grava um lote na tabela metrics via INSERT ... FORMAT JSONEachRow.
func (c *Client) InsertMetrics(ctx context.Context, rows []model.Metric) error {
	if len(rows) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, m := range rows {
		labels := m.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		if err := enc.Encode(map[string]any{
			"tenant_id": m.TenantID,
			"metric":    m.Metric,
			"labels":    labels,
			"ts":        m.TS.UTC().Format("2006-01-02 15:04:05.000"),
			"value":     m.Value,
		}); err != nil {
			return err
		}
	}
	return c.insertJSONEachRow(ctx, "metrics", &buf)
}

// InsertEvents grava um lote na tabela events.
func (c *Client) InsertEvents(ctx context.Context, rows []model.Event) error {
	if len(rows) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, e := range rows {
		labels := e.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		if err := enc.Encode(map[string]any{
			"tenant_id": e.TenantID,
			"kind":      e.Kind,
			"ts":        e.TS.UTC().Format("2006-01-02 15:04:05.000"),
			"title":     e.Title,
			"body":      e.Body,
			"labels":    labels,
		}); err != nil {
			return err
		}
	}
	return c.insertJSONEachRow(ctx, "events", &buf)
}

// InsertLogs grava um lote na tabela logs (P5.1).
func (c *Client) InsertLogs(ctx context.Context, rows []model.LogRecord) error {
	if len(rows) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, r := range rows {
		labels := r.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		if err := enc.Encode(map[string]any{
			"tenant_id":    r.TenantID,
			"ts":           r.TS.UTC().Format("2006-01-02 15:04:05.000"),
			"service":      r.Service,
			"severity":     r.Severity,
			"severity_num": r.SeverityNum,
			"body":         r.Body,
			"labels":       labels,
			"trace_id":     r.TraceID,
			"span_id":      r.SpanID,
		}); err != nil {
			return err
		}
	}
	return c.insertJSONEachRow(ctx, "logs", &buf)
}

// InsertSpans grava um lote na tabela spans (P5.3).
func (c *Client) InsertSpans(ctx context.Context, rows []model.Span) error {
	if len(rows) == 0 {
		return nil
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	for _, s := range rows {
		labels := s.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		if err := enc.Encode(map[string]any{
			"tenant_id":      s.TenantID,
			"ts":             s.TS.UTC().Format("2006-01-02 15:04:05.000"),
			"trace_id":       s.TraceID,
			"span_id":        s.SpanID,
			"parent_span_id": s.ParentID,
			"service":        s.Service,
			"name":           s.Name,
			"kind":           s.Kind,
			"duration_ms":    s.DurationMs,
			"status_code":    s.StatusCode,
			"status_msg":     s.StatusMsg,
			"labels":         labels,
		}); err != nil {
			return err
		}
	}
	return c.insertJSONEachRow(ctx, "spans", &buf)
}

func (c *Client) insertJSONEachRow(ctx context.Context, table string, body *bytes.Buffer) error {
	q := url.Values{}
	if c.db != "" {
		q.Set("database", c.db)
	}
	q.Set("query", fmt.Sprintf("INSERT INTO %s FORMAT JSONEachRow", table))
	endpoint := c.addr + "/?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return err
	}
	if c.user != "" {
		req.Header.Set("X-ClickHouse-User", c.user)
	}
	if c.pass != "" {
		req.Header.Set("X-ClickHouse-Key", c.pass)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		msg, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("clickhouse insert %s %d: %s", table, resp.StatusCode, strings.TrimSpace(string(msg)))
	}
	return nil
}
