// Package selfupdate mantém o agente na versão que o painel publica, sem que
// alguém precise entrar por SSH em cada host.
//
// # Por que isto existe
//
// Correção de veracidade de métrica é inútil enquanto não chega à frota. Pior:
// enquanto metade dos hosts roda 0.7.x e metade roda 0.8.x, a mesma tela compara
// servidores que medem CPU de maneiras DIFERENTES — com a mesma cor e a mesma
// autoridade. Frota misturada não é atraso de release, é conclusão errada tomada
// com confiança.
//
// # A restrição que define o desenho
//
// O agente roda como o usuário `revoada` (não-root), numa unit systemd com
// ProtectSystem=strict, e o binário vive em /usr/local/bin/revoada-agent,
// que é de root. Logo o agente NÃO consegue sobrescrever o próprio binário —
// nem deveria: o dia em que ele conseguir, a superfície de ataque do agente
// passa a incluir "virar root".
//
// Então a troca é feita em duas mãos, e este pacote só executa a primeira:
//
//  1. O AGENTE (não-root) baixa o binário novo para o diretório de estado que
//     ele comprovadamente escreve, confere o SHA-256, confere que o binário
//     EXECUTA e anuncia a versão prometida, e então pede o próprio reinício.
//  2. O SYSTEMD (root), no ExecStartPre com prefixo `+`, promove o binário
//     estagiado para /usr/local/bin e o remove do estágio.
//  3. Restart=always reergue o serviço, já com o binário novo.
//
// O único componente privilegiado do caminho é o systemd — que já era o único
// componente privilegiado do serviço. Ver deploy/agent/promote-update.sh.
//
// # Best-effort absoluto
//
// Nenhuma falha daqui pode interromper a coleta. Todo caminho de erro deste
// pacote termina em "registra e tenta de novo depois"; nunca em `os.Exit`, nunca
// em pânico que suba, nunca em bloqueio do laço de coleta. Um host que não
// consegue atualizar é um host defasado; um host que parou de coletar por causa
// do atualizador é um host cego, e cego é estritamente pior.
package selfupdate

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"time"

	"github.com/eduardorarruda/revoada/agent/internal/selfuninstall"
)

// Modo diz o que o agente faz DEPOIS de estagiar um binário verificado.
type Modo string

const (
	// ModoEstagiar é o modo dos hosts SEM promotor root: cron, launchd, Windows,
	// systemd anterior ao 231. Nele o agente NÃO baixa e NÃO estaga nada — ele
	// apenas relata ao painel que este host não se atualiza sozinho.
	//
	// Por que não estagiar: durante um tempo o comentário aqui dizia "quem promove é
	// a próxima execução do install.sh", e isso nunca foi verdade — o install.sh não
	// olhava o estágio. O resultado, num host de produção em modo cron
	// (mail.exemplo.com.br), era 12 MB de binário parados para sempre no disco do
	// cliente e um `estado.json` travado em ERRO cuja mensagem mandava reinstalar a
	// unit systemd de um host que não tem unit nenhuma.
	//
	// Fazer o install.sh promover o estágio seria a outra saída possível, e ela é
	// INCOMPATÍVEL com a correção que o instalador precisa ter: ele acabou de instalar
	// o binário autoritativo, então promover um arquivo mais velho por cima é
	// exatamente o rebaixamento silencioso que o `rm -f` do estágio existe para
	// impedir (ver deploy/agent/install.sh, seção do UPDATE_DIR). Escolhida, então, a
	// saída coerente: nestes hosts a atualização é o próprio install.sh rodando de
	// novo — que baixa o binário certo do painel —, e o agente diz isso em voz alta
	// em vez de gastar a banda do cliente para nada.
	ModoEstagiar Modo = "stage"
	// ModoSystemd estagia e pede o próprio reinício, contando que o
	// ExecStartPre=+ da unit promova o binário. O install.sh só escreve este modo
	// no agent.yaml quando de fato instalou o promotor — ver a checagem de versão
	// do systemd lá. Escrever "systemd" num host onde o promotor não existe
	// produziria o pior defeito possível: o agente sai, sobe igual, sai de novo.
	ModoSystemd Modo = "systemd"
)

