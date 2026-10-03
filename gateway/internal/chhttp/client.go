// Package chhttp é um cliente ClickHouse mínimo sobre a interface HTTP,
// usando apenas a stdlib (sem dependências externas). Suficiente para o
// migrator e para consultas simples; o caminho de escrita em lote (P1.4)
// usa este mesmo cliente.
package chhttp

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client fala com o ClickHouse pela porta HTTP (8123 por padrão).
type Client struct {
	addr string // ex.: http://127.0.0.1:8123
	user string
	pass string
	db   string
	http *http.Client
}

// Config vem de env vars (prefixo REVOADA_).
type Config struct {
	Addr string
	User string
	Pass string
	DB   string
}

// RequestTimeout é o prazo máximo de UMA chamada ao ClickHouse (inclusive um INSERT
// em lote). É exportado de propósito: o AckWait do consumer do writer TEM de ser maior
// que ele, senão o JetStream reentrega uma mensagem cujo insert ainda está em curso e
// o mesmo lote é gravado duas vezes. O teste TestAckWaitMaiorQueInsert (pacote queue)
// trava essa relação.
const RequestTimeout = 60 * time.Second

// New cria o cliente com timeout padrão.
func New(c Config) *Client {
	return &Client{
		addr: strings.TrimRight(c.Addr, "/"),
		user: c.User,
		pass: c.Pass,
		db:   c.DB,
		http: &http.Client{Timeout: RequestTimeout},
	}
}

// Exec envia uma única instrução SQL e devolve o corpo da resposta.
// Erros HTTP do ClickHouse (não-200) viram error com a mensagem do servidor.
func (c *Client) Exec(ctx context.Context, query string) (string, error) {
	q := url.Values{}
	if c.db != "" {
		q.Set("database", c.db)
	}
	endpoint := c.addr + "/?" + q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(query))
	if err != nil {
		return "", err
	}
	if c.user != "" {
		req.Header.Set("X-ClickHouse-User", c.user)
	}
	if c.pass != "" {
		req.Header.Set("X-ClickHouse-Key", c.pass)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("clickhouse request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("clickhouse %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return string(body), nil
}

// Ping valida a conexão com um SELECT 1.
func (c *Client) Ping(ctx context.Context) error {
	out, err := c.Exec(ctx, "SELECT 1")
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "1" {
		return fmt.Errorf("ping inesperado: %q", out)
	}
	return nil
}
