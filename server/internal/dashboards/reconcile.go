package dashboards

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// HostDashboardPrefix é o prefixo do UID dos dashboards por-servidor ("Visão do
// Host — <host>"). O UID é sempre "host-" + hostname técnico. IsHostDashboard usa
// esse prefixo para reconhecê-los (o front também destaca por ele).
const HostDashboardPrefix = "host-"

// IsHostDashboard diz se um UID é de um dashboard de host gerado automaticamente.
// Inclui o genérico ("host-visao-geral") e todos os por-servidor ("host-<host>").
func IsHostDashboard(uid string) bool {
	return strings.HasPrefix(uid, HostDashboardPrefix)
}

// reconcileStore é a superfície mínima do store usada por ReconcileHostDashboards.
// *store.Store a satisfaz; permite testar o reconcile com um fake, sem banco.
type reconcileStore interface {
	ActiveHosts(ctx context.Context) ([]string, error)
	ListDashboards(ctx context.Context, search string) ([]store.DashboardMeta, error)
	HostDisplayName(ctx context.Context, hostname string) (string, error)
	CreateDashboard(ctx context.Context, uid, title, folder string, model json.RawMessage, by string) error
	GetDashboard(ctx context.Context, uid string) (store.Dashboard, error)
}

// ReconcileHostDashboards garante que todo servidor do inventário tenha o seu
// dashboard "Visão do Host — <host>" já populado (CPU/memória/disco/rede/…), a
// partir do MESMO template do starter (StarterHost). É IDEMPOTENTE: cria apenas o
// que falta e não toca nos que já existem (mesmo editados). Reaproveita o starter
// existente em vez de duplicar o template.
//
// NÃO deve rodar no caminho quente de ingestão. É chamado no caminho de LISTAGEM de
// dashboards (best-effort), de forma que abrir a tela de dashboards já traz um painel
// pronto para cada host novo.
//
// LIMITAÇÃO CONHECIDA: não há marcador de "apagado de propósito" (tombstone). Se o
// usuário apagar a Visão do Host de um servidor, o próximo reconcile a RECRIA. Isso é
// intencional (mais simples) — o dashboard por-servidor é considerado infraestrutura
// de fábrica, não conteúdo do usuário.
func ReconcileHostDashboards(ctx context.Context, st reconcileStore) (created int, err error) {
	hosts, err := st.ActiveHosts(ctx)
	if err != nil {
		return 0, err
	}
	if len(hosts) == 0 {
		return 0, nil
	}
	// Conjunto de UIDs já existentes — uma única leitura, sem N consultas.
	existing, err := st.ListDashboards(ctx, "")
	if err != nil {
		return 0, err
	}
	have := make(map[string]struct{}, len(existing))
	for _, d := range existing {
		have[d.UID] = struct{}{}
	}

	for _, host := range hosts {
		uid := HostDashboardPrefix + host
		if _, ok := have[uid]; ok {
			continue // já existe — respeita o estado atual
		}
		// Nome amigável só para o TÍTULO (o UID e os filtros continuam técnicos).
		label, _ := st.HostDisplayName(ctx, host)
		model, merr := StarterHostJSON(host, label)
		if merr != nil {
			return created, merr
		}
		displayName := host
		if label != "" {
			displayName = label
		}
		title := "Visão do Host, " + displayName
		if cerr := st.CreateDashboard(ctx, uid, title, "Hosts", model, "sistema"); cerr != nil {
			// Corrida com outra listagem concorrente: se agora existe, objetivo
			// atingido — segue. Caso contrário, para e devolve o erro.
			if _, gerr := st.GetDashboard(ctx, uid); gerr == nil {
				continue
			}
			return created, cerr
		}
		created++
	}
	return created, nil
}