// Config é tudo que o atualizador precisa saber.
type Config struct {
	// PanelURL é a base do painel (https://painel.exemplo). Vazio desliga o
	// atualizador: sem saber a quem perguntar, não há o que fazer.
	PanelURL string
	// Key é a chave de ingestão deste servidor; autentica a consulta e é o que
	// permite ao painel fixar a versão DESTE host.
	Key string
	// Versao é a versão em execução — a que este binário reporta em `version`.
	Versao string
	// Hostname é o MESMO nome que o agente usa como rótulo `host` ao enviar métricas
	// (cfg.Hostname do agent.yaml). É o que amarra esta chave a uma linha do
	// inventário na tela de Atualização dos agentes.
	//
	// Sem ele o painel só tem `agents.hostname`, que é o apelido digitado por gente
	// ao criar a chave ("Loja Exemplo", "Teste traces") — medido em produção, nenhum dos
	// 8 apelidos casava com nenhum dos 5 servidores reais. Resultado: a tela não
	// consegue afirmar em QUAL servidor um freio de atualização vai agir, e um freio
	// que pode cair no servidor errado é pior do que não existir.
	Hostname string
	// Dir é o diretório de estágio, gravável pelo usuário do agente. É o mesmo
	// caminho literal que o promotor root conhece; o install.sh escreve os dois.
	Dir string
	// Intervalo entre consultas. <=0 usa o default de uma hora.
	Intervalo time.Duration
	// Modo é o que fazer depois de estagiar. Ver ModoEstagiar/ModoSystemd.
	Modo Modo
	// ConfigPath é o agent.yaml, repassado ao binário estagiado na checagem de
	// sanidade (`doctor`).
	ConfigPath string
}

// Atualizador roda o laço de verificação.
type Atualizador struct {
	cfg Config
	log *slog.Logger
	// reiniciar é como o atualizador pede a parada limpa do agente. É injetado
	// pelo main; este pacote nunca chama os.Exit por conta própria.
	reiniciar func()
	// agora e dormir existem para os testes controlarem o relógio sem esperar uma
	// hora de verdade.
	agora func() time.Time
	// ciclos conta os ciclos de coleta cujo envio o gateway ACEITOU nesta execução.
	// É a prova de trabalho que substitui o "ficou 2 minutos de pé" — ver
	// marcarSaudavelDepois. Alimentado por CicloEnviado, chamado pelo laço de coleta.
	ciclos atomic.Int64
	// piso/teto/cheque são injetáveis para o teste não esperar 5 minutos reais.
	piso, teto, cheque time.Duration
	// minCiclos é ciclosSaudavel, injetável pelo mesmo motivo.
	minCiclos int64
	// forcar antecipa a consulta fora do ciclo horário. Buffer 1: um pedido pendente
	// basta, e um canal cheio simplesmente descarta o pedido novo — o que já está lá
	// vai fazer exatamente o mesmo trabalho.
	forcar chan struct{}
	// ultimaForcada guarda quando a última consulta antecipada foi disparada, para o
	// limitador de ConsultarAgora.
	ultimaForcada atomic.Int64
}

// intervaloMinimoForcado é o quanto se espera entre duas consultas antecipadas. Sem
// ele, um agente cuja chave o gateway recusa a cada 15 s pediria consulta 240 vezes
// por hora — a frota inteira nessa situação (um painel reinstalado, por exemplo)
// viraria uma enxurrada exatamente sobre o serviço que está tentando se recuperar.
const intervaloMinimoForcado = 5 * time.Minute

