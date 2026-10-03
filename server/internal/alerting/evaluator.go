// Package alerting avalia regras periodicamente e gerencia a máquina de estados
// normal → pendente → disparado → resolvido, persistindo em alert_events e anotando
// no ClickHouse. Notifica mudanças de estado via Notifier (P4.2).
package alerting

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/chquery"
	"github.com/eduardorarruda/revoada/server/internal/query"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// Estados de uma Notification. `nodata` é deliberadamente SEPARADO de `resolved`:
// eram a mesma coisa e o operador recebia "✅ RESOLVIDO" quando, na verdade, a série
// tinha simplesmente parado de chegar.
//
// Medido em dev: série interrompida às 14:30:33, alerta fechado às 14:32:51 com
// motivo "sem dados" — e o que saiu no canal foi "✅ RESOLVIDO". O servidor morreu e
// o aviso dizia que ele tinha voltado ao normal. Um alerta encerrado por FALTA DE
// INFORMAÇÃO precisa de texto próprio: o problema pode continuar, ou estar pior.
const (
	StateFiring   = "firing"
	StateResolved = "resolved"
	StateNoData   = "nodata"
)

// Notification descreve uma transição de estado para o notificador.
type Notification struct {
	Rule     store.AlertRule
	Labels   map[string]string
	Value    float64
	State    string // firing | resolved | nodata
	Since    time.Time
	Flapping bool // alerta oscilante: notifica uma vez e agrupa o resto
	// SuppressedCount é quantas transições deste mesmo alerta foram ENGOLIDAS pelo
	// amortecedor de oscilação na última hora. Sem esse número, quem recebia o
	// "🔁 OSCILANDO" nunca mais ouvia falar do incidente — nem quando ele terminava.
	SuppressedCount int
	// ThrottledCount é quantas transições DESTA REGRA o teto horário engoliu desde a
	// última mensagem entregue. Sem esse número, o teto seria uma censura silenciosa —
	// o operador veria 20 mensagens e não saberia que houve mais 40 eventos.
	ThrottledCount int
	// Evidence é COMO o painel chegou à conclusão, em uma frase ("2 tentativas
	// seguidas ... a camada segura (TLS) não completou em 10 s"). Fica FORA dos
	// labels de propósito: label entra no fingerprint, e um número que muda a cada
	// ciclo separaria o disparo da recuperação em dois alertas distintos.
	// Opcional — quem não tem evidência a declarar deixa vazio.
	Evidence string
}

// Notifier recebe transições de estado (implementado na P4.2).
type Notifier interface {
	Notify(ctx context.Context, n Notification)
}

// BatchNotifier é o Notifier que sabe entregar VÁRIAS transições numa mensagem só.
//
// Existe por causa do primeiro ciclo de uma regra recém-semeada: a regra de ausência
// nasce ligada, global e sem canais escolhidos (= todos os canais), e no primeiro ciclo
// ela encontra de uma vez TODO servidor do inventário que não reporta — agente ainda
// não repontado, VM desligada, host desativado que ninguém apagou. Medido no dev: 69
// hosts → 69 eventos e 126 notificações em dois minutos, todas com alert_count=1. Um
// resumo agrupado conta a mesma coisa em UMA mensagem.
//
// É opcional: um Notifier que não implemente a interface simplesmente não recebe o
// resumo (os alertas continuam sendo abertos no banco e aparecendo na tela).
type BatchNotifier interface {
	NotifyBatch(ctx context.Context, ns []Notification)
}

// Parâmetros de flapping: N TRANSIÇÕES (aberturas E fechamentos) do mesmo fingerprint
// dentro da janela marcam o alerta como oscilante — notifica uma vez e suprime o resto.
const (
	flapThreshold = 4
	// flapWindowMin / flapWindowMax delimitam a janela de contagem de oscilação, que é
	// PROPORCIONAL à janela da regra (ver flapWindowFor). Antes era uma constante de 10
	// minutos, e isso tornava a detecção INALCANÇÁVEL: com o piso de minConsecutiveEvals
	// contando evidência nova, cada transição custa ao menos 2 janelas da regra, então
	// abrir→fechar→abrir exige ≥4 janelas. Para 4 transições em 10 minutos a regra
	// precisaria de janela ≤ 50 s — e o padrão de fábrica é 300 s. Consequência medida em
	// dev: nenhum alert_event com flapping=true, e detectFlapping/SetFlapping/suppress/
	// flapClose/flushFlapClosures inteiramente mortos.
	flapWindowMin = 10 * time.Minute
	flapWindowMax = 2 * time.Hour
	// noDataMinGrace: carência mínima antes de auto-resolver um alerta cuja série
	// parou de reportar (evita resolver por um buraco pontual de coleta).
	noDataMinGrace = 90 * time.Second
	// pendingTTL: expira entradas de estado cuja série sumiu e que não têm alerta
	// ativo, impedindo o vazamento dos mapas por cardinalidade de labels efêmeros.
	pendingTTL = 30 * time.Minute
	// suppressWindow é a janela de contagem das transições suprimidas pelo
	// amortecedor de oscilação — é a "última hora" que aparece na mensagem.
	suppressWindow = time.Hour
)

// maxNotifyPerRuleHour é o TETO de mensagens que UMA regra pode gerar por hora.
//
// O amortecedor de oscilação sozinho nunca segurou nada (ver flapWindowFor: ele era
// inalcançável), e o Router entrega cada transição na hora, sem agrupamento e sem teto.
// Cenário medido/derivado: um cron de 5 em 5 minutos que estoura a CPU por 2 minutos,
// contra uma regra de janela 60 s, produz 24 mensagens por hora POR CANAL — nenhuma
// marcada como oscilante. E no primeiro boot da regra de ausência foram 69 eventos e
// 126 notificações em dois minutos, uma por servidor silencioso, todas com alert_count=1.
//
// O teto é por REGRA (não por fingerprint) porque é a regra que define o volume: 69
// hosts mudos são 69 fingerprints distintos e um problema só. O que passa do teto não
// some: vira contagem, e a contagem viaja na PRÓXIMA mensagem que couber
// (Notification.ThrottledCount → "N avisos desta regra foram agrupados…").
const maxNotifyPerRuleHour = 20

// notifyBudget é o consumo de mensagens de uma regra na última hora.
type notifyBudget struct {
	sent    []time.Time // mensagens efetivamente entregues
	dropped []time.Time // transições engolidas pelo teto, ainda não contadas em mensagem
}

// flapWindowFor é a janela de contagem de oscilação de uma regra: proporcional à janela
// dela, com piso e teto.
//
// A conta: cada transição exige minConsecutiveEvals avaliações com balde NOVO, ou seja
// ~minConsecutiveEvals janelas. Para caber flapThreshold transições, a janela de
// contagem precisa de flapThreshold×minConsecutiveEvals janelas da regra — 8 janelas.
// Com a regra padrão (300 s) são 40 minutos; com a de 60 s cai no piso de 10 minutos,
// que é justamente o que pega o cron de 5 em 5 minutos (abre em t+2, fecha em t+4,
// reabre em t+7 → 5 transições em 10 minutos).
func flapWindowFor(windowSeconds int) time.Duration {
	if windowSeconds <= 0 {
		windowSeconds = 60
	}
	d := time.Duration(flapThreshold*minConsecutiveEvals*windowSeconds) * time.Second
	if d < flapWindowMin {
		d = flapWindowMin
	}
	if d > flapWindowMax {
		d = flapWindowMax
	}
	return d
}

