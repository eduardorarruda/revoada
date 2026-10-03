package copia

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/eduardorarruda/revoada/agent/internal/migracao/captura"
	"github.com/eduardorarruda/revoada/core/migracao/plano"
	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
)

// Relator é o que o motor usa para falar com o painel (o canal implementa).
type Relator interface {
	Evento(etapa string, nivel agentev1.EventoTarefa_Nivel, msg string, progresso float64, metricas map[string]float64)
	Pausa(ctx context.Context) error
	Credencial(nome string) ([]byte, error)
	Checkpoint(tabela, ultimaChave string, linhas int64)
}

// lerEspecificacao valida o mínimo antes de abrir qualquer conexão.
func lerEspecificacao(b []byte) (plano.Especificacao, error) {
	var e plano.Especificacao
	if err := json.Unmarshal(b, &e); err != nil {
		return e, fmt.Errorf("especificação inválida: %w", err)
	}
	if e.Execucao == "" {
		return e, fmt.Errorf("especificação sem id de execução")
	}
	if e.Destino.Motor != "postgres" {
		return e, fmt.Errorf("destino %q ainda não suportado (só PostgreSQL)", e.Destino.Motor)
	}
	if e.Mapeamento.Hash() != e.HashMapeamento {
		return e, fmt.Errorf("o mapeamento não confere com o hash aprovado")
	}
	if e.Lote <= 0 {
		e.Lote = plano.LoteInicial
	}
	e.Lote = min(max(e.Lote, 100), 50_000)
	if e.Amostras <= 0 {
		e.Amostras = 10
	}
	return e, nil
}

// abrirBanco abre a conexão com a senha selada (que só vive aqui, em memória).
func abrirBanco(ctx context.Context, b plano.Banco, r Relator, nome string) (*sql.DB, error) {
	senha, err := r.Credencial(nome)
	if err != nil {
		return nil, err
	}
	db, err := captura.Abrir(captura.Conexao{Motor: b.Motor, Endereco: b.Endereco, Banco: b.Banco,
		Usuario: b.Usuario, Senha: string(senha), Opcoes: b.Opcoes})
	for i := range senha {
		senha[i] = 0
	}
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(4)
	db.SetConnMaxIdleTime(5 * time.Minute)
	pctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := db.PingContext(pctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("não conectou ao banco de %s: %w", nome, err)
	}
	return db, nil
}

// estrategia decide como a tabela é carregada (e, por consequência, revertida).
func estrategia(ctx context.Context, g destino, p plano.Passo) (string, error) {
	if p.Criar {
		return plano.EstrategiaApagarTabela, nil
	}
	vazia, err := g.vazia(ctx, p.Destino.Nome)
	if err != nil {
		return "", fmt.Errorf("conferindo se %s está vazia: %w", p.Destino.Nome, err)
	}
	if vazia {
		return plano.EstrategiaEsvaziar, nil
	}
	return plano.EstrategiaStaging, nil
}

// colunaSoma escolhe a coluna do checksum: a PK de uma coluna do destino, senão a
// coluna de lote, senão a primeira mapeada.
func colunaSoma(p plano.Passo) int {
	alvo := ""
	if len(p.Destino.ChavePrimaria) == 1 {
		alvo = p.Destino.ChavePrimaria[0]
	}
	for i, mc := range p.Map.Colunas {
		if alvo != "" && mc.ColunaDestino == alvo {
			return i
		}
	}
	for i, mc := range p.Map.Colunas {
		if p.Map.ColunaLote != "" && mc.ColunaOrigem == p.Map.ColunaLote {
			return i
		}
	}
	return 0
}

func pct(feitas, total int64) float64 {
	if total <= 0 {
		return 100
	}
	return min(float64(feitas)*100/float64(total), 100)
}