// ConsultarAgora pede uma consulta fora do ciclo. Chamado quando o gateway RECUSA a
// chave: ou ela foi revogada (e pode haver uma ordem de desinstalação esperando), ou
// alguém a apagou — nos dois casos, esperar até uma hora para descobrir é tempo demais
// com um agente órfão martelando o gateway.
//
// Seguro em nil e concorrente: quem chama não precisa saber se o atualizador existe
// neste host.
func (a *Atualizador) ConsultarAgora() {
	if a == nil {
		return
	}
	agora := a.agora().UnixNano()
	ultima := a.ultimaForcada.Load()
	if ultima != 0 && time.Duration(agora-ultima) < intervaloMinimoForcado {
		return
	}
	if !a.ultimaForcada.CompareAndSwap(ultima, agora) {
		return // outra goroutine acabou de forçar; uma consulta basta
	}
	select {
	case a.forcar <- struct{}{}:
	default:
	}
}

// CicloEnviado é chamado pelo laço de coleta a cada ciclo cujo envio o gateway
// aceitou. É o único sinal de que ESTE binário está fazendo o trabalho para o qual
// existe — e é o que autoriza declarar a versão sã (ver marcarSaudavelDepois).
// Seguro em nil e concorrente: o chamador não deve precisar saber se o atualizador
// está ligado neste host.
func (a *Atualizador) CicloEnviado() {
	if a == nil {
		return
	}
	a.ciclos.Add(1)
}

// intervaloPadrao: de hora em hora. Não a cada ciclo de coleta (15s) — a
// pergunta "mudou a versão?" não muda 240 vezes por hora, e cada consulta é uma
// conexão a mais contra o painel vezes o tamanho da frota.
const intervaloPadrao = time.Hour

// maxEstagiosPorVersao é quantas vezes o agente aceita estagiar a MESMA versão
// antes de desistir dela.
//
// O defeito que este contador evita é o mais grave que este pacote pode causar:
// se o promotor root não estiver funcionando (unit sem o ExecStartPre, systemd
// antigo demais para o prefixo `+`, binário promovido mas sem permissão), o
// agente estaga, sai, sobe na MESMA versão velha, vê o mesmo alvo, estaga e sai
// de novo — para sempre. O host deixa de coletar e o sintoma no painel é
// "servidor sumiu", não "atualização falhou". Ao segundo estágio sem troca de
// versão, o agente para de tentar aquela versão e denuncia o estado ao painel.
const maxEstagiosPorVersao = 2

// ─── o que conta como "esta versão está sã" ──────────────────────────────────
//
// Durante um tempo o critério foi só "ficou 2 minutos de pé", e ficar de pé não é
// trabalho. O modo de falha que isso deixa passar é concreto e caro:
//
//   - uma versão que estoura contra o `MemoryMax=256M` da unit leva alguns minutos
//     para o heap chegar lá;
//   - uma versão que morre no primeiro ciclo de containers de um host com muitos
//     containers idem.
//
// Nos dois casos o agente escrevia `saudavel` ANTES de morrer. Como a marca de saúde
// é justamente o que desarma o desfazimento do promotor root, o promotor nunca mais
// voltava atrás: o host entrava em restart-loop permanente. E o restart-loop fecha a
// única saída que sobrava — o agente nunca vive os 30+ minutos do jitter mínimo, logo
// nunca chega a consultar o painel para pegar a correção. O host volta a exigir o SSH
// que esta função inteira existe para eliminar.
//
// Critério novo: PROVA DE TRABALHO. A versão só é declarada sã depois de fechar
// `ciclosSaudavel` ciclos de coleta cujo envio o gateway ACEITOU. Coletar e ser aceito
// é o que o agente existe para fazer; um binário que consegue fazer isso 20 vezes
// seguidas não é um binário que estoura no minuto 3.
const (
	// ciclosSaudavel: 20 ciclos = 5 minutos no intervalo padrão de 15s. Cobre com
	// folga a janela em que as falhas acima aparecem, sem esticar tanto a ponto de
	// um reboot legítimo no meio do caminho ser lido como fracasso.
	ciclosSaudavel = 20
	// pisoSaudavel é o chão de TEMPO, para o caso de `interval_seconds` pequeno: com
	// 1s de intervalo, 20 ciclos passariam em 20 segundos e o critério voltaria a ser
	// "ficou de pé um instante".
	pisoSaudavel = 5 * time.Minute
	// tetoSaudavel é a válvula de escape. Sem ela, este critério cria uma regressão
	// própria: um agente PERFEITO num host cujo gateway está fora nunca fecha um ciclo
	// aceito, nunca marca saúde — e três reboots do host (manutenção, queda de energia)
	// fariam o promotor desfazer uma versão boa. Depois de 6 horas de processo vivo, a
	// versão está provada por outro caminho: nenhum dos defeitos que este critério
	// pega sobrevive 6 horas de pé.
	tetoSaudavel = 6 * time.Hour
	// intervaloCheque é de quanto em quanto tempo o contador é olhado depois do piso.
	intervaloCheque = 15 * time.Second
)

