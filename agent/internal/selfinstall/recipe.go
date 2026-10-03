// Package selfinstall dá ao binário do agente a capacidade de se instalar sozinho.
//
// A ideia: o painel pega o binário pronto do agente, gruda uma "receita" (chave de
// ingestão, endereço do gateway, limites) no FIM do arquivo e entrega isso ao
// administrador como um executável único. Quem recebe só precisa rodar — o agente
// lê a receita de dentro de si mesmo, escreve o agent.yaml, se registra como
// serviço e começa a reportar. Nada de editar YAML à mão nem colar chave.
//
// Grudar bytes no fim de um executável é seguro em PE (Windows), ELF (Linux) e
// Mach-O (macOS): os três formatos são descritos por cabeçalhos com offsets, então
// o carregador simplesmente ignora o que vem depois do fim declarado.
//
// # Formato do trailer (v1)
//
//	[ binário original ][ receita em JSON ][ magic (26 B) ][ tamanho do JSON (8 B, big-endian) ]
//
// O tamanho vem por ÚLTIMO de propósito: para ler basta o tamanho do arquivo, um
// seek para trás de 34 bytes e um segundo seek — sem varrer o binário inteiro.
//
// ATENÇÃO — formato compartilhado entre dois módulos Go: quem ESCREVE o trailer é
// o painel (server/internal/installer/trailer.go), quem LÊ é este pacote. Como
// `agent/` e `server/` são módulos separados (sem replace), o formato está
// duplicado nos dois lados de propósito. Mudou aqui, mude lá — e suba a versão do
// magic, que é o que impede um binário novo de ler um trailer velho.
package selfinstall

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

// Magic marca o fim de um binário com receita embutida. A versão faz parte da
// string: um formato futuro usa outro magic e os binários antigos simplesmente
// não o reconhecem (em vez de lerem lixo).
const Magic = "REVOADA-INSTALADOR-v1"

// TrailerSize é o rodapé de tamanho fixo: magic + o comprimento do JSON.
const TrailerSize = len(Magic) + 8

// maxRecipe limita o JSON lido do próprio binário. A receita real tem algumas
// centenas de bytes; o teto só existe para que um arquivo corrompido (ou um
// trailer forjado) não vire uma alocação absurda.
const maxRecipe = 64 << 10

// Recipe é o que o painel embute no instalador: o suficiente para o agente se
// configurar sozinho, e nada além disso.
type Recipe struct {
	GatewayURL string `json:"gateway_url"` // para onde o agente envia (ex.: https://painel.exemplo)
	Key        string `json:"key"`         // chave de ingestão (serverkey) — instalador por servidor
	PanelURL   string `json:"panel_url"`   // endereço do painel: inscrição e mensagem final
	// EnrollToken vem no lugar de Key no instalador UNIVERSAL: em vez da chave
	// pronta, o agente pede a sua ao painel na hora de instalar (ver enroll.go).
	// Exatamente um dos dois vem preenchido.
	EnrollToken string `json:"enroll_token,omitempty"`
	// Probe marca este host como sonda multi-região.
	Probe bool `json:"probe,omitempty"`
	// IntervalSeconds é o intervalo de coleta; 0 => default do agente (15s).
	IntervalSeconds int `json:"interval_seconds,omitempty"`
	// MemoryLimitMB/MaxProcs são a cerca suave (GOMEMLIMIT/GOMAXPROCS) que o
	// agente aplica no boot. 0 => não aplica.
	MemoryLimitMB int `json:"memory_limit_mb,omitempty"`
	MaxProcs      int `json:"max_procs,omitempty"`
}

// ErrSemReceita indica que o binário não tem trailer — é o agente comum, rodando
// pelo serviço com -config. Não é erro de verdade: é o caminho normal.
var ErrSemReceita = errors.New("binário sem receita embutida")

// Embedded lê a receita embutida no executável em curso. Devolve também onde
// termina o binário ORIGINAL (offset do começo do JSON): é por aí que a instalação
// corta ao copiar o agente para o disco, para que o agente instalado fique
// idêntico ao oficial — sem a chave de ingestão grudada no fim.
//
// Devolve ErrSemReceita quando não há trailer, e um erro descritivo quando há um
// trailer mas ele está corrompido — a distinção importa: o primeiro caso é
// silencioso (é o agente comum rodando pelo serviço), o segundo precisa aparecer
// para o administrador.
func Embedded() (Recipe, int64, error) {
	exe, err := os.Executable()
	if err != nil {
		return Recipe{}, 0, fmt.Errorf("localizando o próprio executável: %w", err)
	}
	f, err := os.Open(exe)
	if err != nil {
		return Recipe{}, 0, fmt.Errorf("abrindo %s: %w", exe, err)
	}
	defer func() { _ = f.Close() }()
	st, err := f.Stat()
	if err != nil {
		return Recipe{}, 0, fmt.Errorf("lendo %s: %w", exe, err)
	}
	return ReadFrom(f, st.Size())
}

// ReadFrom extrai a receita de um binário já aberto. Separado de Embedded para
// ser testável sem depender do executável em curso.
func ReadFrom(r io.ReaderAt, size int64) (Recipe, int64, error) {
	if size < int64(TrailerSize) {
		return Recipe{}, 0, ErrSemReceita
	}
	foot := make([]byte, TrailerSize)
	if _, err := r.ReadAt(foot, size-int64(TrailerSize)); err != nil {
		return Recipe{}, 0, fmt.Errorf("lendo o rodapé: %w", err)
	}
	if string(foot[:len(Magic)]) != Magic {
		return Recipe{}, 0, ErrSemReceita
	}
	n := binary.BigEndian.Uint64(foot[len(Magic):])
	// A partir daqui o magic já bateu: qualquer inconsistência é trailer
	// corrompido (download truncado, antivírus que "limpou" o fim do arquivo), e
	// tem de ser reportada em vez de virar "binário comum".
	if n == 0 || n > maxRecipe || int64(n)+int64(TrailerSize) > size {
		return Recipe{}, 0, fmt.Errorf("receita embutida com tamanho inválido (%d bytes) — o download pode ter sido truncado; baixe o instalador de novo", n)
	}
	fim := size - int64(TrailerSize) - int64(n)
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, fim); err != nil {
		return Recipe{}, 0, fmt.Errorf("lendo a receita embutida: %w", err)
	}
	var rec Recipe
	if err := json.Unmarshal(buf, &rec); err != nil {
		return Recipe{}, 0, fmt.Errorf("receita embutida ilegível: %w", err)
	}
	// Chave OU token de inscrição: o instalador por servidor traz a primeira, o
	// universal traz o segundo. Sem nenhum dos dois o agente não teria como se
	// autenticar, e instalar assim só adiaria a descoberta do problema.
	if rec.GatewayURL == "" || (rec.Key == "" && rec.EnrollToken == "") {
		return Recipe{}, 0, errors.New("receita embutida incompleta (sem gateway, e sem chave nem token de inscrição) — gere o instalador de novo no painel")
	}
	return rec, fim, nil
}
