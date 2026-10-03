package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ExecucaoMigracao liga uma tarefa do canal a um projeto de migração.
type ExecucaoMigracao struct {
	TarefaID       string    `json:"tarefa_id"`
	ProjetoID      string    `json:"projeto_id"`
	Versao         int       `json:"versao"`
	Tipo           string    `json:"tipo"` // simular | executar | reverter
	Execucao       string    `json:"execucao"`
	HashMapeamento string    `json:"hash_mapeamento"`
	RelacionadaID  *string   `json:"relacionada_id,omitempty"`
	CriadaEm       time.Time `json:"criada_em"`
	Tarefa         Tarefa    `json:"tarefa"`
}

func (s *Store) CriarExecucao(ctx context.Context, e ExecucaoMigracao) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO execucoes_migracao
		(tarefa_id, projeto_id, versao, tipo, execucao, hash_mapeamento, relacionada_id) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		e.TarefaID, e.ProjetoID, e.Versao, e.Tipo, e.Execucao, e.HashMapeamento, e.RelacionadaID)
	return err
}

const colunasExecucao = `e.tarefa_id, e.projeto_id, e.versao, e.tipo, e.execucao, e.hash_mapeamento, e.relacionada_id, e.criada_em,
	t.id, t.tipo, t.agente_id, t.estado, t.especificacao, t.progresso, t.iniciada_por, t.origem, t.correlacao_id,
	t.expira_em, t.criada_em, t.inicio, t.fim, t.erro, t.resumo`

func scanExecucao(row pgx.Row) (ExecucaoMigracao, error) {
	var e ExecucaoMigracao
	t := &e.Tarefa
	err := row.Scan(&e.TarefaID, &e.ProjetoID, &e.Versao, &e.Tipo, &e.Execucao, &e.HashMapeamento, &e.RelacionadaID, &e.CriadaEm,
		&t.ID, &t.Tipo, &t.AgenteID, &t.Estado, &t.Especificacao, &t.Progresso, &t.IniciadaPor,
		&t.Origem, &t.CorrelacaoID, &t.ExpiraEm, &t.CriadaEm, &t.Inicio, &t.Fim, &t.Erro, &t.Resumo)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// ExecucoesDoProjeto lista as tarefas do projeto, mais recentes primeiro. A
// especificação (schemas + mapeamento) não volta na lista: é grande e já está na versão.
func (s *Store) ExecucoesDoProjeto(ctx context.Context, projetoID string, limite int) ([]ExecucaoMigracao, error) {
	if limite <= 0 || limite > 200 {
		limite = 50
	}
	rows, err := s.pool.Query(ctx, `SELECT `+colunasExecucao+` FROM execucoes_migracao e JOIN tarefas t ON t.id = e.tarefa_id
		WHERE e.projeto_id = $1 ORDER BY e.criada_em DESC LIMIT $2`, projetoID, limite)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ExecucaoMigracao{}
	for rows.Next() {
		e, err := scanExecucao(rows)
		if err != nil {
			return nil, err
		}
		e.Tarefa.Especificacao = nil
		out = append(out, e)
	}
	return out, rows.Err()
}

// ExecucaoPorTarefa devolve a execução ligada a uma tarefa.
func (s *Store) ExecucaoPorTarefa(ctx context.Context, tarefaID string) (ExecucaoMigracao, error) {
	return scanExecucao(s.pool.QueryRow(ctx, `SELECT `+colunasExecucao+` FROM execucoes_migracao e
		JOIN tarefas t ON t.id = e.tarefa_id WHERE e.tarefa_id = $1`, tarefaID))
}

// Checkpoint é o último lote confirmado de uma tabela numa tarefa.
type Checkpoint struct {
	TarefaID     string    `json:"tarefa_id"`
	Tabela       string    `json:"tabela"`
	UltimaChave  string    `json:"ultima_chave"`
	Linhas       int64     `json:"linhas"`
	Seq          int64     `json:"seq"`
	AtualizadoEm time.Time `json:"atualizado_em"`
}

// GravarCheckpoint guarda o checkpoint; um seq mais velho (reenvio fora de ordem)
// não sobrescreve um mais novo.
func (s *Store) GravarCheckpoint(ctx context.Context, c Checkpoint) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO execucao_checkpoints (tarefa_id, tabela, ultima_chave, linhas, seq)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (tarefa_id, tabela) DO UPDATE SET ultima_chave = EXCLUDED.ultima_chave, linhas = EXCLUDED.linhas,
		seq = EXCLUDED.seq, atualizado_em = now() WHERE execucao_checkpoints.seq < EXCLUDED.seq`,
		c.TarefaID, c.Tabela, c.UltimaChave, c.Linhas, c.Seq)
	return err
}

func (s *Store) CheckpointsDaTarefa(ctx context.Context, tarefaID string) ([]Checkpoint, error) {
	rows, err := s.pool.Query(ctx, `SELECT tarefa_id, tabela, ultima_chave, linhas, seq, atualizado_em
		FROM execucao_checkpoints WHERE tarefa_id = $1 ORDER BY atualizado_em`, tarefaID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Checkpoint{}
	for rows.Next() {
		var c Checkpoint
		if err := rows.Scan(&c.TarefaID, &c.Tabela, &c.UltimaChave, &c.Linhas, &c.Seq, &c.AtualizadoEm); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// UltimaVersaoAprovada é a versão do mapeamento que pode ser executada.
func (s *Store) UltimaVersaoAprovada(ctx context.Context, projetoID string) (VersaoMapeamento, error) {
	return scanVersao(s.pool.QueryRow(ctx, `SELECT `+colunasVersao+` FROM mapeamentos
		WHERE projeto_id=$1 AND estado='aprovado' ORDER BY versao DESC LIMIT 1`, projetoID))
}
