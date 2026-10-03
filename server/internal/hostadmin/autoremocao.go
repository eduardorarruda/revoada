package hostadmin

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/auth"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// AUTO-DESINSTALAÇÃO — o agente se remove sozinho, sem SSH.
//
// O problema: apagar um servidor pelo painel tirava os DADOS, mas o agente continuava
// instalado e rodando na máquina. Quem instalou à mão, sem credencial SSH guardada,
// ficava com um processo órfão batendo no gateway e levando 401 até alguém entrar na
// máquina — que é justamente o que a pessoa não tinha como fazer.
//
// A ordem viaja pelo canal que já existe: a consulta horária de auto-atualização. Ele
// já manda o agente BAIXAR E EXECUTAR um binário; mandar remover é estritamente menos
// poder do que isso, e não abre superfície nova.
//
// O que esta ordem NÃO consegue fazer, e a tela precisa dizer:
//   - máquina desligada ou sem rede nunca recebe (fica "aguardando");
//   - agente sem `panel_url` no agent.yaml não tem canal e nunca vai perguntar;
//   - agente numa versão anterior a esta feature ignora o campo e segue rodando.
const (
	// gracaAposRelato: quanto esperamos, depois de o agente confirmar, para apagar a
	// chave de vez. No Linux a remoção se completa no restart seguinte (pelo promotor
	// root) e o agente confirma ANTES de morrer — apagar a chave no mesmo instante
	// tiraria o chão de uma desinstalação em curso.
	gracaAposRelato = 15 * time.Minute
	// desistirApos: ordem que nunca foi cumprida. Guardar uma chave viva para sempre à
	// espera de um agente que não volta é pior do que assumir a perda — chave é
	// credencial. Sete dias cobre férias, máquina desligada no fim de semana e a
	// janela de manutenção mais folgada que já vi por aqui.
	desistirApos = 7 * 24 * time.Hour
	// intervaloFaxina: de quanto em quanto tempo as duas regras acima são aplicadas.
	intervaloFaxina = 10 * time.Minute
)

// ordenarAutoRemocao registra a ordem para TODAS as chaves daquele host (a do
// inventário e as guardadas nos alvos de provisionamento). Devolve quantas ordens
// foram registradas — zero significa que não há agente conhecido para avisar, e a
// tela precisa dizer isso em vez de prometer uma remoção que não vai acontecer.
func (h *Handler) ordenarAutoRemocao(ctx context.Context, tenant, hostname string,
	names []string, target *store.ProvisionTarget, quem string,
) int {
	chaves := map[string]bool{}
	if ks, err := h.st.AgentKeysByHost(ctx, tenant, names); err == nil {
		for _, k := range ks {
			chaves[k] = true
		}
	} else {
		h.log.Error("auto-desinstalação: listar chaves do host falhou", "host", hostname, "err", err)
	}
	if alvos, err := h.st.ProvisionTargetsByHostname(ctx, tenant, hostname); err == nil {
		for _, t := range alvos {
			if t.Serverkey != "" {
				chaves[t.Serverkey] = true
			}
		}
	}
	if target != nil && target.Serverkey != "" {
		chaves[target.Serverkey] = true
	}

	var n int
	for k := range chaves {
		if err := h.st.OrderAgentUninstall(ctx, k, hostname, quem); err != nil {
			h.log.Error("auto-desinstalação: registrar a ordem falhou", "host", hostname, "err", err)
			continue
		}
		n++
	}
	h.log.Info("auto-desinstalação pedida", "host", hostname, "chaves", n, "por", quem)
	return n
}

// quemPediu extrai o nome do usuário autenticado para a ordem carregar autoria. Sem
// sessão (não deveria acontecer: a rota é admin) devolve vazio, e a trilha de
// auditoria continua sendo a fonte completa.
func quemPediu(r *http.Request) string {
	if claims, ok := auth.ClaimsFrom(r.Context()); ok {
		return claims.Name
	}
	return ""
}

// RunUninstallJanitor fecha as ordens que já cumpriram (ou perderam) o seu prazo.
// Roda até o contexto morrer; chamado uma vez pelo main.
func (h *Handler) RunUninstallJanitor(ctx context.Context) {
	t := time.NewTicker(intervaloFaxina)
	defer t.Stop()
	h.faxinaDeOrdens(ctx) // uma passada no boot: o painel pode ter ficado fora por horas
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			h.faxinaDeOrdens(ctx)
		}
	}
}

func (h *Handler) faxinaDeOrdens(ctx context.Context) {
	fechadas, err := h.st.SweepFinishedAgentUninstalls(ctx, gracaAposRelato, desistirApos)
	if err != nil {
		h.log.Warn("auto-desinstalação: faxina das ordens falhou", "err", err)
		return
	}
	for _, u := range fechadas {
		// A chave sai do banco aqui; nunca logamos a chave em si.
		h.log.Info("auto-desinstalação encerrada", "host", u.Hostname,
			"estado", u.Estado(), "resultado", u.Result, slog.Time("ordenada_em", u.OrderedAt))
	}
}
