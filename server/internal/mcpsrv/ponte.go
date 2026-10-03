package mcpsrv

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Ponte liga um cliente MCP por stdio (Claude Desktop, Claude Code, IDEs) ao /mcp do
// painel: cada linha JSON-RPC lida da entrada vira um POST com o token, e a resposta
// volta como uma linha na saída. É o `revoada-painel mcp` — não abre banco nenhum,
// não guarda nada: tudo passa pelas mesmas regras e pela mesma auditoria do HTTP.
func Ponte(ctx context.Context, url, token string, entrada io.Reader, saida io.Writer, cli *http.Client) error {
	if url == "" || token == "" {
		return errors.New("defina REVOADA_URL (ex.: https://painel.empresa.com) e REVOADA_MCP_TOKEN (rvm_…)")
	}
	if cli == nil {
		cli = &http.Client{Timeout: 5 * time.Minute}
	}
	fim := strings.TrimRight(url, "/") + "/mcp"
	sessao := ""
	defer func() {
		if sessao != "" { // encerra a sessão no painel
			r, _ := http.NewRequest(http.MethodDelete, fim, nil)
			r.Header.Set("Authorization", "Bearer "+token)
			r.Header.Set("Mcp-Session-Id", sessao)
			if resp, err := cli.Do(r); err == nil {
				resp.Body.Close()
			}
		}
	}()
	leitor := bufio.NewReader(entrada)
	for {
		linha, err := leitor.ReadBytes('\n')
		if len(bytes.TrimSpace(linha)) > 0 {
			if e := enviar(ctx, cli, fim, token, &sessao, bytes.TrimSpace(linha), saida); e != nil {
				return e
			}
		}
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func enviar(ctx context.Context, cli *http.Client, url, token string, sessao *string, msg []byte, saida io.Writer) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(msg))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("Authorization", "Bearer "+token)
	if *sessao != "" {
		req.Header.Set("Mcp-Session-Id", *sessao)
	}
	resp, err := cli.Do(req)
	if err != nil {
		return fmt.Errorf("painel inacessível: %w", err)
	}
	defer resp.Body.Close()
	if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
		*sessao = id
	}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("o painel recusou o token MCP (%d): confira REVOADA_MCP_TOKEN, a validade e se não foi revogado", resp.StatusCode)
	case resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent:
		return nil // notificação: sem resposta
	case resp.StatusCode >= 300:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("o painel respondeu %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))
	}
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 64<<10), 16<<20)
		for sc.Scan() {
			if d, ok := strings.CutPrefix(sc.Text(), "data:"); ok {
				if _, err := fmt.Fprintln(saida, strings.TrimSpace(d)); err != nil {
					return err
				}
			}
		}
		return sc.Err()
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return err
	}
	if b = bytes.TrimSpace(b); len(b) > 0 {
		_, err = fmt.Fprintln(saida, string(b))
	}
	return err
}
