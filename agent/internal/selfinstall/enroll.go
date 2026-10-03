package selfinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Inscrição: o instalador UNIVERSAL (um arquivo para vários servidores) não carrega
// uma chave de ingestão — carrega um token que só serve para pedir uma. Ao instalar,
// o agente se apresenta ao painel dizendo o seu hostname e recebe uma chave própria,
// criada na hora só para esta máquina.
//
// Duas consequências práticas: cada servidor acaba com a SUA chave (revogar um não
// derruba os outros), e o mesmo arquivo pode rodar em quantas máquinas você quiser.

const enrollTimeout = 20 * time.Second

// maxErro limita quanto do corpo de uma resposta de erro é lido. O painel devolve
// uma frase; um proxy no meio do caminho pode devolver uma página inteira.
const maxErro = 8 << 10

// Enroll troca o token de inscrição por uma chave de ingestão. `panelURL` é o
// endereço do painel (o mesmo que o navegador usa).
func Enroll(ctx context.Context, panelURL, token, hostname string) (string, error) {
	if strings.TrimSpace(panelURL) == "" {
		return "", fmt.Errorf("endereço do painel não informado")
	}
	corpo, err := json.Marshal(map[string]string{"token": token, "hostname": hostname})
	if err != nil {
		return "", err
	}
	url := strings.TrimRight(panelURL, "/") + "/api/enroll"

	ctx, cancel := context.WithTimeout(ctx, enrollTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(corpo))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("não consegui falar com o painel em %s: %w", url, err)
	}
	defer func() { _, _ = io.Copy(io.Discard, resp.Body); _ = resp.Body.Close() }()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// O painel responde com uma frase pronta para o administrador da máquina
		// (ex.: "token inválido ou revogado — gere um instalador novo"). Repassar
		// essa frase vale mais que traduzir um código HTTP.
		b, _ := io.ReadAll(io.LimitReader(resp.Body, maxErro))
		motivo := strings.TrimSpace(string(b))
		if motivo == "" {
			motivo = resp.Status
		}
		return "", fmt.Errorf("o painel recusou a inscrição: %s", motivo)
	}

	var out struct {
		Key string `json:"key"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxErro)).Decode(&out); err != nil {
		return "", fmt.Errorf("resposta do painel ilegível: %w", err)
	}
	if out.Key == "" {
		return "", fmt.Errorf("o painel respondeu sem a chave de ingestão")
	}
	return out.Key, nil
}
