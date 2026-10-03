package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"regexp"
	"testing"
	"testing/fstest"
	"time"
)

func distArtefato(conteudo []byte, mod time.Time, extras map[string]string) fstest.MapFS {
	m := fstest.MapFS{
		"revoada-agent": &fstest.MapFile{Data: conteudo, ModTime: mod},
	}
	for k, v := range extras {
		m[k] = &fstest.MapFile{Data: []byte(v), ModTime: mod}
	}
	return m
}

// PROVA: o checksum sai do arquivo REALMENTE servido, não de metadado.
//
// Um checksum digitado à mão (ou copiado de um build anterior) diverge do
// artefato no primeiro deploy em que alguém esquece de atualizá-lo — e aí a frota
// inteira baixa, recusa e reporta erro para sempre, sem que ninguém entenda por
// quê. Calcular do arquivo torna essa classe de erro impossível.
func TestChecksumSaiDoArquivoServido(t *testing.T) {
	corpo := []byte("conteudo do binario do agente")
	a := NewArtefatos(distArtefato(corpo, time.Unix(1000, 0), nil))

	art, err := a.Para("linux", "amd64")
	if err != nil {
		t.Fatalf("Para: %v", err)
	}
	esperado := sha256.Sum256(corpo)
	if art.SHA256 != hex.EncodeToString(esperado[:]) {
		t.Fatalf("SHA256 = %s, queria %s", art.SHA256, hex.EncodeToString(esperado[:]))
	}
	if art.Tamanho != int64(len(corpo)) {
		t.Fatalf("Tamanho = %d, queria %d", art.Tamanho, len(corpo))
	}
	if art.Nome != "revoada-agent" {
		t.Fatalf("Nome = %q", art.Nome)
	}
}

// PROVA: publicar um binário novo invalida o cache sozinho.
//
// Se o cache fosse eterno, o deploy trocaria o artefato e o painel continuaria
// anunciando o checksum do anterior. Todo agente da frota baixaria o binário
// novo, calcularia um SHA-256 diferente do anunciado e abortaria — a
// auto-atualização pararia inteira, em silêncio, logo depois de um deploy bem
// sucedido.
func TestCacheDeChecksumSegueOArquivo(t *testing.T) {
	dist := distArtefato([]byte("versao antiga"), time.Unix(1000, 0), nil)
	a := NewArtefatos(dist)

	antes, err := a.Para("linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	// Deploy: conteúdo novo, modtime nova.
	dist["revoada-agent"] = &fstest.MapFile{Data: []byte("versao nova, maior"), ModTime: time.Unix(2000, 0)}

	depois, err := a.Para("linux", "amd64")
	if err != nil {
		t.Fatal(err)
	}
	if antes.SHA256 == depois.SHA256 {
		t.Fatal("o checksum não mudou depois de o artefato ser republicado — o cache está servindo o valor errado")
	}
	nova := sha256.Sum256([]byte("versao nova, maior"))
	if depois.SHA256 != hex.EncodeToString(nova[:]) {
		t.Fatalf("checksum depois do deploy = %s, queria o do arquivo novo", depois.SHA256)
	}
}

// PROVA: plataforma sem binário publicado devolve ErrArtefatoAusente, não um
// palpite. O caso real é linux/arm64: o `make agent` só compila linux/amd64.
// Apontar um ARM para o binário amd64 daria um download íntegro que não executa.
func TestPlataformaSemBinarioNaoRecebePalpite(t *testing.T) {
	a := NewArtefatos(distArtefato([]byte("x"), time.Unix(1, 0), nil))
	for _, c := range [][2]string{{"linux", "arm64"}, {"linux", "386"}, {"freebsd", "amd64"}, {"", ""}} {
		if _, err := a.Para(c[0], c[1]); !errors.Is(err, ErrArtefatoAusente) {
			t.Errorf("Para(%q,%q) err = %v, queria ErrArtefatoAusente", c[0], c[1], err)
		}
	}
}

func TestVersaoPreferOArquivoVERSION(t *testing.T) {
	a := NewArtefatos(distArtefato([]byte("x"), time.Unix(1, 0), map[string]string{"VERSION": "v1.2.3\n"}))
	v, err := a.Versao()
	if err != nil {
		t.Fatal(err)
	}
	if v != "1.2.3" {
		t.Fatalf("Versao = %q, queria 1.2.3 (com o 'v' removido)", v)
	}
}

// PROVA: um VERSION corrompido é DESCARTADO, nunca anunciado.
//
// Anunciar "latest" ou um pedaço de HTML como versão faria todo agente da frota
// recusar a resposta e reportar erro de hora em hora. Cair no fallback mantém a
// frota funcionando enquanto alguém conserta o dist.
func TestVERSIONCorrompidoCaiNoFallback(t *testing.T) {
	for _, lixo := range []string{"latest\n", "<!doctype html>\n", "\n", "0.9\n", "abc.def.ghi\n"} {
		a := NewArtefatos(distArtefato([]byte("x"), time.Unix(1, 0), map[string]string{"VERSION": lixo}))
		v, err := a.Versao()
		if err != nil {
			t.Fatalf("VERSION=%q: %v", lixo, err)
		}
		if v != versaoAgentePadrao {
			t.Errorf("VERSION=%q devolveu %q; queria o fallback %q", lixo, v, versaoAgentePadrao)
		}
	}
}

// PROVA: a constante de fallback do servidor bate com a versão do agente.
//
// Servidor e agente saem do mesmo commit, mas nada no compilador garante isso —
// são módulos Go separados. Se a constante ficar para trás, o painel anuncia uma
// versão antiga para uma frota que já a tem: ninguém atualiza e ninguém entende
// por quê (o dist não traz VERSION hoje; ver docs/auto-atualizacao-agentes.md).
func TestFallbackDeVersaoAcompanhaOAgente(t *testing.T) {
	b, err := os.ReadFile("../../../agent/cmd/revoada-agent/main.go")
	if err != nil {
		t.Skip("fonte do agente indisponível (build fora do repositório)")
	}
	m := regexp.MustCompile(`(?m)^var version = "([^"]+)"`).FindSubmatch(b)
	if m == nil {
		t.Skip("não encontrei a declaração de versão no main.go do agente")
	}
	if got := string(m[1]); got != versaoAgentePadrao {
		t.Fatalf("o agente está na %s e o fallback do servidor diz %s — atualize versaoAgentePadrao em artefato.go", got, versaoAgentePadrao)
	}
}

func TestVersaoMaisNova(t *testing.T) {
	casos := []struct {
		alvo, atual string
		quer        bool
	}{
		{"0.9.0", "0.8.0", true},
		{"1.0.0", "0.9.9", true},
		{"0.8.1", "0.8.0", true},
		{"0.8.0", "0.8.0", false}, // mesma versão: reinstalar seria laço de reinício
		{"0.7.0", "0.8.0", false}, // downgrade: decisão de operador, não do painel
	}
	for _, c := range casos {
		got, err := VersaoMaisNova(c.alvo, c.atual)
		if err != nil {
			t.Fatalf("%s vs %s: %v", c.alvo, c.atual, err)
		}
		if got != c.quer {
			t.Errorf("VersaoMaisNova(%s,%s) = %v, queria %v", c.alvo, c.atual, got, c.quer)
		}
	}
	for _, ruim := range []string{"", "latest", "0.9", "<!doctype html>"} {
		if _, err := VersaoMaisNova("0.9.0", ruim); err == nil {
			t.Errorf("aceitou %q como versão em execução", ruim)
		}
	}
}
