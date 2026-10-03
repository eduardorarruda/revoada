package alerting

import (
	"context"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// Regra de ausência ("servidor parou de reportar").
//
// O buraco que ela fecha: TODA regra do painel compara um VALOR. Quando o agente
// morre, não existe valor — e `valor ausente > 90` é `false`. Um servidor desligado,
// travado ou sem rede saía do radar em silêncio, com todas as regras "> X" caladas,
// exatamente no momento em que era mais importante avisar. A ausência só existia como
// PINTURA DE TELA (store/dashboards.go, health/health.go), e tela não acorda ninguém.
//
// Por isso a regra é de fábrica e não opcional: ela é o piso do monitoramento — o
// aviso de que não há mais o que monitorar. É semeada no boot do avaliador, no mesmo
// padrão idempotente do dashboard genérico (dashboards.EnsureGeneric).
const (
	// HeartbeatMetric é o nome RESERVADO da métrica-âncora de presença. Não é uma
	// métrica de série temporal: é um marcador que faz o avaliador trocar a consulta
	// ao ClickHouse pela idade do último sinal de cada servidor no inventário.
	//
	// A âncora é `hosts.last_seen`, escrita pelo gateway a cada ingestão autenticada
	// do agente (gateway/internal/pg, UpsertHost) — a mesma fonte do "up" da
	// Infraestrutura e do semáforo do mural. Assim o alerta e a tela contam a mesma
	// história; um dizer "fora" e o outro "no ar" seria pior que não alertar.
	HeartbeatMetric = "revoada.host.heartbeat"

	// heartbeatRuleName é o nome exibido da regra de fábrica.
	heartbeatRuleName = "Servidor parou de reportar"

	// heartbeatThresholdSeconds: 5 minutos sem nenhum sinal do agente.
	//
	// O agente reporta a cada 30 s e a Infraestrutura considera "fora" a partir de
	// 2 min. Alertar em 2 min transformaria toda reinicialização de agente, todo
	// reboot curto e todo soluço de rede em mensagem no WhatsApp. 5 minutos são
	// 10 ciclos de coleta perdidos: já não é soluço.
	heartbeatThresholdSeconds = 300

	// heartbeatWindowSeconds é a janela declarada da regra. Não agrega nada (a
	// consulta não vai ao ClickHouse), mas alimenta staleGrace no reconcile — e uma
	// janela curta mantém a carência no piso de 90 s.
	heartbeatWindowSeconds = 60
)

// EnsureHeartbeatRule garante que a regra de ausência exista e esteja LIGADA.
// Idempotente, chamada no boot do avaliador.
//
// Conservadora quanto ao que o usuário configurou: limiar, severidade e canais da
// regra existente são preservados — quem quiser 10 minutos em vez de 5, ou avisar só
// um canal, edita e a edição permanece. O que ela NÃO deixa passar é a regra ficar
// desligada: um heartbeat desligado é um servidor morto sem ninguém para contar, e
// o painel volta a mentir por omissão exatamente como antes.
func (e *Evaluator) EnsureHeartbeatRule(ctx context.Context) error {
	rules, err := e.st.ListAlertRules(ctx)
	if err != nil {
		return err
	}
	for _, r := range rules {
		if r.Metric != HeartbeatMetric {
			continue
		}
		if r.Enabled {
			return nil
		}
		r.Enabled = true
		if err := e.st.UpdateAlertRule(ctx, r.ID, r); err != nil {
			return err
		}
		// Religar é o mesmo evento de "primeiro ciclo" que semear: enquanto esteve
		// desligada, os servidores mudos se acumularam, e todos abrem juntos no ciclo
		// seguinte. Vale o mesmo regime de resumo agrupado (ver marcaRecemSemeada).
		e.marcaRecemSemeada(r.ID)
		e.log.Warn("regra de servidor sem reportar estava desligada; religada (é de fábrica)", "rule", r.Name, "id", r.ID)
		return nil
	}
	id, err := e.st.CreateAlertRule(ctx, store.AlertRule{
		Name:          heartbeatRuleName,
		Metric:        HeartbeatMetric,
		Filters:       map[string]string{},
		Hosts:         []string{}, // global: todo servidor do inventário
		Agg:           "max",
		ConditionOp:   ">",
		Threshold:     heartbeatThresholdSeconds,
		WindowSeconds: heartbeatWindowSeconds,
		ForSeconds:    0, // o piso de minConsecutiveEvals já exige duas avaliações
		Severity:      "critical",
		ChannelIDs:    []int64{}, // vazio = todos os canais habilitados
	})
	if err != nil {
		return err
	}
	e.marcaRecemSemeada(id)
	e.log.Info("regra de fábrica 'Servidor parou de reportar' semeada", "id", id, "limite_s", heartbeatThresholdSeconds)
	return nil
}

// marcaRecemSemeada põe a regra no regime de PRIMEIRO CICLO: os alertas dela continuam
// sendo abertos no banco e aparecendo na tela, mas não fazem fan-out — saem num resumo
// agrupado só, entregue por flushPrimeiroCiclo no fim do ciclo.
//
// A razão é medida, não teórica. Esta regra nasce ligada, global e com ChannelIDs vazio
// (= TODOS os canais). No primeiro ciclo ela não encontra "um servidor que caiu": ela
// encontra de uma vez o inventário inteiro que não reporta — agente ainda não repontado,
// VM desligada, host desativado que ninguém apagou. Medido no dev: 69 hosts → 69 eventos
// e 126 notificações em dois minutos, TODAS com alert_count = 1. Em produção isso é uma
// mensagem por canal, por servidor silencioso, no minuto do deploy — no WhatsApp do
// operador, junto com todo o resto do deploy.
//
// Um inventário desatualizado não é um incidente novo; é um relatório. Uma mensagem.
func (e *Evaluator) marcaRecemSemeada(ruleID int64) {
	if e.freshRules == nil {
		return
	}
	e.freshRules[ruleID] = primeiroCicloCiclos
}

// primeiroCicloCiclos é quantas avaliações a regra recém-semeada passa no regime de
// resumo agrupado.
//
// Não é 1, e a razão é medida: o piso de minConsecutiveEvals exige duas avaliações
// consecutivas para o alerta abrir, então no PRIMEIRO ciclo não abre nada. Com o regime
// durando um ciclo só, o resumo saía vazio e a enxurrada acontecia no segundo ciclo,
// intacta — foi o que a primeira tentativa mediu: 69 alertas abertos, 0 resumos.
//
// minConsecutiveEvals + 1 (3 ciclos ≈ 90 s) cobre a abertura da primeira leva e ainda
// dá uma folga para os servidores cuja série chega com atraso, sem prender a regra num
// regime especial por tempo demais: do quarto ciclo em diante ela notifica normalmente.
const primeiroCicloCiclos = minConsecutiveEvals + 1

// evalHeartbeat avalia a regra de ausência: para cada servidor do inventário, o valor
// comparado é a IDADE em segundos do último sinal do agente.
//
// Devolve true (regra avaliada) sempre que conseguiu ler o inventário — a falha de
// leitura devolve false, e aí o reconcile não auto-resolve nada, pela mesma razão de
// sempre: falha de leitura não é ausência de problema.
func (e *Evaluator) evalHeartbeat(ctx context.Context, r store.AlertRule, now time.Time, seen map[string]bool) bool {
	hosts, err := e.st.HostsDetailed(ctx, "")
	if err != nil {
		e.log.Warn("alerting: lendo inventário para o heartbeat", "err", err)
		return false
	}
	escopo := map[string]bool{}
	for _, h := range r.Hosts {
		if h != "" {
			escopo[h] = true
		}
	}
	for _, h := range hosts {
		if len(escopo) > 0 && !escopo[h.Hostname] {
			continue
		}
		// Servidor cadastrado que NUNCA reportou (last_seen zerado) fica de fora: não
		// há ausência a detectar em quem nunca esteve presente — seria alarme no
		// cadastro, antes mesmo de o agente ser instalado.
		if h.LastSeen.Unix() <= 0 {
			continue
		}
		labels := map[string]string{"host": h.Hostname}
		fp := fingerprint(r.ID, labels)
		seen[fp] = true
		e.lastSeen[fp] = now
		idade := now.Sub(h.LastSeen).Seconds()
		if idade < 0 {
			idade = 0 // relógio do agente adiantado: não inventa presença futura
		}
		if compare(idade, r.ConditionOp, r.Threshold) {
			e.onMet(ctx, r, labels, fp, idade, now)
		} else {
			e.onClear(ctx, r, labels, fp, idade)
		}
	}
	return true
}