// New monta o atualizador. Devolve nil (e explica no log) quando falta o
// essencial — o chamador simplesmente não sobe a goroutine.
func New(cfg Config, log *slog.Logger, reiniciar func()) *Atualizador {
	if cfg.Intervalo <= 0 {
		cfg.Intervalo = intervaloPadrao
	}
	if cfg.Modo == "" {
		cfg.Modo = ModoEstagiar
	}
	return &Atualizador{
		cfg: cfg, log: log, reiniciar: reiniciar, agora: time.Now, forcar: make(chan struct{}, 1),
		piso: pisoSaudavel, teto: tetoSaudavel, cheque: intervaloCheque, minCiclos: ciclosSaudavel,
	}
}

// Run roda até o contexto morrer. É best-effort do começo ao fim: qualquer erro
// vira log e nova tentativa no próximo intervalo.
func (a *Atualizador) Run(ctx context.Context) {
	// Rede de segurança de último recurso. Um pânico aqui dentro derrubaria o
	// processo inteiro e, com ele, a coleta — exatamente o que este pacote não
	// pode fazer. Melhor um host defasado do que um host cego.
	defer func() {
		if r := recover(); r != nil {
			a.log.Error("auto-atualização: pânico contido — a coleta segue, a atualização não", "panico", r)
		}
	}()

	if err := os.MkdirAll(a.cfg.Dir, 0o755); err != nil {
		a.log.Warn("auto-atualização desligada: não consigo escrever o diretório de estágio", "dir", a.cfg.Dir, "err", err)
		return
	}

	// Declara saúde depois de ficar de pé um tempo. Roda sempre, inclusive quando
	// a consulta ao painel está falhando: a marca de saúde é sobre ESTE binário
	// estar funcionando, não sobre o painel estar acessível.
	go a.marcarSaudavelDepois(ctx)

	// Primeira consulta com atraso aleatório dentro do intervalo inteiro.
	//
	// Isto é o "estouro da boiada" e não é hipótese: quando a frota volta junta de
	// uma queda, todo agente sobe no mesmo segundo. Sem espalhar, todos consultam
	// no mesmo instante e — pior — todos BAIXAM o mesmo binário de dezenas de MB
	// no mesmo instante, contra o mesmo painel. Nesta base já se mediu o retorno
	// de uma queda gerar 105× o regime de escrita com UM agente; multiplicar isso
	// pela frota é derrubar o painel na hora em que ele mais precisa responder.
	espera := a.jitter(a.cfg.Intervalo)
	a.log.Info("auto-atualização ativa", "intervalo", a.cfg.Intervalo, "primeira_consulta_em", espera.Round(time.Second), "modo", a.cfg.Modo)

	t := time.NewTimer(espera)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-a.forcar:
			a.log.Info("auto-atualização: consulta antecipada (o gateway recusou a chave)")
		}
		a.tick(ctx)
		// Cada intervalo é sorteado de novo. Um jitter só na largada faria a frota
		// reagrupar sozinha ao longo dos dias, porque todos andam com o mesmo passo
		// a partir de posições fixas.
		t.Reset(a.jitter(a.cfg.Intervalo))
	}
}