// minConsecutiveEvals é o PISO de avaliações consecutivas para o alerta MUDAR de
// estado — vale nos DOIS sentidos (abrir e fechar) e vale mesmo quando a regra tem
// `for_seconds = 0`, que é o default de fábrica.
//
// Existe porque uma única avaliação nunca foi evidência suficiente, e isso foi medido
// em dev, não deduzido:
//
//   - ABRIR: uma única amostra num balde (cnt=1, avg=100) abriu alerta no primeiro
//     ciclo. Um pico de um segundo virava incidente;
//   - FECHAR: `onClear` resolvia e notificava na PRIMEIRA avaliação abaixo do limiar.
//     Uma série oscilando 91/89 a cada 30 s produziu 10 alert_events em 9 minutos —
//     dez mensagens para um problema só.
//
// Com o piso, uma oscilação que não se sustenta por dois ciclos consecutivos nunca
// chega a virar (nem a deixar de ser) um alerta. É um piso, não um teto: uma regra
// com `for_seconds` maior continua obedecendo o valor dela para abrir.
//
// A escolha por "2 avaliações consecutivas" (e não por um segundo limiar de
// histerese) é deliberada: cura os dois sentidos de uma vez, vale para as regras que
// JÁ EXISTEM e não exige coluna nova nem migração.
const minConsecutiveEvals = 2

// containerRunningMetric é a métrica da regra "Container caído". Recebe tratamento
// especial no reconcile: um container REMOVIDO (que parou de reportar) resolve na
// hora — não é "caído", deixou de existir.
const containerRunningMetric = "container.running"

// containerAbsentWindow é a janela recente usada para decidir se um container ainda
// reporta. Sem amostra de container.running nesse período, tratamos como removido.
const containerAbsentWindow = 150 * time.Second

// evalRangeWindows é quantas janelas da regra o avaliador PEDE à Query API.
//
// A consulta agrupa em baldes alinhados ao relógio (…, 18:00:00, 18:05:00, …) e
// descarta o balde que ainda não fechou. Pedir exatamente [now-window, now] não
// basta por dois motivos, e os dois foram vistos em produção:
//
//   - o único balde fechado da faixa costuma estar TRUNCADO À ESQUERDA, porque a
//     consulta filtra ts >= from e o `from` cai no meio dele. Às 18:02 uma regra de
//     "média de 5 minutos" lia o balde 17:55 com apenas os 3 minutos a partir de
//     17:57 — uma média de 3 minutos com nome de 5;
//   - dependendo do instante, sobra ZERO balde fechado, e a série somem do ciclo.
//
// Pedindo DUAS janelas, o balde mais recente que fecha dentro da faixa está sempre
// inteiramente contido nela: a média de 5 minutos é de fato a média dos 5 minutos.
// O custo é ler o dobro de baldes — dois pontos de rollup por série, irrelevante.
const evalRangeWindows = 2

// seriesQuerier é a fatia da Query API que o avaliador usa. É uma interface (e não
// o *query.Handler concreto) para o loop de avaliação poder ser testado sem
// ClickHouse — em especial a faixa de tempo que ele PEDE, que é onde morava o bug
// do balde parcial.
type seriesQuerier interface {
	QuerySeries(ctx context.Context, req query.Request) (query.Response, error)
}

// evalStreak conta avaliações CONSECUTIVAS num mesmo sentido. É o que sustenta o
// piso de minConsecutiveEvals: um ciclo isolado zera o contador do sentido oposto,
// então só uma sequência real de avaliações muda o estado do alerta.
type evalStreak struct {
	met   int // avaliações consecutivas com a condição VERDADEIRA
	clear int // avaliações consecutivas com a condição FALSA
}

// pendingClose é a resolução SUPRIMIDA de um alerta oscilante, guardada para ser
// re-notificada quando o surto de oscilação de fato terminar. Sem ela, o amortecedor
// engolia também a resolução VERDADEIRA (evaluator.go, onClear) e o incidente
// terminava sem ninguém saber.
type pendingClose struct {
	rule   store.AlertRule
	labels map[string]string
	value  float64
	since  time.Time // início do alerta, para a duração total
}

// Evaluator roda o loop de avaliação.
type Evaluator struct {
	st       *store.Store
	q        seriesQuerier
	ch       *chquery.Client
	log      *slog.Logger
	notifier Notifier

	pending     map[string]time.Time   // fingerprint -> desde quando a condição é verdadeira
	flappedAt   map[string]time.Time   // fingerprint -> última detecção de flapping
	lastSeen    map[string]time.Time   // fingerprint -> último ciclo em que a série apareceu com dado
	streak      map[string]evalStreak  // fingerprint -> avaliações consecutivas (piso dos dois sentidos)
	lastBucket  map[string]int64       // fingerprint -> início do último balde JÁ avaliado
	suppress    map[string][]time.Time // fingerprint -> transições engolidas pelo amortecedor
	transitions map[string][]time.Time // fingerprint -> abre/fecha recentes (base da oscilação)
	flapClose   map[string]pendingClose
	// budget é o teto horário de mensagens POR REGRA (ver maxNotifyPerRuleHour).
	budget map[int64]*notifyBudget
	// freshRules são as regras SEMEADAS por este processo que ainda estão na largada:
	// ruleID -> ciclos restantes no regime. Enquanto estiverem aqui, os alertas delas
	// abrem no banco mas não fazem fan-out: saem num resumo agrupado só (ver
	// flushPrimeiroCiclo e primeiroCicloCiclos).
	freshRules map[int64]int
	// primeiroCiclo acumula as aberturas do primeiro ciclo de uma regra recém-semeada,
	// para virarem UMA mensagem em vez de uma por servidor.
	primeiroCiclo map[int64][]Notification
	purge         chan int64 // ruleIDs de regras removidas/editadas a esquecer
	// ignorados: containers que o painel deixou de vigiar (tabela ignored_containers),
	// relidos a cada ciclo. Em erro de leitura fica o conjunto anterior — um soluço do
	// banco não pode fazer um container ignorado voltar a alertar.
	ignorados ignorados
}

func New(st *store.Store, q seriesQuerier, ch *chquery.Client, log *slog.Logger, n Notifier) *Evaluator {
	return &Evaluator{st: st, q: q, ch: ch, log: log, notifier: n,
		pending: map[string]time.Time{}, flappedAt: map[string]time.Time{}, lastSeen: map[string]time.Time{},
		streak: map[string]evalStreak{}, lastBucket: map[string]int64{},
		suppress: map[string][]time.Time{}, transitions: map[string][]time.Time{},
		flapClose: map[string]pendingClose{},
		budget:    map[int64]*notifyBudget{}, freshRules: map[int64]int{},
		primeiroCiclo: map[int64][]Notification{},
		purge:         make(chan int64, 128)}
}

// PurgeRule descarta o estado em memória de uma regra removida ou editada, evitando
// o vazamento dos mapas. Não-bloqueante: a limpeza acontece no próximo ciclo (e o
// reconcile em evalAll é o fallback caso o canal esteja cheio). Seguro para chamar
// de qualquer goroutine (handlers HTTP) — só enfileira; os mapas são tocados apenas
// pela goroutine do Run.
func (e *Evaluator) PurgeRule(ruleID int64) {
	select {
	case e.purge <- ruleID:
	default:
	}
}

func (e *Evaluator) drainPurge() {
	for {
		select {
		case id := <-e.purge:
			e.forgetRule(id)
		default:
			return
		}
	}
}

func (e *Evaluator) forgetRule(ruleID int64) {
	prefix := fmt.Sprintf("%d|", ruleID) // fingerprint = "<ruleID>|<labels>"
	for _, fp := range e.knownFingerprints() {
		if strings.HasPrefix(fp, prefix) {
			e.forgetFP(fp)
		}
	}
}

