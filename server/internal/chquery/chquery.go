// Package chquery consulta o ClickHouse pela interface HTTP (leitura, FORMAT JSON).
package chquery

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// httpTimeout é o teto absoluto de uma chamada ao ClickHouse pelo lado do Go.
// Também é o teto usado no `max_execution_time` quando o contexto não traz prazo.
const httpTimeout = 30 * time.Second

type Client struct {
	addr, user, pass, db string
	http                 *http.Client
	log                  *slog.Logger
}

func New(addr, user, pass, db string) *Client {
	return &Client{
		addr: strings.TrimRight(addr, "/"), user: user, pass: pass, db: db,
		http: &http.Client{Timeout: httpTimeout},
	}
}

// SetLogger liga o cliente ao logger estruturado do servidor. Sem isso o detalhe
// dos erros vai para o slog.Default() (stderr, formato texto) — continua legível,
// mas fora do fluxo JSON do resto do painel.
func (c *Client) SetLogger(l *slog.Logger) {
	if l != nil {
		c.log = l
	}
}

func (c *Client) logger() *slog.Logger {
	if c.log != nil {
		return c.log
	}
	return slog.Default()
}

// Error é o erro que o ClickHouse produz, JÁ SANEADO para atravessar a fronteira HTTP.
//
// O motivo: o corpo de erro do ClickHouse é um dossiê da instalação. Uma busca de
// logs com um termo de 400 KB devolvia ao cliente autenticado
// `clickhouse 400: {"exception": "Code: 62. DB::Exception: Syntax error: failed at
// position 227 ...` — ou seja, o SQL montado inteiro, os nomes das colunas e
// a posição exata do literal; e uma consulta que estourava o prazo devolvia
// `Post "http://clickhouse:8123/?database=revoada&…": context deadline exceeded`, que
// entrega host, porta e nome da base. Nada disso é informação que o usuário do painel
// possa usar, e toda ela é mapa para quem estiver sondando.
//
// O detalhe não se perde: vai INTEIRO para o log do servidor, junto com o SQL e o
// mesmo `ref` que o cliente recebe. O operador cruza os dois em uma busca.
type Error struct {
	Ref    string // id de correlação: o mesmo que aparece na mensagem ao cliente e no log
	Op     string // "query" | "exec" | "insert" — o que estava sendo feito
	detail string // mensagem crua do ClickHouse; NUNCA cruza a fronteira HTTP
	cause  error  // só context.DeadlineExceeded/Canceled, para errors.Is do chamador
}

func (e *Error) Error() string {
	return "falha ao consultar o banco de séries (ref " + e.Ref + ")"
}

// Detail devolve a mensagem crua — para uso do OPERADOR (log, teste), nunca do cliente.
func (e *Error) Detail() string { return e.detail }

// Unwrap expõe apenas a causa de contexto (prazo esgotado/cancelado), para quem
// precise distinguir "demorou" de "quebrou". Deliberadamente NÃO expõe o *url.Error
// original, que carrega o endereço interno do ClickHouse.
func (e *Error) Unwrap() error { return e.cause }

// newRef gera o id de correlação. 8 hex bastam: é para casar uma resposta com uma
// linha de log recente, não para ser único no universo.
func newRef() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano()&0xffffffff, 16)
	}
	return hex.EncodeToString(b[:])
}

// fail registra o erro completo no servidor e devolve a versão saneada ao chamador.
func (c *Client) fail(ctx context.Context, op, query, detail string) *Error {
	e := &Error{Ref: newRef(), Op: op, detail: detail}
	if err := ctx.Err(); err != nil {
		e.cause = err
	}
	c.logger().Error("consulta ao ClickHouse falhou",
		"ref", e.Ref, "op", op, "detalhe", detail, "sql", truncate(query, 4000))
	return e
}

// truncate corta o SQL logado. Um WHERE com um literal de 400 KB não pode virar uma
// linha de log de 400 KB — o incidente de disponibilidade sairia do próprio log.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + fmt.Sprintf("…(+%d bytes)", len(s)-n)
}

// readSettings devolve os ajustes por consulta que limitam o custo de UMA leitura.
//
// Existem porque o prazo do lado do Go não era prazo do lado do banco: quando o
// `context` do servidor estourava, o Go abandonava a requisição HTTP e o ClickHouse
// SEGUIA EXECUTANDO. Medido em dev: 5 requisições paralelas com group_by de 1500
// chaves deixaram 10 consultas vivas em `system.processes` DEPOIS que todas as cinco
// já tinham respondido erro ao cliente — o atacante paga 30 s de espera e o banco
// paga o resto sozinho.
//
//   - max_execution_time: espelha no banco o prazo restante do contexto.
//   - cancel_http_readonly_queries_on_client_close: fechar a conexão MATA a consulta.
func readSettings(ctx context.Context) url.Values {
	budget := httpTimeout
	if dl, ok := ctx.Deadline(); ok {
		if left := time.Until(dl); left > 0 && left < budget {
			budget = left
		}
	}
	secs := int(budget.Seconds())
	if secs < 1 {
		secs = 1
	}
	q := url.Values{}
	q.Set("max_execution_time", strconv.Itoa(secs))
	q.Set("cancel_http_readonly_queries_on_client_close", "1")
	return q
}