// jitter sorteia uma espera entre 50% e 150% do intervalo. Usa crypto/rand
// porque math/rand sem semente é idêntico em todo processo — e uma frota inteira
// sorteando o MESMO "aleatório" é precisamente a boiada que o jitter existe para
// evitar.
func (a *Atualizador) jitter(d time.Duration) time.Duration {
	meio := int64(d / 2)
	if meio <= 0 {
		return d
	}
	n, err := rand.Int(rand.Reader, big.NewInt(meio*2))
	if err != nil {
		return d
	}
	return time.Duration(meio + n.Int64())
}

// tick é uma rodada completa: pergunta, decide, baixa, verifica, estaga.
func (a *Atualizador) tick(ctx context.Context) {
	res, err := a.consultar(ctx)
	if err != nil {
		// Painel fora do ar, DNS ruim, proxy no meio: não é problema deste host e
		// não muda nada na coleta. Warn e vida que segue.
		a.log.Warn("auto-atualização: não consegui consultar o painel", "err", err)
		a.registrar(estado{Estado: "erro_consulta", Erro: err.Error()})
		return
	}
	// A ORDEM DE DESINSTALAÇÃO VEM ANTES DE TUDO. O servidor foi apagado do painel e
	// ninguém tinha SSH para tirar o agente da máquina; ele sai por conta própria.
	// Precede a checagem de versão porque quem vai embora não baixa binário novo.
	if res.Desinstalar {
		a.desinstalar(ctx, res.Motivo)
		return
	}

	if !res.Atualizar {
		a.log.Debug("auto-atualização: nada a fazer", "motivo", res.Motivo)
		a.registrar(estado{Estado: "em_dia", Motivo: res.Motivo, VersaoDesejada: res.Versao})
		return
	}

	// Segunda opinião sobre a decisão do servidor. O painel já filtrou, mas quem
	// arrisca o binário é este processo: um servidor com o dist meio sincronizado
	// (rollback de deploy, artefato antigo republicado) pode anunciar uma versão
	// mais VELHA como desejada. Downgrade automático não acontece por engano de
	// ninguém — voltar versão é decisão de operador, feita com pin.
	novo, err := maisNovaQue(res.Versao, a.cfg.Versao)
	if err != nil {
		a.log.Warn("auto-atualização: versão anunciada não faz sentido", "desejada", res.Versao, "atual", a.cfg.Versao, "err", err)
		a.registrar(estado{Estado: "erro", Erro: err.Error(), VersaoDesejada: res.Versao})
		return
	}
	if !novo {
		a.log.Warn("auto-atualização recusada: o painel anunciou uma versão que não é mais nova que a em execução — o agente nunca faz downgrade sozinho",
			"desejada", res.Versao, "atual", a.cfg.Versao)
		a.registrar(estado{Estado: "recusado_downgrade", VersaoDesejada: res.Versao})
		return
	}

	// PARA AQUI quando este host não tem quem promova o binário.
	//
	// Antes do download, de propósito. A versão anterior baixava e estagiava 12 MB
	// num host em modo cron e depois anunciava "aguardando promoção" — promoção que
	// nunca vinha, porque o install.sh não olha o estágio (e não pode olhar: ele
	// acabou de instalar o binário certo, promover um mais velho por cima seria
	// rebaixar o host). O saldo era banda do cliente gasta, 12 MB parados no disco
	// dele para sempre e um estado de ERRO permanente mandando reinstalar uma unit
	// systemd que este host não tem.
	//
	// O relato continua indo ao painel toda hora: é ele que faz a coluna de versão
	// deste host ser explicável ("precisa do instalador") em vez de só ficar atrasada.
	if a.cfg.Modo != ModoSystemd {
		a.log.Info("auto-atualização: há versão nova, mas este host não se atualiza sozinho (sem o promotor root: cron/launchd/systemd < 231). Rode o instalador do painel para trocar o binário — nada foi baixado.",
			"desejada", res.Versao, "atual", a.cfg.Versao, "modo", a.cfg.Modo)
		a.registrar(estado{Estado: "sem_promotor", VersaoDesejada: res.Versao,
			Motivo: "este host não atualiza sozinho; a troca é feita reexecutando o instalador"})
		return
	}

	// Já tentamos esta versão e continuamos na velha? Então o promotor não está
	// funcionando neste host. Insistir é transformar atualização em queda.
	ctrl, _ := a.lerControle()
	if ctrl.Versao == res.Versao && ctrl.Estagios >= maxEstagiosPorVersao {
		a.log.Error("auto-atualização parada: o binário já foi estagiado e o serviço continua na versão antiga — o promotor do systemd não está agindo neste host. Rode o install.sh novamente para reinstalar a unit.",
			"desejada", res.Versao, "atual", a.cfg.Versao, "estagios", ctrl.Estagios)
		a.registrar(estado{Estado: "promocao_nao_ocorreu", VersaoDesejada: res.Versao,
			Erro: "binário estagiado mas o serviço seguiu na versão antiga"})
		return
	}

	caminho, err := a.baixarEEstagiar(ctx, res)
	if err != nil {
		a.log.Warn("auto-atualização: o binário novo não passou na verificação — nada foi trocado, a coleta segue na versão atual",
			"desejada", res.Versao, "err", err)
		a.registrar(estado{Estado: "erro_download", Erro: err.Error(), VersaoDesejada: res.Versao})
		return
	}

	// Só continua contando quando é a MESMA versão: um alvo novo merece as suas
	// próprias tentativas, senão uma versão que falhou uma vez envenenaria a
	// contagem da seguinte.
	estagios := 1
	if ctrl.Versao == res.Versao {
		estagios = ctrl.Estagios + 1
	}
	if err := a.gravarControle(controle{Versao: res.Versao, Estagios: estagios}); err != nil {
		// Sem o contador não há proteção contra o laço de reinício. Preferimos não
		// reiniciar a reiniciar sem rede de segurança.
		a.log.Warn("auto-atualização: não consegui gravar o controle de estágio; não vou pedir reinício", "err", err)
		a.registrar(estado{Estado: "estagiado", VersaoDesejada: res.Versao, Erro: "controle de estágio não gravado"})
		return
	}

	a.log.Info("auto-atualização: binário novo verificado e estagiado", "versao", res.Versao, "arquivo", caminho, "modo", a.cfg.Modo)
	a.registrar(estado{Estado: "estagiado_reiniciando", VersaoDesejada: res.Versao})
	a.log.Info("auto-atualização: pedindo reinício do serviço para o systemd promover o binário novo", "versao", res.Versao)
	a.reiniciar()
}

