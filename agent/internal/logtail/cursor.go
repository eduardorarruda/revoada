package logtail

import (
	"encoding/json"
	"os"
	"sync"
)

// Cursores dos coletores de STREAM (journald e docker).
//
// # O buraco que isto fecha
//
// O tail de ARQUIVO persiste o offset de cada arquivo (state.go) e por isso atravessa
// uma parada do agente sem perder nada. Os dois coletores de stream não persistiam
// coisa alguma: o docker abria o follow com `tail=0` ("nada do que já existe") e o
// journald com `--since now`. Os dois, portanto, começavam em AGORA a cada boot, e
// tudo que os serviços registraram enquanto o agente esteve parado nunca chegava ao
// painel.
//
// "Enquanto o agente esteve parado" não é uma situação rara nem excepcional. É:
// restart do operador, reboot do host, o Supervise reerguendo um coletor que caiu — e,
// acima de tudo, a AUTO-ATUALIZAÇÃO, que para o agente de propósito, sozinha, sem
// ninguém pedir. Ou seja: a rotina de manter a frota em dia abria um buraco de log em
// todo host, toda vez, e o buraco era invisível — no painel ele se parece exatamente
// com um servidor que ficou quieto.
//
// Guardar o cursor no MESMO buffer_dir do state.go resolve, e pelo mesmo motivo dele:
// é o único diretório que o agente comprovadamente escreve, o instalador o entrega ao
// usuário do serviço, a desinstalação o apaga, e o buffer só enxerga arquivos `.otlp`,
// então o estado nunca é confundido com um lote pendente.

// cursorStore é um mapa chave→posição persistido em JSON. Uma instância por coletor:
// o journald guarda um valor só (o cursor opaco do journal) e o docker guarda um
// timestamp por container.
type cursorStore struct {
	mu      sync.Mutex
	arquivo string // "" quando a persistência não foi configurada (SetLogStateDir)
	dados   map[string]string
	sujo    bool
}

// novoCursor abre (e carrega) o cursor de um coletor. Best-effort do início ao fim:
// arquivo ausente, ilegível ou diretório não configurado apenas devolvem o
// comportamento antigo — começar do agora —, nunca impedem a coleta.
func novoCursor(nome string) *cursorStore {
	c := &cursorStore{arquivo: stateFilePath(nome), dados: map[string]string{}}
	if c.arquivo == "" {
		return c
	}
	b, err := os.ReadFile(c.arquivo) // #nosec G304 -- caminho derivado da config do próprio agente
	if err != nil {
		return c
	}
	var m map[string]string
	if json.Unmarshal(b, &m) != nil {
		return c
	}
	for k, v := range m {
		c.dados[k] = v
	}
	return c
}

func (c *cursorStore) ler(chave string) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.dados[chave]
}

// gravar registra a posição em memória. A escrita em disco é do salvar, para não
// fazer um write por linha de log.
func (c *cursorStore) gravar(chave, valor string) {
	if valor == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.dados[chave] == valor {
		return
	}
	c.dados[chave] = valor
	c.sujo = true
}

// podar descarta as chaves que não existem mais (containers removidos). Sem isso o
// arquivo cresceria para sempre num host que recria containers a cada deploy — o mesmo
// vazamento lento que podarCPUState corrige do lado das métricas.
func (c *cursorStore) podar(vivos map[string]string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.dados {
		if _, ok := vivos[k]; !ok {
			delete(c.dados, k)
			c.sujo = true
		}
	}
}

// salvar grava em disco se houve mudança. Usa o mesmo writeStateJSON do tail de
// arquivo (temporário + rename): um agente morto no meio da escrita não pode deixar
// um cursor pela metade, que no boot seguinte mandaria o coletor para uma posição
// inventada — e "posição inventada" no journald significa reenviar o journal inteiro.
func (c *cursorStore) salvar() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.arquivo == "" || !c.sujo {
		return
	}
	if writeStateJSON(c.arquivo, c.dados) {
		c.sujo = false
	}
}

// esquecer apaga o cursor guardado. Chamado quando o journalctl recusa o cursor
// (journal rotacionado/vacuum, host restaurado de snapshot): insistir num cursor que
// não existe mais deixaria o coletor num laço de falha, e o certo é degradar para
// "começa de agora" — perder o histórico é ruim, parar de coletar é pior.
func (c *cursorStore) esquecer(chave string) {
	c.mu.Lock()
	if _, ok := c.dados[chave]; ok {
		delete(c.dados, chave)
		c.sujo = true
	}
	c.mu.Unlock()
	c.salvar()
}
