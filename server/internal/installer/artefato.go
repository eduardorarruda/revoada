package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"strings"
	"sync"
	"time"
)

// Resolução do artefato que a auto-atualização do agente vai baixar.
//
// A regra que dá sentido a tudo isto: o checksum é calculado a partir do arquivo
// que o painel REALMENTE serve, no momento da pergunta. Nunca é digitado à mão,
// nunca vem de um arquivo de metadados escrito por outra pessoa e nunca é
// herdado de um build anterior. Um checksum digitado é um checksum que, no dia
// em que diverge do artefato, transforma "atualização" em "a frota inteira
// recusa atualizar" — ou, pior, em "a frota aceita o que veio".

// Artefato descreve o binário do agente publicado no dist.
type Artefato struct {
	// Nome é o arquivo dentro do dist (também o caminho servido pelo painel).
	Nome string
	// Versao é a versão que este artefato deve reportar.
	Versao string
	// SHA256 em hexadecimal minúsculo, calculado do conteúdo servido.
	SHA256 string
	// Tamanho em bytes.
	Tamanho int64
}

// versaoAgentePadrao é a versão do agente publicado, usada quando o dist não traz
// um arquivo VERSION.
//
// Servidor e agente saem do MESMO commit e sobem no mesmo deploy, então esta
// constante acerta na prática. Ainda assim é um fallback, não a fonte: o dist
// pode conter um binário mais antigo do que o servidor (deploy parcial, rollback
// só do front). Por isso o VERSION do dist tem precedência, e por isso existe um
// teste que compara esta constante com a do agente — ver artefato_test.go.
const versaoAgentePadrao = "0.8.3"

// arqVersao é o marcador de versão publicado junto com os binários. Uma linha,
// só o número.
const arqVersao = "VERSION"

// Artefatos resolve e mede os binários publicados, com cache.
type Artefatos struct {
	dist fs.FS
	mu   sync.Mutex
	// cache guarda o SHA-256 por arquivo. A chave de invalidação é
	// (tamanho, modtime): o deploy reescreve o arquivo, a modtime muda e o
	// checksum é recalculado sozinho. Sem cache, cada agente da frota faria o
	// painel ler e digerir 12 MB de disco a cada hora — o custo cresce com a
	// frota, que é exatamente a direção errada.
	cache map[string]entradaCache
}

type entradaCache struct {
	tamanho int64
	modtime time.Time
	sha256  string
}

func NewArtefatos(dist fs.FS) *Artefatos {
	return &Artefatos{dist: dist, cache: map[string]entradaCache{}}
}

// nomeArtefato mapeia (GOOS, GOARCH) para o arquivo publicado pelo Makefile.
//
// Devolve erro — e não um palpite — para combinação não publicada. O caso real é
// linux/arm64: o `make agent` só compila linux/amd64. Apontar um ARM para o
// binário amd64 produziria um download íntegro, com checksum correto, que não
// executa. O agente pegaria isso na checagem de sanidade, mas só depois de gastar
// a banda e de reportar uma falha que ninguém saberia explicar. Melhor dizer aqui
// que não existe artefato para aquela máquina.
func nomeArtefato(so, arch string) (string, error) {
	so, arch = strings.ToLower(strings.TrimSpace(so)), strings.ToLower(strings.TrimSpace(arch))
	switch {
	case so == "linux" && arch == "amd64":
		return "revoada-agent", nil
	case so == "windows" && arch == "amd64":
		return arqAgenteWin, nil
	case so == "darwin" && arch == "arm64":
		return "revoada-agent-darwin-arm64", nil
	case so == "darwin" && arch == "amd64":
		return "revoada-agent-darwin-amd64", nil
	}
	return "", fmt.Errorf("%w: o painel não publica binário para %s/%s", ErrArtefatoAusente, so, arch)
}

// Para devolve o artefato correspondente à plataforma do agente.
func (a *Artefatos) Para(so, arch string) (Artefato, error) {
	nome, err := nomeArtefato(so, arch)
	if err != nil {
		return Artefato{}, err
	}
	if a == nil || a.dist == nil {
		return Artefato{}, fmt.Errorf("%w: diretório de artefatos não configurado (REVOADA_AGENT_DIST_DIR)", ErrArtefatoAusente)
	}
	info, err := fs.Stat(a.dist, nome)
	if err != nil {
		return Artefato{}, fmt.Errorf("%w: %s", ErrArtefatoAusente, nome)
	}
	if info.Size() == 0 {
		return Artefato{}, fmt.Errorf("%w: %s está vazio", ErrArtefatoAusente, nome)
	}
	soma, err := a.somaDe(nome, info.Size(), info.ModTime())
	if err != nil {
		return Artefato{}, err
	}
	versao, err := a.Versao()
	if err != nil {
		return Artefato{}, err
	}
	return Artefato{Nome: nome, Versao: versao, SHA256: soma, Tamanho: info.Size()}, nil
}