// marcarSaudavelDepois declara este binário são depois de ele se manter de pé, e
// com isso desarma o desfazimento automático do promotor root.
//
// Enquanto a marca não existe, o promotor conta os reinícios; passando do teto,
// ele restaura o binário anterior. É o que impede uma versão que estoura no boot
// de virar uma frota inteira em restart-loop — o modo de falha em que a
// atualização derruba a coleta de todo mundo ao mesmo tempo.
func (a *Atualizador) marcarSaudavelDepois(ctx context.Context) {
	if !a.esperarSaude(ctx) {
		return
	}
	if err := os.WriteFile(filepath.Join(a.cfg.Dir, arqSaudavel), []byte(a.cfg.Versao+"\n"), 0o644); err != nil {
		a.log.Warn("auto-atualização: não consegui marcar esta versão como sã", "err", err)
		return
	}
	// A marca de saúde torna o contador de reinícios e o registro de promoção
	// obsoletos: se eles ficassem, um reinício comum (reboot, restart do
	// operador) seria contado como tentativa fracassada da promoção já concluída.
	_ = os.Remove(filepath.Join(a.cfg.Dir, arqTentativas))
	_ = os.Remove(filepath.Join(a.cfg.Dir, arqPromovido))

	// Chegamos vivos na versão que havíamos estagiado: o ciclo fechou.
	if ctrl, err := a.lerControle(); err == nil && ctrl.Versao == a.cfg.Versao {
		_ = os.Remove(filepath.Join(a.cfg.Dir, arqControle))
	}
	a.log.Info("auto-atualização: versão em execução marcada como sã", "versao", a.cfg.Versao,
		"ciclos_aceitos", a.ciclos.Load())
}

