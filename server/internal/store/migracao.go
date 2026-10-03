package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ConexaoBanco é um banco cadastrado. Credencial é o cofre.Segredo serializado e
// NUNCA sai da API (json:"-").
type ConexaoBanco struct {
	ID                string            `json:"id"`
	Nome              string            `json:"nome"`
	Motor             string            `json:"motor"`
	Endereco          string            `json:"endereco"`
	Banco             string            `json:"banco"`
	Usuario           string            `json:"usuario"`
	Credencial        json.RawMessage   `json:"-"`
	Opcoes            map[string]string `json:"opcoes"`
	AgentePreferidoID *string           `json:"agente_preferido_id,omitempty"`
	VersaoDetectada   string            `json:"versao_detectada"`
	CriadaPor         string            `json:"criada_por"`
	CriadaEm          time.Time         `json:"criada_em"`
}

const colunasConexao = `id, nome, motor, endereco, banco, usuario, credencial, opcoes, agente_preferido_id, versao_detectada, criada_por, criada_em`

func scanConexao(row pgx.Row) (ConexaoBanco, error) {
	var c ConexaoBanco
	var opcoes []byte
	err := row.Scan(&c.ID, &c.Nome, &c.Motor, &c.Endereco, &c.Banco, &c.Usuario, &c.Credencial, &opcoes,
		&c.AgentePreferidoID, &c.VersaoDetectada, &c.CriadaPor, &c.CriadaEm)
	if errors.Is(err, pgx.ErrNoRows) {
		return c, ErrNotFound
	}
	if err == nil && len(opcoes) > 0 {
		err = json.Unmarshal(opcoes, &c.Opcoes)
	}
	return c, err
}

func (s *Store) CriarConexao(ctx context.Context, c ConexaoBanco) error {
	op, err := json.Marshal(c.Opcoes)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO conexoes_banco (id, nome, motor, endereco, banco, usuario, credencial, opcoes, agente_preferido_id, criada_por)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		c.ID, c.Nome, c.Motor, c.Endereco, c.Banco, c.Usuario, c.Credencial, op, c.AgentePreferidoID, c.CriadaPor)
	return err
}

func (s *Store) ConexaoPorID(ctx context.Context, id string) (ConexaoBanco, error) {
	return scanConexao(s.pool.QueryRow(ctx, `SELECT `+colunasConexao+` FROM conexoes_banco WHERE id=$1`, id))
}

