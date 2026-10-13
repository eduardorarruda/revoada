package httpapi

import (
	"net/http"

	"github.com/eduardorarruda/revoada/server/internal/config"
)

// privacidade alimenta a política de privacidade do painel (rota pública: quem ainda
// não entrou também tem direito de ler). Devolve os prazos CONFIGURADOS nesta
// instalação — a política com os prazos padrão mentiria para quem os mudou — e o
// controlador e o contato do encarregado, que só quem opera a instalação conhece.
func privacidade(w http.ResponseWriter, _ *http.Request) {
	dados, _ := config.RetentionDays()
	auditoria, _ := config.AuditRetentionDays()
	writeJSON(w, http.StatusOK, map[string]any{
		"controlador":              config.PrivacidadeControlador(),
		"contato":                  config.PrivacidadeContato(),
		"retencao_dias":            dados,
		"auditoria_dias":           auditoria,
		"sessao_inatividade_horas": int(config.SessaoInatividade().Hours()),
		"sessao_max_horas":         int(config.SessaoMaxima().Hours()),
	})
}
