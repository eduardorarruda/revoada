package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Cada teste aqui existe para provar UMA garantia que, se quebrada, transforma a
// auto-atualização de conserto em incidente. Nenhum deles testa o caminho feliz
// por si: o caminho feliz é testado como controle, para que as recusas signifiquem
// alguma coisa.

// binarioFalso devolve o conteúdo de um "agente" que responde `version` com a
// versão pedida. É um script /bin/sh porque o que está sob teste é a checagem de
// sanidade — "o arquivo baixado executa e se identifica?" —, e um script cumpre
// esse contrato sem precisar compilar um binário Go a cada caso.
func binarioFalso(versao string) []byte {
	return []byte("#!/bin/sh\necho \"revoada-agent " + versao + "\"\n")
}

func soma(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// painelFalso sobe um painel de mentira: responde a consulta com o que o teste
// mandar e serve o artefato em /revoada-agent.
type painelFalso struct {
	*httptest.Server
	artefato []byte
	// resposta é montada a partir da URL real do servidor (só conhecida depois de
	// subir), por isso é uma função.
	resposta    func(base string) Resposta
	consultas   int
	statusFalha int
	// aoBaixar é chamado quando o artefato é pedido. Serve para provar o que NÃO
	// deve acontecer (um host sem promotor não pode gastar a banda do cliente).
	aoBaixar func()
	// redirecionarPara, se preenchido, faz o artefato responder 302 para lá. É como
	// o teste do vazamento de serverkey leva o agente para outra origem.
	redirecionarPara string
}

func novoPainel(t *testing.T, artefato []byte, resposta func(base string) Resposta) *painelFalso {
	t.Helper()
	p := &painelFalso{artefato: artefato, resposta: resposta}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/agent/update-check", func(w http.ResponseWriter, r *http.Request) {
		p.consultas++
		if p.statusFalha != 0 {
			http.Error(w, "indisponível", p.statusFalha)
			return
		}
		if r.Header.Get("X-Revoada-Key") == "" {
			t.Errorf("consulta chegou sem a chave no cabeçalho X-Revoada-Key")
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(p.resposta(strings.TrimRight(p.URL, "/")))
	})
	mux.HandleFunc("/revoada-agent", func(w http.ResponseWriter, r *http.Request) {
		if p.aoBaixar != nil {
			p.aoBaixar()
		}
		if p.redirecionarPara != "" {
			http.Redirect(w, r, p.redirecionarPara, http.StatusFound)
			return
		}
		_, _ = w.Write(p.artefato)
	})
	p.Server = httptest.NewServer(mux)
	t.Cleanup(p.Close)
	return p
}

// novoAtualizador monta um atualizador apontado para o painel falso, num
// diretório de estágio descartável.
func novoAtualizador(t *testing.T, p *painelFalso, versaoAtual string) (*Atualizador, *bool) {
	t.Helper()
	dir := t.TempDir()
	pediuReinicio := false
	a := New(Config{
		PanelURL:  p.URL, // httptest é http://127.0.0.1:porta — laço local, aceito de propósito
		Key:       "chave-de-teste",
		Versao:    versaoAtual,
		Dir:       dir,
		Intervalo: time.Hour,
		Modo:      ModoSystemd,
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), func() { pediuReinicio = true })
	return a, &pediuReinicio
}

func estagiado(a *Atualizador) bool {
	_, err := os.Stat(filepath.Join(a.cfg.Dir, arqNovo))
	return err == nil
}

// ─────────────────────────────────────────────────────────────────────────────

// PROVA: checksum divergente nunca vira binário estagiado, e nunca vira reinício.
//
// Este é o teste que separa "auto-atualização" de "executar o que chegar pelo
// fio". Se ele passar a falhar, o agente instala artefato corrompido, artefato
// meio publicado no meio de um deploy e artefato adulterado no caminho — os três
// são indistinguíveis daqui, e os três terminam num agente que não é o que o
// painel acha que é.
func TestChecksumInvalidoNaoPromove(t *testing.T) {
	pularSeSemShell(t)
	bin := binarioFalso("0.9.0")
	p := novoPainel(t, bin, func(base string) Resposta {
		return Resposta{
			Atualizar: true, Versao: "0.9.0",
			URL: base + "/revoada-agent",
			// Checksum de OUTRO conteúdo: é assim que um artefato trocado se parece.
			SHA256:  soma([]byte("outra coisa qualquer")),
			Tamanho: int64(len(bin)),
		}
	})
	a, pediuReinicio := novoAtualizador(t, p, "0.8.0")

	a.tick(context.Background())

	if estagiado(a) {
		t.Fatal("binário com checksum errado foi estagiado — o promotor root o instalaria")
	}
	if *pediuReinicio {
		t.Fatal("pediu reinício do serviço depois de um checksum divergente")
	}
	if e := a.ultimoEstado(); e == nil || e.Estado != "erro_download" {
		t.Fatalf("estado reportado ao painel = %+v, queria erro_download", e)
	}
}