// knownFingerprints devolve todo fingerprint com estado em memória. Existe para que
// acrescentar um mapa de estado novo não deixe um vazamento silencioso para trás: a
// limpeza (forgetRule/sweepState) passa por aqui e cobre todos de uma vez.
func (e *Evaluator) knownFingerprints() []string {
	set := map[string]bool{}
	for _, m := range []map[string]time.Time{e.pending, e.flappedAt, e.lastSeen} {
		for fp := range m {
			set[fp] = true
		}
	}
	for fp := range e.streak {
		set[fp] = true
	}
	for fp := range e.lastBucket {
		set[fp] = true
	}
	for fp := range e.suppress {
		set[fp] = true
	}
	for fp := range e.transitions {
		set[fp] = true
	}
	for fp := range e.flapClose {
		set[fp] = true
	}
	out := make([]string, 0, len(set))
	for fp := range set {
		out = append(out, fp)
	}
	return out
}

func (e *Evaluator) forgetFP(fp string) {
	delete(e.pending, fp)
	delete(e.flappedAt, fp)
	delete(e.lastSeen, fp)
	delete(e.streak, fp)
	delete(e.lastBucket, fp)
	delete(e.suppress, fp)
	delete(e.transitions, fp)
	delete(e.flapClose, fp)
}

// Run avalia todas as regras a cada 30s até o ctx ser cancelado.
func (e *Evaluator) Run(ctx context.Context) {
	// ANTES de qualquer avaliação: alinha o fingerprint dos alertas que já estavam
	// abertos ao esquema atual. Se isto rodar depois do primeiro ciclo, cada alerta
	// ativo do painel vira uma duplicata ("DISPAROU" de novo) e um falso "📡 SEM DADOS"
	// — ver MigrateOpenFingerprints para a medição.
	if reescritos, fundidos, err := e.MigrateOpenFingerprints(ctx); err != nil {
		e.log.Warn("alerting: migrando fingerprints de alertas abertos", "err", err)
	} else if reescritos > 0 || fundidos > 0 {
		e.log.Info("alerting: fingerprints de alertas abertos alinhados ao esquema atual",
			"reescritos", reescritos, "fundidos", fundidos)
	}
	// Semeia a regra de ausência ANTES do primeiro ciclo: sem ela, um agente morto
	// deixava TODAS as regras "> X" caladas (valor ausente < 90 é `false`), e a
	// ausência só existia como pintura de tela — que não avisa ninguém.
	if err := e.EnsureHeartbeatRule(ctx); err != nil {
		e.log.Warn("alerting: semeando a regra de servidor sem reportar", "err", err)
	}
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	e.evalAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.evalAll(ctx)
		}
	}
}

func (e *Evaluator) evalAll(ctx context.Context) {
	e.drainPurge()
	rules, err := e.st.ListAlertRules(ctx)
	if err != nil {
		e.log.Warn("alerting: listando regras", "err", err)
		return
	}
	if lista, err := e.st.ListIgnoredContainers(ctx); err != nil {
		e.log.Warn("alerting: lendo containers ignorados (mantém o conjunto anterior)", "err", err)
	} else {
		e.ignorados = novosIgnorados(lista)
	}
	now := time.Now()
	seen := map[string]bool{}   // fingerprints com dado neste ciclo
	okRules := map[int64]bool{} // regras cuja query teve sucesso
	ruleset := make(map[int64]store.AlertRule, len(rules))
	for _, r := range rules {
		ruleset[r.ID] = r
		if !r.Enabled {
			continue
		}
		if e.evalRule(ctx, r, now, seen) {
			okRules[r.ID] = true
		}
	}
	e.reconcile(ctx, ruleset, okRules, seen)
	e.flushPrimeiroCiclo(ctx, now)
	e.flushFlapClosures(ctx, now)
	e.sweepState(seen, now)
}

// flushPrimeiroCiclo fecha o primeiro ciclo das regras recém-semeadas: entrega UM
// resumo agrupado por regra (em vez de uma mensagem por servidor) e tira a regra do
// regime especial — do segundo ciclo em diante ela notifica normalmente.
//
// O que isto evita, medido no dev: a regra de ausência nasce ligada, global e sem
// canais escolhidos (= todos os canais). No primeiro ciclo ela encontra de uma vez todo
// servidor do inventário que não reporta — 69 hosts → 69 eventos e 126 notificações em
// dois minutos, todas com alert_count=1. Em produção isso é uma mensagem por canal, por
// servidor desativado, no minuto do deploy. O inventário estar desatualizado não é um
// incidente novo; é um relatório.
func (e *Evaluator) flushPrimeiroCiclo(ctx context.Context, now time.Time) {
	for ruleID, restantes := range e.freshRules {
		// O regime dura primeiroCicloCiclos avaliações, não uma. Motivo medido: com o
		// piso de minConsecutiveEvals, NENHUM alerta abre no primeiro ciclo — o primeiro
		// só abre no segundo. Retirando a regra do regime ao fim do ciclo 1, o resumo
		// saía vazio e a enxurrada acontecia no ciclo 2, exatamente como antes (medido:
		// 69 alertas abertos, 0 resumos, fan-out normal).
		if restantes > 1 {
			e.freshRules[ruleID] = restantes - 1
			continue
		}
		abertos := e.primeiroCiclo[ruleID]
		delete(e.primeiroCiclo, ruleID)
		delete(e.freshRules, ruleID)
		if len(abertos) == 0 {
			continue
		}
		e.log.Info("regra recém-semeada: primeiro ciclo entregue como resumo, sem fan-out por servidor",
			"rule", abertos[0].Rule.Name, "alertas", len(abertos))
		if e.notifier == nil {
			continue
		}
		if !e.gastaOrcamento(ruleID, now, &abertos[0]) {
			continue
		}
		if b, ok := e.notifier.(BatchNotifier); ok {
			b.NotifyBatch(ctx, abertos)
			continue
		}
		// Notificador SEM suporte a lote (é o caso do notify.Router hoje): degrada para
		// UMA mensagem, a do primeiro alerta, carregando a contagem dos demais. Silêncio
		// total seria pior — os outros 68 estão na tela, mas ninguém olharia a tela sem
		// um aviso. O teto continua valendo: uma mensagem, não sessenta e nove.
		unica := abertos[0]
		unica.ThrottledCount += len(abertos) - 1
		e.notifier.Notify(ctx, unica)
		e.log.Warn("notificador sem suporte a lote: primeiro ciclo saiu como UMA mensagem com a contagem dos demais",
			"rule", unica.Rule.Name, "agrupados", len(abertos)-1)
	}
}