// QueryJSON executa uma query e devolve as linhas em JSON (FORMAT JSONEachRow),
// já decodificadas como []map[string]any.
func (c *Client) QueryJSON(ctx context.Context, query string) ([]map[string]any, error) {
	q := readSettings(ctx)
	q.Set("database", c.db)
	q.Set("default_format", "JSONEachRow")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.addr+"/?"+q.Encode(), strings.NewReader(query))
	if err != nil {
		return nil, c.fail(ctx, "query", query, err.Error())
	}
	if c.user != "" {
		req.Header.Set("X-ClickHouse-User", c.user)
	}
	if c.pass != "" {
		req.Header.Set("X-ClickHouse-Key", c.pass)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		// err é *url.Error: carrega a URL completa (host, porta, base). Saneia.
		return nil, c.fail(ctx, "query", query, err.Error())
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, c.fail(ctx, "query", query,
			fmt.Sprintf("clickhouse %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
	}
	var rows []map[string]any
	dec := json.NewDecoder(strings.NewReader(string(body)))
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			return nil, c.fail(ctx, "query", query, "resposta ilegível: "+err.Error())
		}
		rows = append(rows, m)
	}
	return rows, nil
}

// Ping valida a conexão.
func (c *Client) Ping(ctx context.Context) error {
	_, err := c.QueryJSON(ctx, "SELECT 1 AS ok")
	return err
}

// Exec roda um statement sem corpo de resultado (DDL/DML: ALTER, DELETE, OPTIMIZE).
// 200 = ok. Para mutations (ALTER … DELETE) o ClickHouse responde assim que a
// mutation é ENFILEIRADA (assíncrona) — acompanhe a conclusão via system.mutations.
func (c *Client) Exec(ctx context.Context, statement string) error {
	q := url.Values{}
	q.Set("database", c.db)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.addr+"/?"+q.Encode(), strings.NewReader(statement))
	if err != nil {
		return c.fail(ctx, "exec", statement, err.Error())
	}
	if c.user != "" {
		req.Header.Set("X-ClickHouse-User", c.user)
	}
	if c.pass != "" {
		req.Header.Set("X-ClickHouse-Key", c.pass)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return c.fail(ctx, "exec", statement, err.Error())
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return c.fail(ctx, "exec", statement,
			fmt.Sprintf("clickhouse exec %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
	}
	return nil
}

// MetricPoint é um ponto a gravar na tabela metrics.
type MetricPoint struct {
	Metric string
	Labels map[string]string
	Value  float64
}

// InsertMetrics grava pontos na tabela metrics (usado pelas sondas sintéticas).
func (c *Client) InsertMetrics(ctx context.Context, tenant string, points []MetricPoint) error {
	return c.InsertMetricsAt(ctx, tenant, time.Now(), points)
}

// InsertMetricsAt grava pontos com um instante explícito. O runner das chamadas de IA
// precisa disso: ele fecha o minuto de 5 minutos atrás (espera os spans de agentes
// longos chegarem), e o ponto tem de cair nesse minuto, não no "agora" do insert.
func (c *Client) InsertMetricsAt(ctx context.Context, tenant string, quando time.Time, points []MetricPoint) error {
	if len(points) == 0 {
		return nil
	}
	ts := quando.UTC().Format("2006-01-02 15:04:05.000")
	var sb strings.Builder
	for _, p := range points {
		labels := p.Labels
		if labels == nil {
			labels = map[string]string{}
		}
		row, _ := json.Marshal(map[string]any{
			"tenant_id": tenant, "metric": p.Metric, "labels": labels, "ts": ts, "value": p.Value,
		})
		sb.Write(row)
		sb.WriteByte('\n')
	}
	q := url.Values{}
	q.Set("database", c.db)
	q.Set("query", "INSERT INTO metrics FORMAT JSONEachRow")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.addr+"/?"+q.Encode(), strings.NewReader(sb.String()))
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
		return c.fail(ctx, "insert", "INSERT INTO metrics", err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return c.fail(ctx, "insert", "INSERT INTO metrics",
			fmt.Sprintf("clickhouse insert metrics %d: %s", resp.StatusCode, strings.TrimSpace(string(b))))
	}
	return nil
}

// InsertEvent grava um evento na tabela events (anotações: alertas, deploys).
func (c *Client) InsertEvent(ctx context.Context, tenant, kind, title, body string, labels map[string]string) error {
	if labels == nil {
		labels = map[string]string{}
	}
	row, _ := json.Marshal(map[string]any{
		"tenant_id": tenant, "kind": kind,
		"ts":    time.Now().UTC().Format("2006-01-02 15:04:05.000"),
		"title": title, "body": body, "labels": labels,
	})
	q := url.Values{}
	q.Set("database", c.db)
	q.Set("query", "INSERT INTO events FORMAT JSONEachRow")
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.addr+"/?"+q.Encode(), strings.NewReader(string(row)))
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
		return c.fail(ctx, "insert", "INSERT INTO events", err.Error())
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return c.fail(ctx, "insert", "INSERT INTO events",
			fmt.Sprintf("clickhouse insert event %d: %s", resp.StatusCode, strings.TrimSpace(string(b))))
	}
	return nil
}