// PROVA: um binário íntegro que NÃO EXECUTA não é promovido.
//
// Checksum prova integridade, não funcionamento. Um artefato compilado para
// outra arquitetura, ligado a uma libc que o host não tem, ou truncado ainda na
// origem passa no SHA-256 e não roda. Promovê-lo troca "host desatualizado" por
// "host que não sobe" — e, com Restart=always, por restart-loop.
func TestBinarioQueNaoExecutaNaoPromove(t *testing.T) {
	pularSeSemShell(t)
	// Bytes que não são executável nenhum, mas cujo checksum bate certinho.
	lixo := []byte("\x00\x01isto nao e um executavel\x02\x03")
	p := novoPainel(t, lixo, func(base string) Resposta {
		return Resposta{
			Atualizar: true, Versao: "0.9.0",
			URL: base + "/revoada-agent", SHA256: soma(lixo), Tamanho: int64(len(lixo)),
		}
	})
	a, pediuReinicio := novoAtualizador(t, p, "0.8.0")

	a.tick(context.Background())

	if estagiado(a) {
		t.Fatal("binário que não executa foi estagiado")
	}
	if *pediuReinicio {
		t.Fatal("pediu reinício para um binário que não executa")
	}
}

// PROVA: um binário que executa mas se identifica com OUTRA versão não é
// promovido. É o caso do dist meio sincronizado — o painel anuncia 0.9.0 e o
// arquivo servido ainda é o antigo. Sem esta checagem, o painel passaria a exibir
// uma frota "atualizada" que não está.
func TestVersaoDivergenteNaoPromove(t *testing.T) {
	pularSeSemShell(t)
	bin := binarioFalso("0.8.5") // o painel vai dizer que é 0.9.0
	p := novoPainel(t, bin, func(base string) Resposta {
		return Resposta{
			Atualizar: true, Versao: "0.9.0",
			URL: base + "/revoada-agent", SHA256: soma(bin), Tamanho: int64(len(bin)),
		}
	})
	a, pediuReinicio := novoAtualizador(t, p, "0.8.0")

	a.tick(context.Background())

	if estagiado(a) {
		t.Fatal("binário que se identifica com outra versão foi estagiado")
	}
	if *pediuReinicio {
		t.Fatal("pediu reinício para um binário de versão divergente")
	}
}

// PROVA: o agente nunca faz downgrade, mesmo que o painel mande.
//
// Um dist com artefato antigo republicado (rollback de deploy) faria a frota
// inteira voltar sozinha. Voltar versão é decisão de operador, feita com pin.
func TestDowngradeRecusado(t *testing.T) {
	bin := binarioFalso("0.7.0")
	p := novoPainel(t, bin, func(base string) Resposta {
		return Resposta{
			Atualizar: true, Versao: "0.7.0",
			URL: base + "/revoada-agent", SHA256: soma(bin), Tamanho: int64(len(bin)),
		}
	})
	a, pediuReinicio := novoAtualizador(t, p, "0.8.0")

	a.tick(context.Background())

	if estagiado(a) {
		t.Fatal("aceitou estagiar uma versão mais velha que a em execução")
	}
	if *pediuReinicio {
		t.Fatal("pediu reinício para um downgrade")
	}
	if e := a.ultimoEstado(); e == nil || e.Estado != "recusado_downgrade" {
		t.Fatalf("estado = %+v, queria recusado_downgrade", e)
	}
}