// evalRule avalia uma regra. Devolve true se a query teve sucesso (o reconcile usa
// isso para não auto-resolver quando a coleta apenas falhou). Registra em `seen` e
// `lastSeen` os fingerprints que apareceram com dado.
func (e *Evaluator) evalRule(ctx context.Context, r store.AlertRule, now time.Time, seen map[string]bool) bool {
	// A regra de ausência não tem série para consultar — o que ela mede é o silêncio.
	// Entra pelo MESMO caminho de estado e notificação das demais (onMet/onClear), e
	// por isso herda o piso de avaliações, o fingerprint estável e o amortecedor.
	if r.Metric == HeartbeatMetric {
		return e.evalHeartbeat(ctx, r, now, seen)
	}
	allOK := true
	from, to := evalRange(now, r.WindowSeconds)
	// Uma decisão por FINGERPRINT por ciclo. As séries são colhidas primeiro e só
	// depois avaliadas — ver colapso por fingerprint em `escolheSerie`.
	colapsadas := map[string]serieColapsada{}
	var ordem []string // ordem de primeira aparição: torna o ciclo determinístico
	for _, filters := range ruleFilterSets(r) {
		resp, err := e.q.QuerySeries(ctx, query.Request{
			Metric:  r.Metric,
			Filters: filters,
			Agg:     r.Agg,
			Step:    r.WindowSeconds,
			From:    from.UTC().Format(time.RFC3339),
			To:      to.UTC().Format(time.RFC3339),
		})
		if err != nil {
			allOK = false // não marca a regra como avaliada: o reconcile não auto-resolve.
			continue
		}
		for _, s := range resp.Series {
			// Container ignorado: fora do ciclo. Sem `seen` e SEM ESTADO — o que o
			// avaliador sabia dele é esquecido. Congelar a sequência de avaliações
			// faria o "voltar a vigiar" reabrir o alerta na 1ª leitura, com o início
			// antigo, e um "resolvido" de oscilação pendente ainda sairia. O reconcile
			// encerra calado o alerta que estiver aberto.
			if e.ignorados.contem(s.Labels) {
				e.forgetFP(fingerprint(r.ID, s.Labels))
				continue
			}
			// A resposta já vem sem o balde em construção, então "último valor
			// não-nulo" é o último balde FECHADO — a agregação da regra sobre a
			// janela inteira. Continuar aceitando um balde anterior quando o mais
			// recente veio vazio é proposital: é melhor reavaliar com o balde de
			// trás (no máximo duas janelas de idade, ver evalRangeWindows) do que
			// deixar a série sumir do ciclo e o reconcile começar a contar carência
			// para auto-resolver um alerta que continua válido.
			i, ok := lastValueIndex(s.Values)
			if !ok {
				continue
			}
			v := *s.Values[i]
			fp := fingerprint(r.ID, s.Labels)
			seen[fp] = true
			e.lastSeen[fp] = now

			bucket, hasTS := bucketTS(resp.TS, i)
			cand := serieColapsada{
				labels: s.Labels, value: v, bucket: bucket, hasTS: hasTS,
				viola: compare(v, r.ConditionOp, r.Threshold),
			}
			if prev, existe := colapsadas[fp]; existe {
				colapsadas[fp] = escolheSerie(prev, cand)
				continue
			}
			colapsadas[fp] = cand
			ordem = append(ordem, fp)
		}
	}

	for _, fp := range ordem {
		c := colapsadas[fp]
		// O piso de minConsecutiveEvals conta EVIDÊNCIA NOVA, não tique de
		// relógio. Sem esta trava ele conta o MESMO balde duas vezes: o
		// avaliador roda a cada 30 s e pede duas janelas (evalRangeWindows), então
		// um balde fechado continua sendo o "último valor não-nulo" nos ciclos
		// seguintes enquanto nada mais novo chega. Medido em dev: UMA amostra
		// solta (qa.spike, um único ponto às 19:01:05) abriu alerta às 19:02:01 —
		// dois tiques lendo o mesmo balde — e fechou às 19:03:31 como "sem dados",
		// rendendo 1 alert_event e 4 mensagens para um pico de um segundo. É a
		// mesma falha que o piso existia para matar, um ciclo mais tarde.
		if c.hasTS {
			if prev, visto := e.lastBucket[fp]; visto && prev == c.bucket {
				continue // mesmo balde já avaliado: não é uma segunda avaliação
			}
			e.lastBucket[fp] = c.bucket
		}
		if c.viola {
			e.onMet(ctx, r, c.labels, fp, c.value, now)
		} else {
			e.onClear(ctx, r, c.labels, fp, c.value)
		}
	}
	return allOK
}

// serieColapsada é a leitura de UMA série candidata a representar um fingerprint no
// ciclo. Ver escolheSerie para o porquê de existir mais de uma candidata.
type serieColapsada struct {
	labels map[string]string
	value  float64
	bucket int64 // início do balde de onde saiu o valor
	hasTS  bool  // a resposta trouxe eixo de tempo alinhado (ver bucketTS)
	viola  bool  // a condição da regra é verdadeira para este valor
}

// escolheSerie decide qual de DUAS leituras do mesmo fingerprint vale no ciclo.
//
// Por que o colapso existe — o defeito que ele mata:
//
// O fingerprint ignora volatileLabels (state/health/exit_code/…), então N séries
// distintas do ClickHouse colapsam numa identidade só. É o caso normal de
// `container.running`: o agente emite `state=running` e `state=exited` para o mesmo
// container, e a Query API devolve duas séries. Antes, TODAS eram avaliadas, em
// sequência, no mesmo ciclo: cada uma chamava onMet/onClear e o bumpStreak zerava o
// contador do sentido oposto, então o piso de minConsecutiveEvals NUNCA era alcançado.
// Pior, o lastBucket andava para trás — a segunda série regravava um balde mais antigo
// e o ciclo seguinte descartava a leitura nova como "mesmo balde".
//
// Medido em dev: UMA série cruza o limiar e o alerta abre em 23 s; DUAS séries do mesmo
// fingerprint, com o mesmo problema acontecendo, levam 5 MINUTOS para abrir. O alerta
// não deixava de existir — chegava treze vezes mais tarde, que num container em
// crash-loop é a diferença entre avisar e relatar.
//
// Os critérios, nesta ordem:
//
//  1. balde MAIS RECENTE vence (é o pedido: a decisão do ciclo é sobre o agora). Uma
//     leitura sem eixo de tempo (caminho esparso da Query API) perde para qualquer uma
//     que tenha balde, porque não dá para saber se é velha;
//  2. empate no mesmo balde: vence a que VIOLA. São a mesma coisa medida com rótulos de
//     estado diferentes dentro do mesmo intervalo; se uma delas diz que o container está
//     fora, o container esteve fora. Errar para o lado do alerta custa uma mensagem;
//     errar para o outro esconde o incidente;
//  3. empate total: fica a primeira (a ordem de ruleFilterSets é determinística), para
//     que o mesmo ciclo produza sempre a mesma decisão.
func escolheSerie(atual, novo serieColapsada) serieColapsada {
	switch {
	case novo.hasTS && !atual.hasTS:
		return novo
	case atual.hasTS && !novo.hasTS:
		return atual
	case novo.hasTS && atual.hasTS && novo.bucket != atual.bucket:
		if novo.bucket > atual.bucket {
			return novo
		}
		return atual
	case novo.viola && !atual.viola:
		return novo
	default:
		return atual
	}
}

// evalRange devolve a faixa de tempo pedida à Query API para uma regra de janela
// `windowSeconds`. Ver evalRangeWindows para o porquê de a faixa ser mais larga que
// a janela da regra. Função separada para ser testável sem banco nem ClickHouse.
func evalRange(now time.Time, windowSeconds int) (from, to time.Time) {
	if windowSeconds <= 0 {
		windowSeconds = 60 // mesmo default da Query API para step inválido
	}
	span := time.Duration(evalRangeWindows*windowSeconds) * time.Second
	return now.Add(-span), now
}