// somaDe devolve o SHA-256 do arquivo, reaproveitando o cache quando tamanho e
// modtime não mudaram.
func (a *Artefatos) somaDe(nome string, tamanho int64, mod time.Time) (string, error) {
	a.mu.Lock()
	if e, ok := a.cache[nome]; ok && e.tamanho == tamanho && e.modtime.Equal(mod) {
		a.mu.Unlock()
		return e.sha256, nil
	}
	a.mu.Unlock()

	// Digestão fora do lock: ler 12 MB de disco segurando o mutex bloquearia todas
	// as outras consultas da frota atrás de um I/O. Duas leituras simultâneas do
	// mesmo arquivo são desperdício aceitável; a serialização da frota não é.
	f, err := a.dist.Open(nome)
	if err != nil {
		return "", fmt.Errorf("%w: %s", ErrArtefatoAusente, nome)
	}
	// Só leitura: fechar não pode falhar de um jeito que mude o checksum já calculado,
	// e engolir o erro aqui é deliberado — não há o que reportar a quem chamou.
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("lendo %s para calcular o checksum: %w", nome, err)
	}
	soma := hex.EncodeToString(h.Sum(nil))

	a.mu.Lock()
	a.cache[nome] = entradaCache{tamanho: tamanho, modtime: mod, sha256: soma}
	a.mu.Unlock()
	return soma, nil
}

// Versao devolve a versão publicada no dist. Prefere o arquivo VERSION; sem ele,
// cai na constante compilada junto com este servidor.
func (a *Artefatos) Versao() (string, error) {
	if a != nil && a.dist != nil {
		if b, err := fs.ReadFile(a.dist, arqVersao); err == nil {
			v := strings.TrimSpace(strings.SplitN(string(b), "\n", 2)[0])
			v = strings.TrimPrefix(v, "v")
			if versaoValida(v) {
				return v, nil
			}
			// VERSION ilegível não é motivo para parar a frota: o fallback é a
			// constante deste build, que é a versão do agente publicada no mesmo
			// deploy. Mas o valor lixo é descartado — jamais anunciado.
		}
	}
	if !versaoValida(versaoAgentePadrao) {
		return "", fmt.Errorf("versão do agente inválida no servidor: %q", versaoAgentePadrao)
	}
	return versaoAgentePadrao, nil
}

// VersaoMaisNova diz se `alvo` é estritamente mais nova que `atual`.
//
// "Estritamente" é a regra inteira. O painel nunca manda um agente voltar de
// versão (isso é decisão de operador, feita com pin) e nunca manda reinstalar a
// versão em execução — esta segunda parte importa mais do que parece: o agente
// estaga o binário e SAI para o systemd promovê-lo, então mandá-lo "atualizar"
// para a versão que ele já roda o põe num laço de reinício permanente, sem nunca
// mudar de versão. O host pararia de coletar e o sintoma no painel seria
// "servidor sumiu".
//
// O agente repete esta mesma checagem do lado dele (agent/internal/selfupdate).
// A duplicação é proposital: são duas partes com ciclos de deploy diferentes, e
// nenhuma das duas deve depender da outra estar correta para não se autodestruir.
func VersaoMaisNova(alvo, atual string) (bool, error) {
	a, err := partesDaVersao(alvo)
	if err != nil {
		return false, fmt.Errorf("versão publicada: %w", err)
	}
	b, err := partesDaVersao(atual)
	if err != nil {
		return false, fmt.Errorf("versão em execução: %w", err)
	}
	for i := 0; i < 3; i++ {
		if a[i] != b[i] {
			return a[i] > b[i], nil
		}
	}
	return false, nil
}

func partesDaVersao(v string) ([3]int, error) {
	var out [3]int
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if !versaoValida(v) {
		return out, fmt.Errorf("%q não está no formato MAIOR.MENOR.PATCH", v)
	}
	for i, p := range strings.Split(v, ".") {
		n := 0
		for _, c := range p {
			n = n*10 + int(c-'0')
		}
		out[i] = n
	}
	return out, nil
}

// versaoValida aceita apenas MAIOR.MENOR.PATCH numérico — o mesmo formato que o
// agente sabe comparar. Anunciar qualquer outra coisa faria todo agente da frota
// recusar a resposta e reportar erro, o que é ruído em vez de atualização.
func versaoValida(v string) bool {
	partes := strings.Split(v, ".")
	if len(partes) != 3 {
		return false
	}
	for _, p := range partes {
		if p == "" {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}