// PROVA: "atualizar" para a MESMA versão é recusado.
//
// Sem esta recusa o agente entra em laço permanente: estaga, sai, o systemd o
// sobe na mesma versão, ele estaga de novo. O host pararia de coletar sem nunca
// mudar de versão — e o sintoma no painel seria "servidor sumiu".
func TestMesmaVersaoRecusada(t *testing.T) {
	bin := binarioFalso("0.8.0")
	p := novoPainel(t, bin, func(base string) Resposta {
		return Resposta{
			Atualizar: true, Versao: "0.8.0",
			URL: base + "/revoada-agent", SHA256: soma(bin), Tamanho: int64(len(bin)),
		}
	})
	a, pediuReinicio := novoAtualizador(t, p, "0.8.0")

	a.tick(context.Background())

	if estagiado(a) || *pediuReinicio {
		t.Fatal("aceitou reinstalar a versão que já está rodando")
	}
}

// PROVA (controle): o caminho feliz de fato estaga, grava o .meta que o promotor
// root lê, e pede o reinício. Sem este teste, todos os anteriores passariam com
// uma implementação que nunca atualiza nada.
func TestCaminhoFelizEstagiaEPedeReinicio(t *testing.T) {
	pularSeSemShell(t)
	bin := binarioFalso("0.9.0")
	p := novoPainel(t, bin, func(base string) Resposta {
		return Resposta{
			Atualizar: true, Versao: "0.9.0",
			URL: base + "/revoada-agent", SHA256: soma(bin), Tamanho: int64(len(bin)),
		}
	})
	a, pediuReinicio := novoAtualizador(t, p, "0.8.0")

	a.tick(context.Background())

	if !estagiado(a) {
		t.Fatal("não estagiou o binário no caminho feliz")
	}
	if !*pediuReinicio {
		t.Fatal("não pediu o reinício — sem isso o systemd nunca promove o binário")
	}
	meta, err := os.ReadFile(filepath.Join(a.cfg.Dir, arqMeta))
	if err != nil {
		t.Fatalf("o .meta que o promotor root lê não foi escrito: %v", err)
	}
	if !strings.Contains(string(meta), "sha256="+soma(bin)) || !strings.Contains(string(meta), "versao=0.9.0") {
		t.Fatalf(".meta não tem o contrato esperado pelo promotor: %q", meta)
	}
}

// PROVA: no ModoEstagiar o agente NÃO baixa, NÃO estaga e NÃO reinicia — ele
// relata que este host não se atualiza sozinho.
//
// É o modo de cron, launchd, Windows e systemd < 231, onde não existe promotor
// root. O comportamento antigo (baixar e deixar pronto "para o install.sh
// promover") prometia uma promoção que nunca vinha — o install.sh não olha o
// estágio, e não pode olhar, porque ele acabou de instalar o binário certo.
// O saldo, no host de produção em cron, era 12 MB parados para sempre no disco do
// cliente e um estado de ERRO permanente mandando reinstalar uma unit inexistente.
func TestModoEstagiarNaoBaixaNemReinicia(t *testing.T) {
	pularSeSemShell(t)
	bin := binarioFalso("0.9.0")
	p := novoPainel(t, bin, func(base string) Resposta {
		return Resposta{
			Atualizar: true, Versao: "0.9.0",
			URL: base + "/revoada-agent", SHA256: soma(bin), Tamanho: int64(len(bin)),
		}
	})
	baixou := false
	p.aoBaixar = func() { baixou = true }
	a, pediuReinicio := novoAtualizador(t, p, "0.8.0")
	a.cfg.Modo = ModoEstagiar

	a.tick(context.Background())

	if baixou {
		t.Fatal("baixou o binário num host que não tem quem o promova — é banda e disco do cliente gastos para nada")
	}
	if estagiado(a) {
		t.Fatal("estagiou um binário que ninguém vai promover")
	}
	if *pediuReinicio {
		t.Fatal("reiniciou o serviço num host que não tem quem promova o binário")
	}
	e := a.ultimoEstado()
	if e == nil || e.Estado != "sem_promotor" {
		t.Fatalf("o painel precisa saber POR QUE este host não sai da versão antiga; estado=%+v", e)
	}
	if e.VersaoDesejada != "0.9.0" {
		t.Fatalf("o relato tem de dizer qual versão o host deveria ter: %+v", e)
	}
}