// splitContainers separa um filtro `container` multivalorado (vários containers numa
// mesma regra "Container caído", ex.: "web,worker,db") na lista de containers. Nomes de
// container do Docker não contêm vírgula (só [A-Za-z0-9_.-]), então separar por vírgula
// é seguro e sem ambiguidade. Sem vírgula (0 ou 1 container) devolve nil — o caminho
// antigo segue intacto, sem nada a expandir.
func splitContainers(v string) []string {
	if !strings.Contains(v, ",") {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// cloneFilters copia os filtros crus (com folga para acrescentar host/container).
func cloneFilters(f map[string]string) map[string]string {
	m := make(map[string]string, len(f)+1)
	for k, v := range f {
		m[k] = v
	}
	return m
}

// ruleFilterSets devolve os conjuntos de labels a consultar para uma regra. Sem hosts
// selecionados a regra é global — um único conjunto, os filtros crus (vale para todo
// servidor que reporta a métrica). Com hosts, uma consulta por servidor (filtros +
// host=<hostname>), permitindo escopo para 1 ou N servidores específicos. O fingerprint
// resultante é idêntico ao da consulta global daquele host (o label `host` volta na
// série nos dois casos), então alternar o escopo não duplica nem perde alertas.
//
// Um filtro `container` com vários valores (vírgulas) é expandido em um conjunto por
// container — assim uma única regra "Container caído" pode vigiar N containers (ex.: 5
// de 15) sem virar N regras. Cada conjunto tem um container exato, então cada
// (host, container) rende sua própria série/fingerprint e alerta independente.
func ruleFilterSets(r store.AlertRule) []map[string]string {
	containers := splitContainers(r.Filters["container"])

	// expandByContainer: sem multi-container devolve o próprio conjunto; com N
	// containers, um conjunto por container (sobrescrevendo o label `container`).
	expandByContainer := func(seed map[string]string) []map[string]string {
		if len(containers) == 0 {
			return []map[string]string{seed}
		}
		out := make([]map[string]string, 0, len(containers))
		for _, c := range containers {
			m := cloneFilters(seed)
			m["container"] = c
			out = append(out, m)
		}
		return out
	}

	if len(r.Hosts) == 0 {
		return expandByContainer(cloneFilters(r.Filters))
	}
	sets := make([]map[string]string, 0, len(r.Hosts))
	for _, h := range r.Hosts {
		if h == "" {
			continue
		}
		m := cloneFilters(r.Filters)
		m["host"] = h
		sets = append(sets, expandByContainer(m)...)
	}
	if len(sets) == 0 {
		return expandByContainer(cloneFilters(r.Filters))
	}
	return sets
}

// reconcile auto-resolve alertas ativos que ficaram órfãos: regra removida/desativada,
// ou série que parou de reportar (após uma carência). Corrige o bug de alertas que
// ficavam "firing" para sempre quando o host/métrica sumia.
func (e *Evaluator) reconcile(ctx context.Context, ruleset map[int64]store.AlertRule, okRules map[int64]bool, seen map[string]bool) {
	active, err := e.st.ListAlerts(ctx, true)
	if err != nil {
		return
	}
	now := time.Now()
	for _, a := range active {
		rule, exists := ruleset[a.RuleID]
		// Container ignorado: o alerta aberto fecha calado (normalmente o próprio
		// IgnoreContainer já fechou; isto cobre o que abriu no mesmo instante).
		if e.ignorados.contem(a.Labels) {
			e.resolveStale(ctx, a, reasonContainerIgnored, nil)
			continue
		}
		// Container REMOVIDO: se a regra vigia container.running e o container deixou
		// de reportar (foi removido, não apenas parou), resolve JÁ com motivo próprio.
		// Sem isto, a janela da regra (ex.: 5 min) manteria o alerta "firing" por muito
		// tempo após a remoção — o ruído do "container caído" para containers efêmeros
		// (regra 5.1). Um container PARADO mas presente segue reportando e alerta normal.
		if exists && rule.Enabled && okRules[rule.ID] && rule.Metric == containerRunningMetric {
			host, ctr := a.Labels["host"], a.Labels["container"]
			if host != "" && ctr != "" && !e.containerReporting(ctx, host, ctr) {
				e.resolveStale(ctx, a, reasonContainerRemoved, &rule)
				continue
			}
		}
		// Servidor apagado do inventário: a regra de heartbeat deixa de enumerá-lo, e
		// o alerta ficaria à espera da carência para fechar como "sem dados" — texto
		// de servidor mudo para um servidor que o próprio operador removeu. Fecha
		// silenciosamente: é administrativo, não é notícia.
		if exists && rule.Enabled && okRules[rule.ID] && rule.Metric == HeartbeatMetric && !seen[a.Fingerprint] {
			e.resolveStale(ctx, a, reasonHostRemoved, &rule)
			continue
		}
		last, hasLast := e.lastSeen[a.Fingerprint]
		in := staleInput{
			ruleExists:    exists,
			ruleEnabled:   exists && rule.Enabled,
			ruleOK:        okRules[a.RuleID],
			seen:          seen[a.Fingerprint],
			hasLastSeen:   hasLast,
			sinceLastSeen: now.Sub(last),
			windowSeconds: rule.WindowSeconds,
		}
		switch action, reason := decideStale(in); action {
		case staleResolve:
			if exists {
				e.resolveStale(ctx, a, reason, &rule)
			} else {
				e.resolveStale(ctx, a, reason, nil) // regra removida: não há a quem notificar
			}
		case staleStartClock:
			// Um alerta ativo cujo estado em memória se perdeu (restart do server, ou
			// mudança do esquema de fingerprint) recomeça a contar a carência daqui.
			e.lastSeen[a.Fingerprint] = now
		case staleKeep:
		}
	}
}

// staleAction é o que o reconcile faz com um alerta ativo neste ciclo.
type staleAction int

const (
	staleKeep       staleAction = iota // deixa como está
	staleStartClock                    // sem histórico de presença: começa a contar a carência
	staleResolve                       // resolve automaticamente
)

// staleReason é o MOTIVO pelo qual um alerta ativo foi encerrado sem que a condição
// tenha deixado de valer. É um tipo (e não uma string solta) porque o motivo decide
// duas coisas que não podem divergir: o que vai escrito na anotação do ClickHouse e
// se — e com que texto — alguém é notificado.
type staleReason string

const (
	reasonRuleRemoved      staleReason = "regra removida"
	reasonRuleDisabled     staleReason = "regra desativada"
	reasonNoData           staleReason = "sem dados"
	reasonContainerRemoved staleReason = "container removido"
	reasonHostRemoved      staleReason = "servidor removido do inventário"
	reasonContainerIgnored staleReason = "container ignorado"
)

// staleNotification traduz o motivo do encerramento em (estado notificado, notifica?).
//
// Duas regras que custaram caro estão aqui:
//
//   - motivo ADMINISTRATIVO não notifica. Desligar um monitor não conserta o
//     servidor: `decideStale` devolvia staleResolve para regra desativada e o
//     reconcile chamava resolveStale com a regra em mãos — o "✅ RESOLVIDO" saía. Um
//     alerta encerrado porque alguém mexeu no cadastro fecha calado, como já se fazia
//     para regra removida;
//   - "sem dados" NÃO é "voltou ao normal". Vira StateNoData, com texto próprio: o
//     servidor parou de reportar e pode estar pior.
func staleNotification(reason staleReason) (state string, notify bool) {
	switch reason {
	case reasonNoData:
		return StateNoData, true
	case reasonContainerRemoved:
		// O container deixou de existir — não é ausência de dados nem melhora: é uma
		// remoção, e o operador precisa saber que o alerta saiu da lista por isso.
		return StateResolved, true
	default: // regra removida, regra desativada, servidor removido, container ignorado
		return StateResolved, false
	}
}

// staleInput reúne o que o reconcile sabe sobre um alerta ativo neste ciclo.
type staleInput struct {
	ruleExists    bool // a regra ainda existe
	ruleEnabled   bool // …e está habilitada
	ruleOK        bool // a consulta da regra teve SUCESSO neste ciclo
	seen          bool // a série apareceu COM DADO neste ciclo
	hasLastSeen   bool // já houve algum ciclo com dado registrado em memória
	sinceLastSeen time.Duration
	windowSeconds int
}

// decideStale é a decisão de auto-resolução, isolada do banco para poder ser
// testada — é o caminho mais perigoso do avaliador, porque resolver por engano
// dispara notificação de "resolvido" para um problema que continua acontecendo.
//
// O descarte do balde incompleto (ver evalRangeWindows) mexeu justamente na
// entrada `seen`: se uma consulta passasse a devolver série vazia, o alerta cairia
// aqui. Por isso as três travas abaixo são explícitas e testadas:
//
//   - consulta que FALHOU (ruleOK=false) nunca resolve — falha de leitura não é
//     ausência de problema;
//   - série presente neste ciclo nunca resolve;
//   - série ausente só resolve depois de uma carência de, no mínimo, 90 s e de 3
//     janelas da regra — um buraco pontual de coleta não resolve nada.
func decideStale(in staleInput) (staleAction, staleReason) {
	switch {
	case !in.ruleExists:
		return staleResolve, reasonRuleRemoved
	case !in.ruleEnabled:
		return staleResolve, reasonRuleDisabled
	case !in.ruleOK:
		return staleKeep, "" // a query da regra falhou neste ciclo (evita falso resolve)
	case in.seen:
		return staleKeep, "" // série presente, segue o fluxo normal
	case !in.hasLastSeen:
		return staleStartClock, "" // sem histórico (ex.: pós-restart) → inicia o relógio
	case in.sinceLastSeen >= staleGrace(in.windowSeconds):
		return staleResolve, reasonNoData
	default:
		return staleKeep, ""
	}
}

// staleGrace é a carência antes de auto-resolver uma série que parou de reportar:
// o maior entre noDataMinGrace e 3 janelas da regra (a janela manda porque uma
// regra de 5 min só produz um valor novo a cada 5 min).
func staleGrace(windowSeconds int) time.Duration {
	grace := noDataMinGrace
	if g := 3 * time.Duration(windowSeconds) * time.Second; g > grace {
		grace = g
	}
	return grace
}

// containerReporting diz se um container ainda emite container.running recentemente
// (janela containerAbsentWindow). Um container REMOVIDO para de reportar; um container
// PARADO mas presente segue reportando running=0. Consulta a tabela crua (step curto)
// para não depender do atraso dos rollups. Em erro de consulta devolve true
// (conservador: nunca resolve um alerta por engano por causa de falha de leitura).
//
// Pede IncludePartial: aqui a pergunta é de PRESENÇA ("ainda chega alguma amostra?"),
// não de valor, e uma amostra no balde ainda aberto prova presença tão bem quanto
// qualquer outra. Sem isso, um container que só reportou depois da última fronteira
// de balde pareceria removido e o alerta seria resolvido com "container removido" —
// uma notificação falsa de "resolvido" para um container que continua caído.
func (e *Evaluator) containerReporting(ctx context.Context, host, container string) bool {
	now := time.Now()
	resp, err := e.q.QuerySeries(ctx, query.Request{
		Metric:         containerRunningMetric,
		Filters:        map[string]string{"host": host, "container": container},
		Agg:            "max",
		Step:           30,
		From:           now.Add(-containerAbsentWindow).UTC().Format(time.RFC3339),
		To:             now.UTC().Format(time.RFC3339),
		IncludePartial: true,
	})
	if err != nil {
		return true
	}
	for _, s := range resp.Series {
		if _, ok := lastValue(s.Values); ok {
			return true
		}
	}
	return false
}

// resolveStale marca um alerta órfão como resolvido, anota no ClickHouse e esquece o
// estado. `rule` pode ser nil (regra removida) — nesse caso não notifica. O MOTIVO
// decide o estado notificado e se há notificação (ver staleNotification).
func (e *Evaluator) resolveStale(ctx context.Context, a store.AlertEvent, reason staleReason, rule *store.AlertRule) {
	// O estado é decidido ANTES de gravar, porque agora ele vai para o banco junto: a
	// tela de Alertas lia `state` e escrevia "resolvido" no mesmo evento cujo WhatsApp
	// tinha dito "📡 SEM DADOS" — a separação entre "acabou" e "parei de ouvir" existia
	// só no canal e não chegava ao histórico.
	state, notify := staleNotification(reason)
	ev, err := e.st.ResolveAlert(ctx, a.Fingerprint, state)
	if err != nil { // já resolvido
		e.forgetFP(a.Fingerprint)
		return
	}
	title := fmt.Sprintf("[%s] %s %s", state, a.RuleName, a.Severity)
	_ = e.ch.InsertEvent(ctx, "default", "alert", title, "encerrado automaticamente: "+string(reason), a.Labels)
	e.forgetFP(a.Fingerprint)
	if rule != nil && notify {
		e.notify(ctx, Notification{Rule: *rule, Labels: a.Labels, Value: a.Value, State: state, Since: ev.StartedAt})
	}
	e.log.Info("alerta encerrado automaticamente", "rule", a.RuleName, "labels", a.Labels,
		"motivo", reason, "estado", state, "notificou", notify && rule != nil)
}

// sweepState expira estado em memória cuja série sumiu há mais que o TTL e que não
// tem alerta ativo (os ativos são tratados pelo reconcile). Fecha o vazamento dos mapas.
//
// O relógio do expurgo é o `lastSeen` (último ciclo COM dado): usar o timestamp de
// cada mapa isoladamente apagaria o contador de avaliações consecutivas de uma série
// que continua chegando, e o piso de minConsecutiveEvals nunca fecharia.
func (e *Evaluator) sweepState(seen map[string]bool, now time.Time) {
	for _, fp := range e.knownFingerprints() {
		if seen[fp] {
			continue
		}
		ts, ok := e.lastSeen[fp]
		if ok && now.Sub(ts) <= pendingTTL {
			continue
		}
		// Uma resolução oscilante ainda por confirmar não pode ser varrida antes da
		// hora: ela é a única notificação que o operador vai receber do fim do incidente.
		if _, pend := e.flapClose[fp]; pend && !ok {
			continue
		}
		e.forgetFP(fp)
	}
}

// notify é o ÚNICO caminho de saída de uma notificação do avaliador. Passar por aqui é
// o que garante que o teto horário por regra valha para TODAS as transições — abrir,
// resolver, "sem dados", re-notificação de oscilante. Antes, cada ponto chamava o
// notificador direto e não havia teto nenhum: o Router entrega na hora, sem agrupar.
func (e *Evaluator) notify(ctx context.Context, n Notification) {
	if e.notifier == nil {
		return
	}
	if !e.gastaOrcamento(n.Rule.ID, time.Now(), &n) {
		return
	}
	e.notifier.Notify(ctx, n)
}

// gastaOrcamento debita uma mensagem do teto horário da regra. Devolve false quando o
// teto já estourou — a transição não some, vira contagem, e a contagem viaja na próxima
// mensagem que couber (ThrottledCount). É a diferença entre "não te avisei" e "te
// avisei que houve mais 37".
func (e *Evaluator) gastaOrcamento(ruleID int64, now time.Time, n *Notification) bool {
	b := e.budget[ruleID]
	if b == nil {
		b = &notifyBudget{}
		e.budget[ruleID] = b
	}
	b.sent = maisRecentesQue(b.sent, now, time.Hour)
	b.dropped = maisRecentesQue(b.dropped, now, time.Hour)
	if len(b.sent) >= maxNotifyPerRuleHour {
		b.dropped = append(b.dropped, now)
		e.log.Warn("teto horário de mensagens da regra atingido; transição agrupada para a próxima mensagem",
			"rule", n.Rule.Name, "teto", maxNotifyPerRuleHour, "agrupadas", len(b.dropped), "labels", n.Labels)
		return false
	}
	n.ThrottledCount = len(b.dropped)
	b.dropped = nil
	b.sent = append(b.sent, now)
	return true
}

// maisRecentesQue descarta timestamps fora da janela, preservando a ordem.
func maisRecentesQue(ts []time.Time, now time.Time, win time.Duration) []time.Time {
	keep := ts[:0]
	for _, t := range ts {
		if now.Sub(t) <= win {
			keep = append(keep, t)
		}
	}
	return keep
}

// bumpStreak registra a avaliação deste ciclo e devolve quantas avaliações
// CONSECUTIVAS já houve no sentido pedido. Uma avaliação num sentido zera o
// contador do outro: é isso que faz uma oscilação nunca alcançar o piso.
func (e *Evaluator) bumpStreak(fp string, met bool) int {
	st := e.streak[fp]
	if met {
		st.met++
		st.clear = 0
	} else {
		st.clear++
		st.met = 0
	}
	e.streak[fp] = st
	if met {
		return st.met
	}
	return st.clear
}

func (e *Evaluator) onMet(ctx context.Context, r store.AlertRule, labels map[string]string, fp string, v float64, now time.Time) {
	// Piso dos dois sentidos: a condição verdadeira num ciclo zera a sequência de
	// ciclos limpos. Uma série 91/89 alternando a cada 30 s nunca acumula duas
	// avaliações no mesmo sentido — e era ela que rendia 10 alert_events em 9 minutos.
	met := e.bumpStreak(fp, true)

	since, seen := e.pending[fp]
	if !seen {
		e.pending[fp] = now
		since = now
	}
	// Piso: nunca abre na PRIMEIRA avaliação. Um balde com uma única amostra
	// (cnt=1, avg=100) abria alerta no primeiro ciclo antes desta trava. A checagem
	// vem ANTES da ida ao banco de propósito: numa oscilação, o ciclo isolado nem
	// chega a consultar o alerta ativo.
	if met < minConsecutiveEvals {
		return
	}
	// já persistido como firing?
	if _, err := e.st.ActiveAlertByFingerprint(ctx, fp); err == nil {
		return
	}
	// respeitou o `for`?
	if now.Sub(since) < time.Duration(r.ForSeconds)*time.Second {
		return
	}
	// O alerta reabriu: a resolução que estava guardada para re-notificação não
	// aconteceu — o incidente não terminou, continua oscilando.
	delete(e.flapClose, fp)
	id, err := e.st.OpenAlert(ctx, store.AlertEvent{
		RuleID: r.ID, Fingerprint: fp, Labels: labels, Value: v, Severity: r.Severity,
	})
	if err != nil {
		e.log.Warn("alerting: abrindo alerta", "err", err)
		return
	}
	e.annotate(ctx, r, labels, "firing", v)

	// Flapping: muitas reaberturas na janela → marca, notifica só na 1ª detecção.
	flapping, firstFlap := e.detectFlapping(ctx, fp, id, now, r.WindowSeconds)
	if flapping && !firstFlap {
		e.recordSuppressed(fp, now)
		e.log.Info("alerta oscilante suprimido", "rule", r.Name, "labels", labels,
			"suprimidas_na_hora", e.suppressedCount(fp, now))
		return // suprime o storm
	}
	n := Notification{Rule: r, Labels: labels, Value: v, State: StateFiring, Since: since,
		Flapping: flapping, SuppressedCount: e.suppressedCount(fp, now)}
	// Regra recém-semeada, primeiro ciclo: o alerta já está aberto no banco e já aparece
	// na tela, mas não vira mensagem AGORA. Vai para o resumo agrupado — senão o
	// inventário mudo inteiro sai como uma mensagem por servidor por canal (ver
	// marcaRecemSemeada: 69 hosts → 126 notificações em dois minutos).
	if _, largada := e.freshRules[r.ID]; largada {
		e.primeiroCiclo[r.ID] = append(e.primeiroCiclo[r.ID], n)
		e.log.Info("alerta disparado no primeiro ciclo da regra; guardado para o resumo",
			"rule", r.Name, "value", v, "labels", labels)
		return
	}
	e.notify(ctx, n)
	e.log.Info("alerta disparado", "rule", r.Name, "value", v, "labels", labels, "flapping", flapping)
}

// recordSuppressed anota mais uma transição engolida pelo amortecedor de oscilação.
// A contagem existe para que a supressão seja VISÍVEL: antes, quem recebia o
// "🔁 OSCILANDO" não ouvia mais nada — nem o número de eventos engolidos, nem o fim
// do incidente (medido no crash-loop: 26 mensagens em 6 minutos e nenhuma marcada
// como oscilante, porque o fingerprint mudava a cada reinício; ver stableLabels).
func (e *Evaluator) recordSuppressed(fp string, now time.Time) {
	e.suppress[fp] = append(e.suppress[fp], now)
	e.pruneSuppressed(fp, now)
}

func (e *Evaluator) pruneSuppressed(fp string, now time.Time) {
	ts := e.suppress[fp]
	keep := ts[:0]
	for _, t := range ts {
		if now.Sub(t) <= suppressWindow {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(e.suppress, fp)
		return
	}
	e.suppress[fp] = keep
}

// suppressedCount é quantas transições foram suprimidas na última hora.
func (e *Evaluator) suppressedCount(fp string, now time.Time) int {
	e.pruneSuppressed(fp, now)
	return len(e.suppress[fp])
}

// flapCloseDue diz se o surto de oscilação já passou (nenhuma nova detecção dentro de
// flapWindow) e a resolução guardada pode enfim ser anunciada. Isolada do banco para
// ser testável: é ela que decide se o operador fica sabendo que o incidente acabou.
func flapCloseDue(lastFlap time.Time, now time.Time, janela time.Duration) bool {
	if lastFlap.IsZero() {
		return true // não há surto em curso
	}
	return now.Sub(lastFlap) > janela
}

// flushFlapClosures re-notifica as resoluções que o amortecedor de oscilação engoliu,
// assim que o surto acaba (nenhuma nova transição por flapWindow).
//
// É a diferença entre "o alerta parou de fazer barulho" e "o alerta terminou". Sem
// isto, o incidente oscilante encerrava em silêncio — a resolução VERDADEIRA era
// suprimida junto com o resto (evaluator.go, onClear) e ninguém sabia que acabou.
func (e *Evaluator) flushFlapClosures(ctx context.Context, now time.Time) {
	for fp, pc := range e.flapClose {
		// Container ignorado (que pode nem reportar mais, e então não passa pelo
		// evalRule): o "resolvido" pendente é descartado, não enviado.
		if e.ignorados.contem(pc.labels) {
			e.forgetFP(fp)
			continue
		}
		if !flapCloseDue(e.flappedAt[fp], now, flapWindowFor(pc.rule.WindowSeconds)) {
			continue // ainda oscilando: esperar o surto passar
		}
		// Reabriu no meio do caminho? Então não terminou — o onMet já teria limpado,
		// mas conferir no banco protege contra um alerta aberto por outra via.
		if _, err := e.st.ActiveAlertByFingerprint(ctx, fp); err == nil {
			delete(e.flapClose, fp)
			continue
		}
		n := e.suppressedCount(fp, now)
		e.notify(ctx, Notification{Rule: pc.rule, Labels: pc.labels, Value: pc.value,
			State: StateResolved, Since: pc.since, SuppressedCount: n})
		e.log.Info("resolução de alerta oscilante re-notificada", "rule", pc.rule.Name, "labels", pc.labels, "suprimidas", n)
		delete(e.flapClose, fp)
		delete(e.flappedAt, fp)
		delete(e.suppress, fp)
	}
}

// detectFlapping conta reaberturas recentes; marca o alerta e devolve se é a
// primeira detecção do surto (única que deve notificar).
func (e *Evaluator) detectFlapping(ctx context.Context, fp string, id int64, now time.Time, windowSeconds int) (flapping, first bool) {
	janela := flapWindowFor(windowSeconds)
	count, err := e.st.CountRecentOpens(ctx, fp, now.Add(-janela))
	if err != nil || count < flapThreshold {
		return false, false
	}
	_ = e.st.SetFlapping(ctx, id)
	last, seen := e.flappedAt[fp]
	e.flappedAt[fp] = now
	first = !seen || now.Sub(last) > janela
	return true, first
}

func (e *Evaluator) onClear(ctx context.Context, r store.AlertRule, labels map[string]string, fp string, v float64) {
	now := time.Now()
	// Piso dos dois sentidos: uma avaliação abaixo do limiar NÃO fecha o alerta.
	// Antes, `onClear` resolvia e notificava na primeira — e a série 91/89 fechava e
	// reabria a cada 30 s.
	if e.bumpStreak(fp, false) < minConsecutiveEvals {
		return // ainda não é recuperação: é só um ciclo abaixo do limiar
	}

	delete(e.pending, fp)
	ev, err := e.st.ResolveAlert(ctx, fp, StateResolved)
	if err != nil {
		return // não havia alerta ativo
	}
	e.annotate(ctx, r, labels, StateResolved, v)
	// Enquanto oscila, suprime a notificação de resolução (senão vira storm) — mas
	// GUARDA a resolução: se o surto terminar aqui, o flushFlapClosures a re-notifica.
	// Suprimir sem guardar era como o incidente terminava em silêncio.
	if last, ok := e.flappedAt[fp]; ok && now.Sub(last) <= flapWindowFor(r.WindowSeconds) {
		e.recordSuppressed(fp, now)
		e.flapClose[fp] = pendingClose{rule: r, labels: labels, value: v, since: ev.StartedAt}
		e.log.Info("resolução oscilante adiada", "rule", r.Name, "labels", labels,
			"suprimidas_na_hora", e.suppressedCount(fp, now))
		return
	}
	delete(e.flappedAt, fp)
	delete(e.flapClose, fp)
	// Since = início do alerta → o notificador calcula a duração total do downtime.
	e.notify(ctx, Notification{Rule: r, Labels: labels, Value: v, State: StateResolved,
		Since: ev.StartedAt, SuppressedCount: e.suppressedCount(fp, now)})
	e.log.Info("alerta resolvido", "rule", r.Name, "labels", labels)
}

func (e *Evaluator) annotate(ctx context.Context, r store.AlertRule, labels map[string]string, state string, v float64) {
	title := fmt.Sprintf("[%s] %s %s", state, r.Name, r.Severity)
	body := fmt.Sprintf("%s %s %.2f (valor %.2f)", r.Metric, r.ConditionOp, r.Threshold, v)
	_ = e.ch.InsertEvent(ctx, "default", "alert", title, body, labels)
}

// Preview conta quantas vezes a regra teria disparado nos últimos `days` dias.
func (e *Evaluator) Preview(ctx context.Context, r store.AlertRule, days int) (int, error) {
	// A regra de ausência não tem série histórica para simular: o inventário guarda
	// só o ÚLTIMO sinal de cada servidor, não o histórico de silêncios. Devolver 0 é
	// honesto; consultar o ClickHouse por uma métrica que não existe devolveria 0
	// também, mas dando a impressão de que houve simulação.
	if r.Metric == HeartbeatMetric {
		return 0, nil
	}
	now := time.Now()
	count := 0
	for _, filters := range ruleFilterSets(r) {
		resp, err := e.q.QuerySeries(ctx, query.Request{
			Metric: r.Metric, Filters: filters, Agg: r.Agg, Step: r.WindowSeconds,
			From: now.Add(-time.Duration(days) * 24 * time.Hour).UTC().Format(time.RFC3339),
			To:   now.UTC().Format(time.RFC3339),
		})
		if err != nil {
			return 0, err
		}
		for _, s := range resp.Series {
			for _, val := range s.Values {
				if val != nil && compare(*val, r.ConditionOp, r.Threshold) {
					count++
				}
			}
		}
	}
	return count, nil
}

func compare(v float64, op string, t float64) bool {
	// Guarda explícita de NaN/±Inf. Hoje esses valores já são barrados antes, em
	// query/helpers.go — isto é defesa em profundidade, para o dia em que outra
	// origem alimentar o avaliador. Vale a pena ser explícito porque o resultado é
	// silencioso e engana: TODA comparação com NaN é falsa, inclusive `NaN > 90`, e
	// um valor impossível vira "está tudo bem" em vez de erro.
	if math.IsNaN(v) || math.IsInf(v, 0) || math.IsNaN(t) || math.IsInf(t, 0) {
		return false
	}
	switch op {
	case ">":
		return v > t
	case ">=":
		return v >= t
	case "<":
		return v < t
	case "<=":
		return v <= t
	case "==":
		return v == t
	}
	return false
}

func lastValue(values []*float64) (float64, bool) {
	i, ok := lastValueIndex(values)
	if !ok {
		return 0, false
	}
	return *values[i], true
}

// lastValueIndex é lastValue devolvendo a POSIÇÃO, para que o chamador possa
// descobrir de qual balde do eixo de tempo o valor veio (ver evalRule: é assim que
// se distingue "outra avaliação" de "o mesmo balde relido").
func lastValueIndex(values []*float64) (int, bool) {
	for i := len(values) - 1; i >= 0; i-- {
		if values[i] != nil {
			return i, true
		}
	}
	return 0, false
}

// bucketTS devolve o início do balde na posição i do eixo de tempo. Devolve
// hasTS=false quando a resposta não trouxe eixo alinhado (caminho esparso da Query
// API, quando a grade densa estoura o teto) — nesse caso o avaliador cai no
// comportamento antigo, que é conservador: conta o tique.
func bucketTS(ts []int64, i int) (int64, bool) {
	if i < 0 || i >= len(ts) {
		return 0, false
	}
	return ts[i], true
}

// volatileLabels são labels que descrevem o ESTADO ou o INVENTÁRIO do recurso, não a
// identidade dele. Ficam de fora do fingerprint.
//
// O fingerprint é a IDENTIDADE do alerta: é por ele que se sabe que "o container api
// do srv1 continua caído" é o mesmo incidente de cinco minutos atrás. Enquanto ele
// carregava o mapa de labels INTEIRO, `container.running` colocava `image`, `state`,
// `health` e `exit_code` dentro dele — justamente os labels que mudam QUANDO o
// problema acontece. Foi reproduzido em dev: mantendo o mesmo problema (valor 100) e
// trocando só `state=running` → `state=exited, exit_code=137`, o painel abriu um
// alerta DUPLICADO e auto-resolveu o antigo com "✅ RESOLVIDO" — com a condição ainda
// verdadeira.
//
// O efeito colateral era pior que a duplicata: `detectFlapping` conta reaberturas POR
// FINGERPRINT, e num crash-loop o `exit_code` gira a cada reinício. O contador nunca
// chegava a flapThreshold e o amortecedor de oscilação NUNCA engatava — medido: 26
// mensagens em 6 minutos (~130/canal/hora) e nenhuma marcada como oscilante.
//
// É uma lista de EXCLUSÃO, e não uma lista de identidade permitida, de propósito: uma
// lista permitida que esquecesse um label discriminante (um `env`, um `mount` de nome
// diferente) FUNDIRIA duas séries distintas num alerta só — dois problemas reais
// virariam um, e um deles sumiria da tela. Errar deixando um label a mais custa uma
// duplicata; errar de menos custa um incidente inteiro.
var volatileLabels = map[string]bool{
	// Estado do recurso — muda exatamente no momento do incidente.
	"state": true, "health": true, "exit_code": true, "status": true,
	// Inventário/versão — muda em atualização de agente, upgrade de imagem, reboot.
	"image": true, "image_id": true, "agent.version": true, "version": true,
	"kernel": true, "os": true, "platform": true, "arch": true,
	"uptime": true, "pid": true, "host.cpu.cores": true,
}

// stableLabels devolve, em ordem, as chaves de identidade de um conjunto de labels
// (tudo menos volatileLabels).
func stableLabels(labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for k := range labels {
		if volatileLabels[k] {
			continue
		}
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func fingerprint(ruleID int64, labels map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d|", ruleID)
	for _, k := range stableLabels(labels) {
		fmt.Fprintf(&b, "%s=%s;", k, labels[k])
	}
	return b.String()
}
