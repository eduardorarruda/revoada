package copia

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/core/migracao/plano"
	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
)

// finalizar faz o acabamento depois da troca: acerta as sequências (o id original
// foi gravado), religa as FKs das tabelas criadas e apaga as stagings. A cópia
// prévia FICA: é ela que permite reverter depois de concluída. Falhas aqui viram
// aviso — os dados já estão no lugar e conferidos.
func (x *execucao) finalizar(ctx context.Context, passos []plano.Passo) []string {
	var avisos []string
	destinoDe := map[string]plano.Passo{}
	for _, p := range passos {
		destinoDe[p.Origem.Nome] = p
	}
	for _, p := range passos {
		real, _ := x.g.d.tabela(p.Destino.Nome)
		for _, c := range p.Destino.Colunas {
			if !c.Identidade {
				continue
			}
			col, _ := x.g.d.id(c.Nome)
			q := fmt.Sprintf("SELECT setval(pg_get_serial_sequence($1, $2), GREATEST((SELECT MAX(%s) FROM %s), 1))", col, real)
			if _, err := x.g.db.ExecContext(ctx, q, real, c.Nome); err != nil {
				avisos = append(avisos, fmt.Sprintf("não deu para acertar a sequência de %s.%s: %v", p.Destino.Nome, c.Nome, err))
			}
		}
		if p.Criar {
			avisos = append(avisos, x.religarFKs(ctx, p, destinoDe)...)
		}
	}
	for _, it := range x.man.Itens {
		if it.Staging == "" {
			continue
		}
		stg, _ := x.g.d.tabela(it.Staging)
		if _, err := x.g.db.ExecContext(ctx, "DROP TABLE IF EXISTS "+stg); err != nil {
			avisos = append(avisos, fmt.Sprintf("a staging %s ficou para trás: %v", it.Staging, err))
		}
	}
	x.man.Fase = "concluido"
	if err := x.g.gravarManifesto(ctx, x.g.db, *x.man); err != nil {
		avisos = append(avisos, "não deu para marcar o manifesto como concluído: "+err.Error())
	}
	return avisos
}

// religarFKs recria, na tabela que nasceu no destino, as FKs da origem cujo pai
// também foi migrado (com as colunas traduzidas pelo mapeamento).
func (x *execucao) religarFKs(ctx context.Context, p plano.Passo, porOrigem map[string]plano.Passo) []string {
	var avisos []string
	traduzir := func(pp plano.Passo, cols []string) ([]string, bool) {
		out := make([]string, 0, len(cols))
		for _, c := range cols {
			achou := false
			for _, mc := range pp.Map.Colunas {
				if mc.ColunaOrigem == c {
					q, _ := x.g.d.id(mc.ColunaDestino)
					out, achou = append(out, q), true
					break
				}
			}
			if !achou {
				return nil, false
			}
		}
		return out, true
	}
	for _, fk := range p.Origem.Estrangeiras {
		pai, ok := porOrigem[fk.TabelaRef]
		if !ok {
			avisos = append(avisos, fmt.Sprintf("%s: a FK %s aponta para %s, que não foi migrada; não foi recriada", p.Destino.Nome, fk.Nome, fk.TabelaRef))
			continue
		}
		filhos, ok1 := traduzir(p, fk.Colunas)
		pais, ok2 := traduzir(pai, fk.ColunasRef)
		if !ok1 || !ok2 {
			avisos = append(avisos, fmt.Sprintf("%s: a FK %s usa colunas fora do mapeamento; não foi recriada", p.Destino.Nome, fk.Nome))
			continue
		}
		h := sha256.Sum256([]byte(p.Destino.Nome + "/" + fk.Nome))
		nome, _ := x.g.d.id("revoada_fk_" + hex.EncodeToString(h[:6]))
		filho, _ := x.g.d.tabela(p.Destino.Nome)
		tpai, _ := x.g.d.tabela(pai.Destino.Nome)
		q := fmt.Sprintf("ALTER TABLE %s ADD CONSTRAINT %s FOREIGN KEY (%s) REFERENCES %s (%s)",
			filho, nome, strings.Join(filhos, ", "), tpai, strings.Join(pais, ", "))
		if _, err := x.g.db.ExecContext(ctx, q); err != nil {
			avisos = append(avisos, fmt.Sprintf("%s: não deu para recriar a FK %s: %v", p.Destino.Nome, fk.Nome, err))
		}
	}
	return avisos
}