// PROVA: se o binário já foi estagiado e o serviço continua na versão antiga, o
// agente PARA de tentar.
//
// Este é o teste do pior defeito que este pacote pode causar. Se o promotor root
// não está agindo (unit sem ExecStartPre, systemd velho demais para o prefixo
// `+`), insistir transforma a atualização num laço de reinício: o host deixa de
// coletar e o painel mostra "servidor sumiu", não "atualização falhou".
func TestNaoInsisteQuandoAPromocaoNaoAcontece(t *testing.T) {
	pularSeSemShell(t)
	bin := binarioFalso("0.9.0")
	p := novoPainel(t, bin, func(base string) Resposta {
		return Resposta{
			Atualizar: true, Versao: "0.9.0",
			URL: base + "/revoada-agent", SHA256: soma(bin), Tamanho: int64(len(bin)),
		}
	})
	a, pediuReinicio := novoAtualizador(t, p, "0.8.0")

	// Cada tick simula um ciclo "estagiou, saiu, voltou na MESMA versão velha".
	for i := 0; i < maxEstagiosPorVersao; i++ {
		*pediuReinicio = false
		a.tick(context.Background())
		if !*pediuReinicio {
			t.Fatalf("tick %d: deveria ter pedido reinício", i+1)
		}
	}

	*pediuReinicio = false
	a.tick(context.Background())
	if *pediuReinicio {
		t.Fatal("insistiu depois do teto de estágios — é assim que o host entra em laço de reinício e para de coletar")
	}
	if e := a.ultimoEstado(); e == nil || e.Estado != "promocao_nao_ocorreu" {
		t.Fatalf("estado = %+v, queria promocao_nao_ocorreu (o painel precisa ver isso)", e)
	}
}

// PROVA: o jitter existe e espalha de verdade.
//
// Sem jitter, a frota que volta junta de uma queda consulta e BAIXA no mesmo
// segundo, contra o mesmo painel. Nesta base já se mediu o retorno de uma queda
// gerar 105× o regime de escrita com UM agente; multiplicado pela frota, é
// derrubar o painel na hora em que ele mais precisa responder.
func TestJitterEspalhaAsConsultas(t *testing.T) {
	a := New(Config{Dir: t.TempDir(), Intervalo: time.Hour}, slog.New(slog.NewTextHandler(io.Discard, nil)), func() {})

	vistos := map[time.Duration]bool{}
	for i := 0; i < 200; i++ {
		d := a.jitter(time.Hour)
		if d < 30*time.Minute || d >= 90*time.Minute {
			t.Fatalf("jitter devolveu %v, fora da faixa de 50%%–150%% do intervalo", d)
		}
		vistos[d] = true
	}
	// Um "jitter" constante passaria na faixa acima e não espalharia nada — é
	// exatamente o modo de falha que interessa pegar aqui.
	if len(vistos) < 100 {
		t.Fatalf("só %d esperas distintas em 200 sorteios: isto não espalha a frota", len(vistos))
	}
}

// PROVA: a primeira consulta NÃO acontece no instante em que o agente sobe.
//
// É o momento crítico: numa volta de queda, a frota inteira sobe junta. Se a
// primeira consulta fosse imediata, o jitter dos ciclos seguintes não salvaria
// ninguém do primeiro impacto.
func TestPrimeiraConsultaNaoEImediata(t *testing.T) {
	bin := binarioFalso("0.9.0")
	p := novoPainel(t, bin, func(base string) Resposta { return Resposta{Atualizar: false, Motivo: "em dia"} })
	a, _ := novoAtualizador(t, p, "0.8.0")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	a.Run(ctx) // Intervalo de 1h => a menor espera sorteada é 30 min

	if p.consultas != 0 {
		t.Fatalf("consultou %d vez(es) logo na largada; a espera inicial precisa ser sorteada", p.consultas)
	}
}

