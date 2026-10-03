package logtail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// Estado de leitura persistido entre execuções do agente.
//
// O mapa de offsets vivia só na memória e Run() começava chamando seedOffsets(),
// que posiciona a leitura no FIM de cada arquivo. Isso quer dizer que tudo que foi
// escrito enquanto o agente esteve parado — um `systemctl restart`, uma atualização
// do pacote, um reboot — nunca era coletado. Justamente a janela em que mais
// interessa saber o que o servidor registrou.
//
// Guardar `caminho → (inode, offset)` em disco resolve: no boot o agente retoma de
// onde parou, e seedOffsets passa a valer só para arquivo que ele nunca viu.

// fileState é a posição de leitura de um arquivo. O inode existe porque detectar
// rotação só por tamanho não funciona em `copytruncate` (logrotate copia e trunca,
// mantendo o inode) nem quando o arquivo novo já nasce maior que o offset antigo.
// Em teste ao vivo com copytruncate, o modo antigo perdeu 11 linhas e entregou a
// 12ª cortada ao meio.
//
// Seen é a última vez (unix seconds) em que o arquivo foi VISTO pelo glob. Existe
// porque a poda antiga apagava o offset na primeira varredura em que o caminho não
// aparecia — e uma ausência de um ciclo é rotina, não fim de vida: o logrotate
// renomeia e recria com uma janela de milissegundos, um `truncate`/`mv` de operador
// idem, e um glob por data (`app-%Y%m%d.log`) some e volta na virada. Ao voltar, o
// arquivo era tratado como novo, o seedOffsets já não rodava (só roda no boot) e o
// readNew lia do byte 0: o arquivo INTEIRO era reenviado. Medido: cada linha
// chegando ao ClickHouse com vezes = 2.
type fileState struct {
	Ino  uint64 `json:"ino"`            // 0 = sistema sem inode utilizável (Windows)
	Off  int64  `json:"off"`            // bytes já lidos e enviados
	Seen int64  `json:"seen,omitempty"` // unix seconds da última vez que o glob viu o arquivo
	// Rabo são os últimos bytes lidos (até tamanhoRabo) antes de Off: a impressão
	// digital que denuncia um truncamento seguido de reescrita maior que Off, em
	// que inode e tamanho não mudam de jeito detectável (ver logtail.go).
	Rabo []byte `json:"rabo,omitempty"`
}

// stateDir é onde os coletores de log guardam seu estado. Definido uma vez no boot
// por SetLogStateDir (padrão do SetLogRateLimit); vazio = sem persistência, e aí o
// comportamento é o antigo (começa no fim de cada arquivo).
var (
	stateMu  sync.Mutex
	stateDir string
)

// SetLogStateDir configura o diretório de estado dos coletores de log. Deve ser o
// buffer_dir: é o único diretório que o agente comprovadamente consegue escrever,
// que o instalador entrega ao usuário do serviço e que a desinstalação apaga. O
// buffer só enxerga arquivos `.otlp`, então este estado nunca é confundido com um
// lote nem varrido pela rotação.
func SetLogStateDir(dir string) {
	stateMu.Lock()
	defer stateMu.Unlock()
	stateDir = dir
}

// stateFilePath resolve um nome de arquivo dentro do diretório de estado, ou ""
// quando a persistência não foi configurada. Compartilhado pelo tail de arquivo e
// pelos cursores de journald/docker (cursor.go), que precisam do MESMO buffer_dir.
func stateFilePath(name string) string {
	stateMu.Lock()
	dir := stateDir
	stateMu.Unlock()
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, name)
}

// stateFile devolve o caminho do estado deste coletor, ou "" se a persistência não
// foi configurada. Um arquivo por `source` porque file e syslog são dois Tailers
// independentes rodando no mesmo processo.
func (t *Tailer) stateFile() string {
	return stateFilePath("logtail-" + t.source + ".state")
}

// loadOffsets lê o estado gravado. Best-effort: estado ausente ou ilegível apenas
// devolve o agente ao comportamento de começar do fim, nunca impede a coleta.
func (t *Tailer) loadOffsets() {
	path := t.stateFile()
	if path == "" {
		return
	}
	b, err := os.ReadFile(path) // #nosec G304 -- caminho derivado da config do próprio agente
	if err != nil {
		return
	}
	var m map[string]fileState
	if json.Unmarshal(b, &m) != nil {
		return
	}
	for k, v := range m {
		t.offsets[k] = v
	}
}

// saveOffsets grava por arquivo temporário + rename, para que um agente morto no
// meio da escrita nunca deixe um estado pela metade — que, lido no boot seguinte,
// mandaria o coletor para um offset inventado. 0600: o estado lista os caminhos dos
// arquivos de log do servidor, e só o usuário do serviço precisa lê-lo.
func (t *Tailer) saveOffsets() {
	path := t.stateFile()
	if path == "" || !t.dirty {
		return
	}
	if writeStateJSON(path, t.offsets) {
		t.dirty = false
	}
}

// writeStateJSON grava um estado em JSON por arquivo temporário + rename. Extraído
// para o cursor de journald/docker (cursor.go) escrever com a mesma garantia: um
// agente morto no meio da escrita não pode deixar um estado pela metade, que no boot
// seguinte mandaria o coletor para uma posição inventada.
func writeStateJSON(path string, v any) bool {
	b, err := json.Marshal(v)
	if err != nil {
		return false
	}
	if os.MkdirAll(filepath.Dir(path), 0o755) != nil {
		return false
	}
	tmp := path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) != nil {
		return false
	}
	if os.Rename(tmp, path) != nil {
		_ = os.Remove(tmp)
		return false
	}
	return true
}