// esperarSaude bloqueia até esta versão ter PROVADO que funciona, e devolve false se
// o agente foi encerrado antes disso (aí nada é marcado, que é o certo: um agente que
// parou no meio do caminho não provou nada).
//
// A ordem é: espera o piso de tempo, depois confere o contador de ciclos aceitos a
// cada `cheque`, e desiste de esperar o contador quando o processo já vive há `teto`.
func (a *Atualizador) esperarSaude(ctx context.Context) bool {
	inicio := a.agora()
	select {
	case <-ctx.Done():
		return false
	case <-time.After(a.piso):
	}
	if a.ciclos.Load() >= a.minCiclos {
		return true
	}
	t := time.NewTicker(a.cheque)
	defer t.Stop()
	avisou := false
	for {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
		if a.ciclos.Load() >= a.minCiclos {
			return true
		}
		if a.agora().Sub(inicio) >= a.teto {
			// Chegou aqui: o binário está de pé há horas e mesmo assim nenhum envio foi
			// aceito. O problema não é a versão — é o gateway, a chave ou a rede. Marcar
			// saúde aqui é o certo: o que a marca protege é contra promover um binário
			// QUEBRADO, e um binário quebrado não fica 6 horas de pé.
			a.log.Warn("auto-atualização: nenhum ciclo de coleta foi aceito pelo gateway, mas esta versão está de pé há horas — marcando como sã para não desfazer uma promoção boa por causa de um problema de rede",
				"horas", a.teto, "ciclos_aceitos", a.ciclos.Load())
			return true
		}
		if !avisou {
			avisou = true
			a.log.Info("auto-atualização: aguardando ciclos de coleta aceitos antes de declarar esta versão sã",
				"necessarios", a.minCiclos, "ate_agora", a.ciclos.Load())
		}
	}
}

// ─── controle de estágio (arquivo local do agente) ───────────────────────────

type controle struct {
	Versao   string `json:"versao"`
	Estagios int    `json:"estagios"`
}

var errSemControle = errors.New("sem controle de estágio")

func (a *Atualizador) lerControle() (controle, error) {
	b, err := os.ReadFile(filepath.Join(a.cfg.Dir, arqControle))
	if err != nil {
		return controle{}, errSemControle
	}
	var c controle
	if err := json.Unmarshal(b, &c); err != nil {
		return controle{}, err
	}
	return c, nil
}

func (a *Atualizador) gravarControle(c controle) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	return gravarAtomico(filepath.Join(a.cfg.Dir, arqControle), b, 0o644)
}

// estado é o que o agente conta ao painel na PRÓXIMA consulta. Não há rota de
// "reportar" separada de propósito: um endpoint só, chamado de hora em hora,
// carrega a pergunta e a resposta da vez anterior. Menos superfície, menos
// tráfego, e o relato chega junto com a prova de que o agente ainda está vivo.
type estado struct {
	Estado         string    `json:"estado"`
	Motivo         string    `json:"motivo,omitempty"`
	Erro           string    `json:"erro,omitempty"`
	VersaoDesejada string    `json:"versao_desejada,omitempty"`
	Quando         time.Time `json:"quando"`
}