// PROVA: nenhuma falha do atualizador interrompe a coleta.
//
// A garantia concreta é: tick() sempre retorna (nunca entra em pânico, nunca
// bloqueia, nunca chama os.Exit) e nunca pede reinício quando algo deu errado.
// Um host defasado é um problema; um host que parou de coletar por causa do
// atualizador é um host cego, que é estritamente pior.
func TestFalhaNuncaInterrompeAColeta(t *testing.T) {
	bin := binarioFalso("0.9.0")

	casos := []struct {
		nome     string
		resposta func(base string) Resposta
		status   int
	}{
		{nome: "painel fora do ar", status: http.StatusBadGateway,
			resposta: func(base string) Resposta { return Resposta{} }},
		{nome: "resposta sem checksum", resposta: func(base string) Resposta {
			return Resposta{Atualizar: true, Versao: "0.9.0", URL: base + "/revoada-agent", Tamanho: int64(len(bin))}
		}},
		{nome: "versao anunciada e lixo", resposta: func(base string) Resposta {
			return Resposta{Atualizar: true, Versao: "<!doctype html>", URL: base + "/revoada-agent", SHA256: soma(bin), Tamanho: int64(len(bin))}
		}},
		{nome: "tamanho anunciado errado", resposta: func(base string) Resposta {
			return Resposta{Atualizar: true, Versao: "0.9.0", URL: base + "/revoada-agent", SHA256: soma(bin), Tamanho: 999999}
		}},
		{nome: "binario apontado para outra origem", resposta: func(base string) Resposta {
			return Resposta{Atualizar: true, Versao: "0.9.0", URL: "https://cdn-de-outra-pessoa.example/agent", SHA256: soma(bin), Tamanho: int64(len(bin))}
		}},
		{nome: "url do binario vazia", resposta: func(base string) Resposta {
			return Resposta{Atualizar: true, Versao: "0.9.0", SHA256: soma(bin), Tamanho: int64(len(bin))}
		}},
	}

	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			p := novoPainel(t, bin, c.resposta)
			p.statusFalha = c.status
			a, pediuReinicio := novoAtualizador(t, p, "0.8.0")

			// O teste falha por timeout do `go test` se tick() bloquear, e por pânico
			// não recuperado se ele entrar em pânico. Os dois são o que se quer pegar.
			a.tick(context.Background())

			if *pediuReinicio {
				t.Fatalf("pediu reinício apesar da falha (%s)", c.nome)
			}
			if estagiado(a) {
				t.Fatalf("estagiou um binário apesar da falha (%s)", c.nome)
			}
			if a.ultimoEstado() == nil {
				t.Fatalf("não registrou o estado da falha — o painel ficaria sem saber (%s)", c.nome)
			}
		})
	}
}

// PROVA: um pânico dentro do atualizador não derruba o processo do agente.
// Run() é a fronteira; se ela deixar um pânico escapar, a coleta morre junto.
func TestPanicoNoAtualizadorNaoDerrubaOProcesso(t *testing.T) {
	pularSeSemShell(t)
	bin := binarioFalso("0.9.0")
	p := novoPainel(t, bin, func(base string) Resposta {
		return Resposta{
			Atualizar: true, Versao: "0.9.0",
			URL: base + "/revoada-agent", SHA256: soma(bin), Tamanho: int64(len(bin)),
		}
	})
	a, _ := novoAtualizador(t, p, "0.8.0")
	// Intervalo mínimo para o laço chegar ao tick sem esperar o sorteio de uma hora.
	a.cfg.Intervalo = time.Millisecond
	// Simula o pior: algo no caminho de atualização estoura de verdade. Se Run
	// deixar isso escapar, o processo do agente morre — e com ele a coleta.
	a.reiniciar = func() { panic("explosão de propósito") }

	pronto := make(chan struct{})
	go func() {
		// Sem recover aqui: um pânico que escapasse de Run derrubaria a goroutine e
		// a suíte inteira, que é exatamente o sintoma em produção.
		defer close(pronto)
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		a.Run(ctx)
	}()

	select {
	case <-pronto:
		// Run voltou por conta própria: o pânico foi contido lá dentro.
	case <-time.After(5 * time.Second):
		t.Fatal("Run não retornou — o atualizador travou o processo")
	}
}

// PROVA: `panel_url` em http puro desliga a auto-atualização (exceto no laço
// local). O que desce por esse cano vira código executado como root no reinício
// seguinte; por http, quem está no caminho troca o binário E o checksum juntos, e
// verificar o checksum que o atacante mandou não prova nada.
func TestHTTPPuroRecusado(t *testing.T) {
	a := New(Config{PanelURL: "http://painel.exemplo", Dir: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)), func() {})
	if _, err := a.baseValida(); err == nil {
		t.Fatal("aceitou panel_url em http puro para um host remoto")
	}
	b := New(Config{PanelURL: "http://127.0.0.1:8080", Dir: t.TempDir()}, slog.New(slog.NewTextHandler(io.Discard, nil)), func() {})
	if _, err := b.baseValida(); err != nil {
		t.Fatalf("recusou o laço local, onde não há caminho para alguém estar no meio: %v", err)
	}
}

