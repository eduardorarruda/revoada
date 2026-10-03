package dashboards

import (
	"context"
	"errors"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// EnsureGeneric garante que o dashboard de fábrica "Visão do Host (genérico)" exista.
// Chamado no boot do server. É IDEMPOTENTE e conservador: se o dashboard já existe
// (mesmo editado pelo usuário), não faz nada — nunca sobrescreve o trabalho de quem
// customizou. Assim o painel turnkey aparece de graça no primeiro boot e some da
// lista de "coisas a criar à mão", sem virar um efeito colateral perigoso nos boots
// seguintes. Devolve created=true só quando de fato criou.
func EnsureGeneric(ctx context.Context, st *store.Store) (created bool, err error) {
	_, err = st.GetDashboard(ctx, GenericHostUID)
	if err == nil {
		return false, nil // já existe — respeita o estado atual
	}
	if !errors.Is(err, store.ErrNotFound) {
		return false, err // erro real de banco — propaga
	}
	model, err := StarterHostGenericJSON()
	if err != nil {
		return false, err
	}
	if err := st.CreateDashboard(ctx, GenericHostUID, "Visão do Host (genérico)", "Hosts", model, "sistema"); err != nil {
		// Corrida entre dois boots: outro processo criou entre o GET e o CREATE.
		// Se agora existe, o objetivo foi atingido — trata como sucesso silencioso.
		if _, gerr := st.GetDashboard(ctx, GenericHostUID); gerr == nil {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
