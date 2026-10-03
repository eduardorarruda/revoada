package installer

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
)

// Magic marca o fim de um binário do agente com receita embutida.
//
// ATENÇÃO — formato compartilhado entre dois módulos Go: quem ESCREVE o trailer é
// este arquivo, quem LÊ é agent/internal/selfinstall (que documenta o formato em
// detalhe). Como `server/` e `agent/` são módulos separados (sem replace), o
// formato está duplicado nos dois lados de propósito. Mudou aqui, mude lá — e suba
// a versão do magic, que é o que impede um agente antigo de ler um trailer novo.
//
//	[ binário original ][ receita em JSON ][ magic (26 B) ][ tamanho do JSON (8 B, big-endian) ]
const Magic = "REVOADA-INSTALADOR-v1"

// Recipe é o que vai embutido: o suficiente para o agente se configurar sozinho.
// Os campos espelham selfinstall.Recipe do agente — mesmos nomes de JSON.
type Recipe struct {
	GatewayURL string `json:"gateway_url"`
	Key        string `json:"key"`
	PanelURL   string `json:"panel_url"`
	// EnrollToken substitui Key no instalador universal: a máquina pede a chave
	// dela ao painel na hora de instalar.
	EnrollToken     string `json:"enroll_token,omitempty"`
	Probe           bool   `json:"probe,omitempty"`
	IntervalSeconds int    `json:"interval_seconds,omitempty"`
	MemoryLimitMB   int    `json:"memory_limit_mb,omitempty"`
	MaxProcs        int    `json:"max_procs,omitempty"`
}

// AppendRecipe devolve o binário com a receita colada no fim. Não altera o
// original: PE, ELF e Mach-O são descritos por cabeçalhos com offsets, então o
// carregador do sistema ignora o que vem depois do fim declarado — e o executável
// continua funcionando exatamente como antes para quem o roda com -config.
func AppendRecipe(binario []byte, rec Recipe) ([]byte, error) {
	js, err := json.Marshal(rec)
	if err != nil {
		return nil, fmt.Errorf("serializando a receita: %w", err)
	}
	out := make([]byte, 0, len(binario)+len(js)+len(Magic)+8)
	out = append(out, binario...)
	out = append(out, js...)
	out = append(out, Magic...)
	out = binary.BigEndian.AppendUint64(out, uint64(len(js)))
	return out, nil
}