func TestParseECompararVersao(t *testing.T) {
	for _, ruim := range []string{"", "latest", "0.9", "0.9.0-rc1", "v0.9.0.1", "<!doctype html>", "a.b.c"} {
		if _, err := parseVersao(ruim); err == nil {
			t.Errorf("aceitou %q como versão — um parser permissivo trocaria o binário com base em lixo", ruim)
		}
	}
	casos := []struct {
		alvo, atual string
		quer        bool
	}{
		{"0.9.0", "0.8.0", true},
		{"1.0.0", "0.9.9", true},
		{"0.8.1", "0.8.0", true},
		{"0.8.0", "0.8.0", false},
		{"0.7.9", "0.8.0", false},
		{"0.8.0", "0.9.0", false},
	}
	for _, c := range casos {
		got, err := maisNovaQue(c.alvo, c.atual)
		if err != nil {
			t.Fatalf("%s vs %s: %v", c.alvo, c.atual, err)
		}
		if got != c.quer {
			t.Errorf("maisNovaQue(%s, %s) = %v, queria %v", c.alvo, c.atual, got, c.quer)
		}
	}
}

func TestVersaoDaSaida(t *testing.T) {
	if v := versaoDaSaida("revoada-agent 0.9.0\n"); v != "0.9.0" {
		t.Errorf("versaoDaSaida = %q", v)
	}
	if v := versaoDaSaida("erro: config não encontrada"); v != "" {
		t.Errorf("aceitou saída de erro como versão: %q", v)
	}
}

// pularSeSemShell pula os casos que dependem de EXECUTAR o artefato baixado.
// A checagem de sanidade roda o binário de verdade; onde isso não é possível
// (Windows na suíte, /tmp montado noexec) o teste não teria o que provar.
func pularSeSemShell(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("a checagem de sanidade executa o artefato; o script de teste é /bin/sh")
	}
	f := filepath.Join(t.TempDir(), "exec-probe")
	if err := os.WriteFile(f, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Skip("não consegui preparar o teste de execução")
	}
	if err := exec.Command(f).Run(); err != nil {
		t.Skipf("o diretório temporário não permite execução (noexec?): %v", err)
	}
}

// ─── vazamento da serverkey por redirect ─────────────────────────────────────

// PROVA: um redirect para outra origem NÃO leva o cabeçalho X-Revoada-Key junto.
//
// O defeito medido: o download usava o http.DefaultClient, que segue redirects. A
// trava `mesmaOrigem` valida só a URL que veio no JSON, e o 302 acontece depois
// disso. O Go remove `Authorization` e `Cookie` ao trocar de host, mas não remove
// cabeçalhos próprios — então a serverkey deste servidor chegava em claro a quem
// respondesse o redirect. Com um servidor isolado no lugar do destino, a outra
// origem recebeu a chave no primeiro salto.
//
// A chave é a identidade do servidor no painel: quem a tem escreve métricas e logs
// como se fosse ele. Vazá-la é estritamente pior do que não atualizar.
func TestRedirectParaOutraOrigemNaoVazaAServerkey(t *testing.T) {
	pularSeSemShell(t)
	bin := binarioFalso("0.9.0")

	var vazou string
	var pedidos int
	outra := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pedidos++
		vazou = r.Header.Get("X-Revoada-Key")
		_, _ = w.Write(bin)
	}))
	t.Cleanup(outra.Close)

	p := novoPainel(t, bin, func(base string) Resposta {
		return Resposta{
			Atualizar: true, Versao: "0.9.0",
			URL: base + "/revoada-agent", SHA256: soma(bin), Tamanho: int64(len(bin)),
		}
	})
	p.redirecionarPara = outra.URL + "/revoada-agent"

	a, pediuReinicio := novoAtualizador(t, p, "0.8.0")
	a.tick(context.Background())

	if vazou != "" {
		t.Fatalf("a serverkey deste servidor chegou em claro a outra origem: %q", vazou)
	}
	if pedidos != 0 {
		t.Fatalf("o agente seguiu o redirect para fora da origem do painel (%d pedido(s))", pedidos)
	}
	if estagiado(a) || *pediuReinicio {
		t.Fatal("estagiou/reiniciou com um artefato vindo de origem não autorizada")
	}
	if e := a.ultimoEstado(); e == nil || e.Estado != "erro_download" {
		t.Fatalf("o painel precisa ver a recusa; estado=%+v", e)
	}
}