func (a *Atualizador) registrar(e estado) {
	e.Quando = a.agora().UTC()
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	// Falha ao registrar não é motivo para nada: o estado é observabilidade, não
	// parte do caminho de atualização.
	if err := gravarAtomico(filepath.Join(a.cfg.Dir, arqEstado), b, 0o644); err != nil {
		a.log.Debug("auto-atualização: não consegui gravar o último estado", "err", err)
	}
}

func (a *Atualizador) ultimoEstado() *estado {
	b, err := os.ReadFile(filepath.Join(a.cfg.Dir, arqEstado))
	if err != nil {
		return nil
	}
	var e estado
	if err := json.Unmarshal(b, &e); err != nil {
		return nil
	}
	return &e
}

// Nomes dos arquivos do diretório de estágio. Três deles são contrato com o
// promotor root (deploy/agent/promote-update.sh) — mudar aqui exige mudar lá.
const (
	arqNovo       = "revoada-agent.novo"      // contrato com o promotor
	arqMeta       = "revoada-agent.novo.meta" // contrato com o promotor
	arqSaudavel   = "saudavel"                // contrato com o promotor
	arqPromovido  = "promovido"               // escrito pelo promotor
	arqTentativas = "tentativas"              // escrito pelo promotor
	arqControle   = "controle.json"           // só do agente
	arqEstado     = "estado.json"             // só do agente
)

// plataforma identifica o artefato que este host precisa.
func plataforma() (string, string) { return runtime.GOOS, runtime.GOARCH }

func gravarAtomico(destino string, b []byte, modo os.FileMode) error {
	// Grava em temporário e renomeia: uma escrita interrompida (disco cheio, host
	// desligado no meio) deixaria um arquivo truncado que na leitura seguinte
	// viraria "controle inválido" — e, no caso do .meta, um checksum truncado que
	// o promotor root leria como divergência.
	tmp := destino + ".tmp"
	if err := os.WriteFile(tmp, b, modo); err != nil {
		return err
	}
	if err := os.Rename(tmp, destino); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("renomeando %s: %w", destino, err)
	}
	return nil
}

// desinstalar cumpre a ordem do painel: remove o agente desta máquina e CONFIRMA de
// volta antes de sair.
//
// A confirmação é imediata e não espera o próximo ciclo — daqui a uma hora este
// processo não existe mais. Sem ela o painel ficaria para sempre com "ordem enviada,
// sem resposta", que é indistinguível de máquina desligada.
//
// No Linux a remoção termina no reinício seguinte (o promotor root faz o trabalho
// privilegiado), então o que se confirma é o INÍCIO: `desinstalando`. O painel guarda
// a diferença; anunciar "removido" antes da hora seria a mesma mentira que o resto
// desta base passou o ano desmontando.
func (a *Atualizador) desinstalar(ctx context.Context, motivo string) {
	a.log.Warn("o painel pediu a desinstalação deste agente", "motivo", motivo)

	res := selfuninstall.Executar(a.cfg.Dir)
	a.registrar(estado{Estado: res.Estado, Motivo: motivo, Erro: res.Erro})

	// Confirma AGORA, com a chave que ainda vale para este canal. A consulta leva o
	// último estado gravado logo acima — é o mesmo formato do relato de atualização.
	cctx, cancel := context.WithTimeout(ctx, timeoutConsulta)
	defer cancel()
	if _, err := a.consultar(cctx); err != nil {
		a.log.Warn("auto-desinstalação: não consegui confirmar ao painel", "err", err)
	}

	switch {
	case res.Estado == selfuninstall.EstadoFalhou:
		// Segue rodando: o painel já sabe do erro, e sumir em silêncio seria pior do
		// que continuar existindo com o problema visível na tela.
		a.log.Error("auto-desinstalação falhou; o agente continua instalado", "err", res.Erro)
	case res.ReiniciarParaConcluir:
		a.log.Warn("auto-desinstalação: pedindo o reinício para o promotor root concluir")
		a.reiniciar()
	default:
		a.log.Warn("auto-desinstalação concluída; encerrando o agente")
		a.reiniciar()
	}
}