func (s *Store) ListarConexoes(ctx context.Context) ([]ConexaoBanco, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+colunasConexao+` FROM conexoes_banco ORDER BY nome`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ConexaoBanco{}
	for rows.Next() {
		c, err := scanConexao(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// TrocarCredencialConexao grava uma senha nova (já cifrada).
func (s *Store) TrocarCredencialConexao(ctx context.Context, id string, credencial []byte) error {
	tag, err := s.pool.Exec(ctx, `UPDATE conexoes_banco SET credencial=$2 WHERE id=$1`, id, credencial)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

func (s *Store) ApagarConexao(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM conexoes_banco WHERE id=$1`, id)
	if err == nil && tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return err
}

// EsquemaCapturado é uma foto da estrutura de um banco.
type EsquemaCapturado struct {
	ID          string          `json:"id"`
	ConexaoID   string          `json:"conexao_id"`
	AgenteID    string          `json:"agente_id"`
	Hash        string          `json:"hash"`
	Conteudo    json.RawMessage `json:"conteudo"`
	CapturadoEm time.Time       `json:"capturado_em"`
}

// GravarEsquema guarda a foto e atualiza a versão detectada da conexão.
func (s *Store) GravarEsquema(ctx context.Context, e EsquemaCapturado, versao string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `INSERT INTO esquemas_capturados (id, conexao_id, agente_id, hash, conteudo) VALUES ($1,$2,$3,$4,$5)`,
			e.ID, e.ConexaoID, e.AgenteID, e.Hash, e.Conteudo); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `UPDATE conexoes_banco SET versao_detectada=$2 WHERE id=$1`, e.ConexaoID, versao)
		return err
	})
}

// UltimoEsquema devolve a foto mais recente de uma conexão.
func (s *Store) UltimoEsquema(ctx context.Context, conexaoID string) (EsquemaCapturado, error) {
	var e EsquemaCapturado
	err := s.pool.QueryRow(ctx, `SELECT id, conexao_id, agente_id, hash, conteudo, capturado_em FROM esquemas_capturados
		WHERE conexao_id=$1 ORDER BY capturado_em DESC LIMIT 1`, conexaoID).
		Scan(&e.ID, &e.ConexaoID, &e.AgenteID, &e.Hash, &e.Conteudo, &e.CapturadoEm)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

func (s *Store) EsquemaPorID(ctx context.Context, id string) (EsquemaCapturado, error) {
	var e EsquemaCapturado
	err := s.pool.QueryRow(ctx, `SELECT id, conexao_id, agente_id, hash, conteudo, capturado_em FROM esquemas_capturados WHERE id=$1`, id).
		Scan(&e.ID, &e.ConexaoID, &e.AgenteID, &e.Hash, &e.Conteudo, &e.CapturadoEm)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// ProjetoMigracao agrupa origem, destino e as versões do mapeamento.
type ProjetoMigracao struct {
	ID        string    `json:"id"`
	Nome      string    `json:"nome"`
	Tipo      string    `json:"tipo"`
	OrigemID  string    `json:"origem_id"`
	DestinoID *string   `json:"destino_id,omitempty"`
	Estado    string    `json:"estado"`
	CriadoPor string    `json:"criado_por"`
	CriadoEm  time.Time `json:"criado_em"`
}

func (s *Store) CriarProjeto(ctx context.Context, p ProjetoMigracao) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO projetos_migracao (id, nome, tipo, origem_id, destino_id, criado_por) VALUES ($1,$2,$3,$4,$5,$6)`,
		p.ID, p.Nome, p.Tipo, p.OrigemID, p.DestinoID, p.CriadoPor)
	return err
}

func (s *Store) ProjetoPorID(ctx context.Context, id string) (ProjetoMigracao, error) {
	var p ProjetoMigracao
	err := s.pool.QueryRow(ctx, `SELECT id, nome, tipo, origem_id, destino_id, estado, criado_por, criado_em FROM projetos_migracao WHERE id=$1`, id).
		Scan(&p.ID, &p.Nome, &p.Tipo, &p.OrigemID, &p.DestinoID, &p.Estado, &p.CriadoPor, &p.CriadoEm)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

func (s *Store) ListarProjetos(ctx context.Context) ([]ProjetoMigracao, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, nome, tipo, origem_id, destino_id, estado, criado_por, criado_em FROM projetos_migracao ORDER BY criado_em DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProjetoMigracao{}
	for rows.Next() {
		var p ProjetoMigracao
		if err := rows.Scan(&p.ID, &p.Nome, &p.Tipo, &p.OrigemID, &p.DestinoID, &p.Estado, &p.CriadoPor, &p.CriadoEm); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// VersaoMapeamento é uma versão do mapeamento de um projeto.
type VersaoMapeamento struct {
	ProjetoID        string          `json:"projeto_id"`
	Versao           int             `json:"versao"`
	EsquemaOrigemID  *string         `json:"esquema_origem_id,omitempty"`
	EsquemaDestinoID *string         `json:"esquema_destino_id,omitempty"`
	Origem           string          `json:"origem"`
	Conteudo         json.RawMessage `json:"conteudo"`
	Hash             string          `json:"hash"`
	Problemas        json.RawMessage `json:"problemas"`
	Estado           string          `json:"estado"`
	CriadoPor        string          `json:"criado_por"`
	CriadoEm         time.Time       `json:"criado_em"`
	AprovadoPor      *string         `json:"aprovado_por,omitempty"`
	AprovadoEm       *time.Time      `json:"aprovado_em,omitempty"`
}

const colunasVersao = `projeto_id, versao, esquema_origem_id, esquema_destino_id, origem, conteudo, hash, problemas, estado, criado_por, criado_em, aprovado_por, aprovado_em`

func scanVersao(row pgx.Row) (VersaoMapeamento, error) {
	var v VersaoMapeamento
	err := row.Scan(&v.ProjetoID, &v.Versao, &v.EsquemaOrigemID, &v.EsquemaDestinoID, &v.Origem, &v.Conteudo, &v.Hash,
		&v.Problemas, &v.Estado, &v.CriadoPor, &v.CriadoEm, &v.AprovadoPor, &v.AprovadoEm)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, ErrNotFound
	}
	return v, err
}

// NovaVersaoMapeamento grava uma versão nova (número = última + 1, atômico).
func (s *Store) NovaVersaoMapeamento(ctx context.Context, v VersaoMapeamento) (VersaoMapeamento, error) {
	return scanVersao(s.pool.QueryRow(ctx, `
		INSERT INTO mapeamentos (projeto_id, versao, esquema_origem_id, esquema_destino_id, origem, conteudo, hash, problemas, estado, criado_por)
		VALUES ($1, (SELECT coalesce(max(versao), 0) + 1 FROM mapeamentos WHERE projeto_id=$1), $2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING `+colunasVersao,
		v.ProjetoID, v.EsquemaOrigemID, v.EsquemaDestinoID, v.Origem, v.Conteudo, v.Hash, v.Problemas, v.Estado, v.CriadoPor))
}

// UltimaVersaoMapeamento devolve a versão mais recente.
func (s *Store) UltimaVersaoMapeamento(ctx context.Context, projetoID string) (VersaoMapeamento, error) {
	return scanVersao(s.pool.QueryRow(ctx, `SELECT `+colunasVersao+` FROM mapeamentos WHERE projeto_id=$1 ORDER BY versao DESC LIMIT 1`, projetoID))
}

func (s *Store) VersaoMapeamentoN(ctx context.Context, projetoID string, versao int) (VersaoMapeamento, error) {
	return scanVersao(s.pool.QueryRow(ctx, `SELECT `+colunasVersao+` FROM mapeamentos WHERE projeto_id=$1 AND versao=$2`, projetoID, versao))
}

// AprovarMapeamento marca a versão como aprovada (só se for 'valido') e o projeto também.
func (s *Store) AprovarMapeamento(ctx context.Context, projetoID string, versao int, por string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE mapeamentos SET estado='aprovado', aprovado_por=$3, aprovado_em=now()
			WHERE projeto_id=$1 AND versao=$2 AND estado='valido'`, projetoID, versao, por)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return ErrConflito
		}
		_, err = tx.Exec(ctx, `UPDATE projetos_migracao SET estado='aprovado' WHERE id=$1`, projetoID)
		return err
	})
}