// PROVA (controle): redirect DENTRO da mesma origem continua funcionando.
//
// Sem este controle, a correção acima poderia ter sido "não siga redirect nenhum" —
// e aí uma normalização banal do painel (`/dist` → `/dist/`, um proxy que reescreve
// caminho) quebraria a atualização da frota inteira sem ninguém entender por quê.
func TestRedirectNaMesmaOrigemContinuaFuncionando(t *testing.T) {
	pularSeSemShell(t)
	bin := binarioFalso("0.9.0")
	var chaveNoDestino string
	p := novoPainel(t, bin, func(base string) Resposta {
		return Resposta{
			Atualizar: true, Versao: "0.9.0",
			URL: base + "/revoada-agent", SHA256: soma(bin), Tamanho: int64(len(bin)),
		}
	})
	// Destino do redirect no MESMO servidor.
	p.Config.Handler.(*http.ServeMux).HandleFunc("/dist/revoada-agent", func(w http.ResponseWriter, r *http.Request) {
		chaveNoDestino = r.Header.Get("X-Revoada-Key")
		_, _ = w.Write(bin)
	})
	p.redirecionarPara = "/dist/revoada-agent"

	a, pediuReinicio := novoAtualizador(t, p, "0.8.0")
	a.tick(context.Background())

	if !estagiado(a) {
		t.Fatal("um redirect na mesma origem não pode impedir a atualização")
	}
	if !*pediuReinicio {
		t.Fatal("não pediu o reinício depois de estagiar")
	}
	if chaveNoDestino == "" {
		t.Fatal("a chave precisa continuar chegando ao destino DENTRO da origem do painel — é o que autentica o download")
	}
}

// ─── prova de trabalho para declarar a versão sã ─────────────────────────────

// atualizadorDeSaude devolve um atualizador com os prazos de saúde encolhidos, para
// o teste não esperar os 5 minutos reais.
func atualizadorDeSaude(t *testing.T, minCiclos int64) *Atualizador {
	t.Helper()
	a := New(Config{
		PanelURL: "https://painel.exemplo", Key: "k", Versao: "0.9.0", Dir: t.TempDir(),
	}, slog.New(slog.NewTextHandler(io.Discard, nil)), func() {})
	a.piso = 5 * time.Millisecond
	a.cheque = time.Millisecond
	a.teto = time.Hour
	a.minCiclos = minCiclos
	return a
}

func temMarcaDeSaude(a *Atualizador) bool {
	_, err := os.Stat(filepath.Join(a.cfg.Dir, arqSaudavel))
	return err == nil
}