// ResumoReversao é o resumo da tarefa migracao.reverter.
type ResumoReversao struct {
	Execucao  string   `json:"execucao"`
	Passos    []string `json:"passos"`
	DuracaoMS int64    `json:"duracao_ms"`
}

// Reverter desfaz uma execução pelo manifesto (ARQUITETURA §9.4) — numa transação só: o
// PostgreSQL desfaz DDL junto, então ou o destino volta inteiro ao estado anterior,
// ou nada muda e o erro diz por quê.
//
//   - tabela criada → DROP TABLE
//   - tabela que estava vazia → antes da troca: só apaga a staging; depois: DELETE
//   - tabela com dados → antes da troca: só apaga a staging; depois: restaura a cópia
//     prévia (DELETE + INSERT … SELECT da cópia)
func Reverter(ctx context.Context, bruto []byte, r Relator) (any, error) {
	esp, err := lerEspecificacao(bruto)
	if err != nil {
		return nil, err
	}
	inicio := time.Now()
	db, err := abrirBanco(ctx, esp.Destino, r, "destino")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	g := destino{db: db, d: novoDialeto(esp.Destino, 0)}
	man, err := g.lerManifesto(ctx, db, esp.Execucao)
	if err != nil {
		return nil, fmt.Errorf("lendo o manifesto no destino: %w", err)
	}
	if man == nil {
		return nil, fmt.Errorf("o destino não tem manifesto da execução %s (nada foi gravado, ou a tabela %s foi apagada)", esp.Execucao, tabelaManifesto)
	}
	if man.Fase == "revertido" {
		return nil, fmt.Errorf("esta execução já foi revertida")
	}
	res := &ResumoReversao{Execucao: esp.Execucao}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback() //nolint:errcheck
	exec := func(passo, q string) error {
		res.Passos = append(res.Passos, passo)
		if _, err := tx.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("%s: %w", passo, err)
		}
		return nil
	}
	itens := slices.Clone(man.Itens)
	slices.Reverse(itens) // filhas primeiro
	// 1ª volta (filhas → pais): apaga o que a execução gravou.
	for _, it := range itens {
		t, _ := g.d.tabela(it.Tabela)
		var err error
		switch it.Estrategia {
		case plano.EstrategiaApagarTabela:
			err = exec("apagar a tabela criada "+it.Tabela, "DROP TABLE IF EXISTS "+t)
		case plano.EstrategiaEsvaziar:
			if it.Trocada {
				err = exec("esvaziar "+it.Tabela+" (estava vazia antes)", "DELETE FROM "+t)
			}
		case plano.EstrategiaStaging:
			if it.Trocada {
				err = exec("apagar as linhas atuais de "+it.Tabela, "DELETE FROM "+t)
			}
		}
		if err != nil {
			return res, err
		}
	}
	// 2ª volta (pais → filhas): devolve o conteúdo da cópia prévia e limpa os auxiliares.
	slices.Reverse(itens)
	for _, it := range itens {
		t, _ := g.d.tabela(it.Tabela)
		if it.Copia != "" {
			bkp, _ := g.d.tabela(it.Copia)
			if it.Trocada {
				if err := exec("restaurar "+it.Tabela+" da cópia prévia", "INSERT INTO "+t+" OVERRIDING SYSTEM VALUE SELECT * FROM "+bkp); err != nil {
					return res, err
				}
			}
			if err := exec("apagar a cópia prévia de "+it.Tabela, "DROP TABLE IF EXISTS "+bkp); err != nil {
				return res, err
			}
		}
		if it.Staging != "" {
			stg, _ := g.d.tabela(it.Staging)
			if err := exec("apagar a staging de "+it.Tabela, "DROP TABLE IF EXISTS "+stg); err != nil {
				return res, err
			}
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+g.controle()+" WHERE execucao = $1", esp.Execucao); err != nil {
		return res, err
	}
	man.Fase = "revertido"
	if err := g.gravarManifesto(ctx, tx, *man); err != nil {
		return res, err
	}
	if err := tx.Commit(); err != nil {
		return res, err
	}
	res.DuracaoMS = time.Since(inicio).Milliseconds()
	r.Evento("reverter", agentev1.EventoTarefa_INFO, fmt.Sprintf("execução revertida: %d passos numa transação", len(res.Passos)), 100, nil)
	return res, nil
}
