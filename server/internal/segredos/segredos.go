// Package segredos monta o cofre do painel (criptografia em envelope do core) a partir
// da configuração: a chave mestra mora num arquivo fora do banco (ARQUITETURA §13).
package segredos

import (
	"fmt"
	"log/slog"

	"github.com/eduardorarruda/revoada/core/seguranca/cofre"
	"github.com/eduardorarruda/revoada/server/internal/config"
)

// Abrir carrega (ou cria, na primeira vez) a chave mestra e devolve o cofre.
func Abrir(log *slog.Logger) (*cofre.Cofre, error) {
	caminho := config.ChaveMestraArquivo()
	p, criado, err := cofre.AbrirOuCriarArquivo(caminho)
	if err != nil {
		return nil, fmt.Errorf("chave mestra em %s: %w", caminho, err)
	}
	if criado {
		log.Warn("cofre: chave mestra NOVA criada — faça backup deste arquivo separado do banco; sem ele os segredos ficam ilegíveis",
			"arquivo", caminho)
	} else {
		log.Info("cofre: chave mestra carregada", "arquivo", caminho, "versao_ativa", p.VersaoAtiva())
	}
	return cofre.Novo(p)
}