// PROVA: "de pé" não é suficiente — a marca de saúde exige ciclos ACEITOS.
//
// O critério antigo era só "ficou 2 minutos de pé". Uma versão que estoura no
// minuto 3 (OOM contra o MemoryMax=256M da unit, erro no primeiro ciclo de
// containers de um host cheio) gravava `saudavel` ANTES de morrer. Como é
// justamente a ausência dessa marca que autoriza o promotor root a desfazer, o
// desfazimento nunca acontecia: o host entrava em restart-loop permanente e, como o
// agente nunca vivia os 30+ minutos do jitter mínimo, nunca mais consultava o painel
// para pegar a correção. Voltava a exigir o SSH que este pacote existe para eliminar.
func TestSaudeExigeCiclosAceitos(t *testing.T) {
	a := atualizadorDeSaude(t, 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pronto := make(chan struct{})
	go func() { a.marcarSaudavelDepois(ctx); close(pronto) }()

	// Passado o piso de tempo e sem nenhum ciclo aceito, a marca NÃO pode existir.
	time.Sleep(40 * time.Millisecond)
	if temMarcaDeSaude(a) {
		t.Fatal("marcou saúde só por ter ficado de pé — é o defeito que este teste existe para pegar")
	}

	a.CicloEnviado()
	a.CicloEnviado()
	time.Sleep(20 * time.Millisecond)
	if temMarcaDeSaude(a) {
		t.Fatal("marcou saúde antes de fechar os ciclos exigidos")
	}

	a.CicloEnviado()
	select {
	case <-pronto:
	case <-time.After(2 * time.Second):
		t.Fatal("não marcou saúde depois dos ciclos aceitos")
	}
	if !temMarcaDeSaude(a) {
		t.Fatal("os ciclos aceitos foram fechados e a marca não apareceu")
	}
}

// PROVA: uma versão que morre antes de fechar os ciclos NÃO deixa marca de saúde —
// que é o que permite ao promotor root desfazer a promoção.
func TestVersaoQueMorreCedoNaoMarcaSaude(t *testing.T) {
	a := atualizadorDeSaude(t, 20)
	ctx, cancel := context.WithCancel(context.Background())
	pronto := make(chan struct{})
	go func() { a.marcarSaudavelDepois(ctx); close(pronto) }()

	// O binário conseguiu alguns ciclos e então estourou (o ctx morre com o processo).
	time.Sleep(20 * time.Millisecond)
	a.CicloEnviado()
	a.CicloEnviado()
	cancel()

	select {
	case <-pronto:
	case <-time.After(2 * time.Second):
		t.Fatal("marcarSaudavelDepois não respeitou o cancelamento")
	}
	if temMarcaDeSaude(a) {
		t.Fatal("gravou saúde para uma versão que morreu antes de provar trabalho — o promotor nunca mais desfaria")
	}
}

// PROVA: gateway fora do ar por horas não pode fazer o promotor desfazer uma versão
// BOA.
//
// É a regressão que o critério novo poderia introduzir: um agente perfeito num host
// cujo gateway está inacessível nunca fecha um ciclo aceito, nunca marca saúde, e
// três reboots do host (manutenção, queda de energia) fariam o promotor restaurar o
// binário anterior. Depois do teto de tempo, a versão está provada por outro caminho.
func TestUptimeLongoMarcaSaudeMesmoSemEnvioAceito(t *testing.T) {
	a := atualizadorDeSaude(t, 1000) // nunca alcançável no tempo do teste
	a.teto = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pronto := make(chan struct{})
	go func() { a.marcarSaudavelDepois(ctx); close(pronto) }()
	select {
	case <-pronto:
	case <-time.After(2 * time.Second):
		t.Fatal("ficou preso esperando ciclos que nunca virão")
	}
	if !temMarcaDeSaude(a) {
		t.Fatal("um binário de pé há muito tempo precisa ser considerado são, senão o promotor desfaz uma versão boa por causa da rede")
	}
}

// ─── vigia de progresso do download ──────────────────────────────────────────

// PROVA: uma conexão que trava no meio do corpo é abortada, em vez de segurar o
// atualizador pelo prazo total.
//
// Só havia o teto total de 15 minutos, e ele não descreve o defeito: um servidor que
// entrega o cabeçalho e alguns bytes e depois cala prende o io.Copy os 15 minutos
// inteiros — com um `.parcial` de até 256 MB ocupando o disco do servidor do cliente.
// Encher o disco de um host monitorado é dano maior do que ficar uma versão atrás.
func TestVigiaAbortaDownloadQueParaDeProgredir(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })

	v := novoVigiaCom(cancel, 60*time.Millisecond, 5*time.Millisecond)
	defer v.parar()
	r := v.envolver(pr)

	go func() { _, _ = pw.Write([]byte("bytes iniciais")) }()
	buf := make([]byte, 64)
	if _, err := r.Read(buf); err != nil {
		t.Fatalf("a primeira leitura deveria funcionar: %v", err)
	}
	// A partir daqui ninguém escreve mais: é a conexão travada.
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("o download travado não foi abortado — o atualizador ficaria preso o prazo inteiro")
	}
	if !v.travou() {
		t.Fatal("o vigia precisa se identificar como a causa, senão o erro vira 'contexto cancelado' e some no log")
	}
}

// PROVA (controle): um download lento MAS que progride não é abortado.
func TestVigiaNaoAbortaDownloadLentoQueProgride(t *testing.T) {
	_, cancel := context.WithCancel(context.Background())
	defer cancel()
	pr, pw := io.Pipe()
	v := novoVigiaCom(cancel, 60*time.Millisecond, 5*time.Millisecond)
	defer v.parar()
	r := v.envolver(pr)

	fim := make(chan struct{})
	go func() {
		defer close(fim)
		for i := 0; i < 10; i++ {
			_, _ = pw.Write([]byte("x"))
			time.Sleep(15 * time.Millisecond)
		}
		_ = pw.Close()
	}()
	if _, err := io.Copy(io.Discard, r); err != nil {
		t.Fatalf("um download lento que progride não pode ser abortado: %v", err)
	}
	<-fim
	if v.travou() {
		t.Fatal("abortou um download que estava progredindo")
	}
}
