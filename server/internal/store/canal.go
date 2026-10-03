package store

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// --- chaves do painel (sempre cifradas pelo cofre) ---

// ChavePainel devolve o segredo cifrado (JSON de cofre.Segredo) guardado com `nome`.
func (s *Store) ChavePainel(ctx context.Context, nome string) ([]byte, error) {
	var b []byte
	err := s.pool.QueryRow(ctx, `SELECT segredo FROM painel_chaves WHERE nome=$1`, nome).Scan(&b)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return b, err
}

// SalvarChavePainel grava a chave só se ainda não existir (dois painéis subindo ao
// mesmo tempo não podem gerar duas CAs). Devolve ErrConflito se já havia uma.
func (s *Store) SalvarChavePainel(ctx context.Context, nome string, segredo []byte) error {
	tag, err := s.pool.Exec(ctx, `INSERT INTO painel_chaves (nome, segredo) VALUES ($1,$2) ON CONFLICT (nome) DO NOTHING`, nome, segredo)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflito
	}
	return nil
}

// --- tokens de inscrição ---

// CriarTokenAgente guarda o hash de um token de inscrição de uso único.
func (s *Store) CriarTokenAgente(ctx context.Context, hash, rotulo, criadoPor string, expira time.Time) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO agente_tokens (hash, rotulo, criado_por, expira_em) VALUES ($1,$2,$3,$4)`,
		hash, rotulo, criadoPor, expira)
	return err
}

// ErrTokenInvalido: token inexistente, vencido ou já usado (a resposta é a mesma de
// propósito — não ajuda quem está adivinhando).
var ErrTokenInvalido = errors.New("token de inscrição inválido, vencido ou já usado")

// ConsumirTokenAgente queima o token (atômico: dois agentes com o mesmo token — só um entra).
func (s *Store) ConsumirTokenAgente(ctx context.Context, hash, agenteID string) (rotulo string, err error) {
	err = s.pool.QueryRow(ctx, `
		UPDATE agente_tokens SET usado_em=now(), agente_id=$2
		 WHERE hash=$1 AND usado_em IS NULL AND expira_em > now()
		 RETURNING rotulo`, hash, agenteID).Scan(&rotulo)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrTokenInvalido
	}
	return rotulo, err
}

// --- agentes ---

// Agente é um agente inscrito.
type Agente struct {
	ID            string     `json:"id"`
	Rotulo        string     `json:"rotulo"`
	Hostname      string     `json:"hostname"`
	SO            string     `json:"so"`
	Arch          string     `json:"arch"`
	Versao        string     `json:"versao"`
	ChaveSelo     []byte     `json:"-"`
	CertSerial    string     `json:"-"`
	CertValidoAte time.Time  `json:"cert_valido_ate"`
	Revogado      bool       `json:"revogado"`
	Estado        string     `json:"estado"`
	Capacidades   []string   `json:"capacidades"`
	VistoEm       *time.Time `json:"visto_em,omitempty"`
	CriadoEm      time.Time  `json:"criado_em"`
}

const colunasAgente = `id, rotulo, hostname, so, arch, versao, chave_selo, cert_serial, cert_valido_ate, revogado, estado, capacidades, visto_em, criado_em`

func scanAgente(row pgx.Row) (Agente, error) {
	var a Agente
	err := row.Scan(&a.ID, &a.Rotulo, &a.Hostname, &a.SO, &a.Arch, &a.Versao, &a.ChaveSelo, &a.CertSerial,
		&a.CertValidoAte, &a.Revogado, &a.Estado, &a.Capacidades, &a.VistoEm, &a.CriadoEm)
	if errors.Is(err, pgx.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// RegistrarAgente grava um agente recém-inscrito.
func (s *Store) RegistrarAgente(ctx context.Context, a Agente) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO agentes (id, rotulo, hostname, so, arch, versao, chave_selo, cert_serial, cert_valido_ate)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		a.ID, a.Rotulo, a.Hostname, a.SO, a.Arch, a.Versao, a.ChaveSelo, a.CertSerial, a.CertValidoAte)
	return err
}

func (s *Store) AgentePorID(ctx context.Context, id string) (Agente, error) {
	return scanAgente(s.pool.QueryRow(ctx, `SELECT `+colunasAgente+` FROM agentes WHERE id=$1`, id))
}

func (s *Store) ListarAgentes(ctx context.Context) ([]Agente, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+colunasAgente+` FROM agentes ORDER BY revogado, hostname, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Agente{}
	for rows.Next() {
		a, err := scanAgente(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// TrocarCertificadoAgente registra o certificado renovado; o anterior deixa de valer.
func (s *Store) TrocarCertificadoAgente(ctx context.Context, id, serial string, validoAte time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE agentes SET cert_serial=$2, cert_valido_ate=$3 WHERE id=$1 AND NOT revogado`, id, serial, validoAte)
	return err
}

func (s *Store) RevogarAgente(ctx context.Context, id string) error {
	tag, err := s.pool.Exec(ctx, `UPDATE agentes SET revogado=TRUE, estado='offline' WHERE id=$1`, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AtualizarApresentacaoAgente guarda o que o agente disse no Olá.
func (s *Store) AtualizarApresentacaoAgente(ctx context.Context, id, hostname, so, arch, versao string, capacidades []string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE agentes SET hostname=$2, so=$3, arch=$4, versao=$5, capacidades=$6, visto_em=now() WHERE id=$1`,
		id, hostname, so, arch, versao, capacidades)
	return err
}

// MarcarPresencaAgente grava online/instavel/offline.
func (s *Store) MarcarPresencaAgente(ctx context.Context, id, estado string, vistoEm time.Time) error {
	_, err := s.pool.Exec(ctx, `UPDATE agentes SET estado=$2, visto_em=GREATEST(coalesce(visto_em, $3), $3) WHERE id=$1`, id, estado, vistoEm)
	return err
}

// --- tarefas ---

// Tarefa é uma execução pedida a um agente.
type Tarefa struct {
	ID            string          `json:"id"`
	Tipo          string          `json:"tipo"`
	AgenteID      string          `json:"agente_id"`
	Estado        string          `json:"estado"`
	Especificacao json.RawMessage `json:"especificacao"`
	Progresso     float64         `json:"progresso"`
	IniciadaPor   string          `json:"iniciada_por"`
	Origem        string          `json:"origem"`
	CorrelacaoID  string          `json:"correlacao_id"`
	ExpiraEm      time.Time       `json:"expira_em"`
	CriadaEm      time.Time       `json:"criada_em"`
	Inicio        *time.Time      `json:"inicio,omitempty"`
	Fim           *time.Time      `json:"fim,omitempty"`
	Erro          string          `json:"erro,omitempty"`
	Resumo        json.RawMessage `json:"resumo,omitempty"`
}

const colunasTarefa = `id, tipo, agente_id, estado, especificacao, progresso, iniciada_por, origem, correlacao_id, expira_em, criada_em, inicio, fim, erro, resumo`

func scanTarefa(row pgx.Row) (Tarefa, error) {
	var t Tarefa
	err := row.Scan(&t.ID, &t.Tipo, &t.AgenteID, &t.Estado, &t.Especificacao, &t.Progresso, &t.IniciadaPor,
		&t.Origem, &t.CorrelacaoID, &t.ExpiraEm, &t.CriadaEm, &t.Inicio, &t.Fim, &t.Erro, &t.Resumo)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

func (s *Store) CriarTarefa(ctx context.Context, t Tarefa) error {
	esp := t.Especificacao
	if len(esp) == 0 {
		esp = json.RawMessage(`{}`)
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO tarefas (id, tipo, agente_id, estado, especificacao, iniciada_por, origem, correlacao_id, expira_em)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`,
		t.ID, t.Tipo, t.AgenteID, t.Estado, esp, t.IniciadaPor, t.Origem, t.CorrelacaoID, t.ExpiraEm)
	return err
}

func (s *Store) TarefaPorID(ctx context.Context, id string) (Tarefa, error) {
	return scanTarefa(s.pool.QueryRow(ctx, `SELECT `+colunasTarefa+` FROM tarefas WHERE id=$1`, id))
}

// FiltroTarefas filtra a lista (campos vazios não filtram).
type FiltroTarefas struct {
	AgenteID string
	Tipo     string
	Limite   int
}

func (s *Store) ListarTarefas(ctx context.Context, f FiltroTarefas) ([]Tarefa, error) {
	if f.Limite <= 0 || f.Limite > 500 {
		f.Limite = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT `+colunasTarefa+` FROM tarefas
		 WHERE ($1 = '' OR agente_id = $1) AND ($2 = '' OR tipo = $2)
		 ORDER BY criada_em DESC LIMIT $3`, f.AgenteID, f.Tipo, f.Limite)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Tarefa{}
	for rows.Next() {
		t, err := scanTarefa(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// TarefasEmAberto são as que ainda precisam chegar ou estão rodando no agente.
func (s *Store) TarefasEmAberto(ctx context.Context, agenteID string) ([]Tarefa, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+colunasTarefa+` FROM tarefas
		 WHERE agente_id=$1 AND estado IN ('na_fila','enviada','executando','pausada') ORDER BY criada_em`, agenteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Tarefa{}
	for rows.Next() {
		t, err := scanTarefa(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// estadosFinais não mudam mais: um resultado repetido (reenvio) não reescreve nada.
const estadosFinais = `('sucesso','falha','cancelada','recusada')`

// MudarEstadoTarefa troca o estado (e marca início/fim). Ignora tarefa já finalizada.
func (s *Store) MudarEstadoTarefa(ctx context.Context, id, estado string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE tarefas SET estado=$2,
		       inicio = CASE WHEN $2 = 'executando' AND inicio IS NULL THEN now() ELSE inicio END
		 WHERE id=$1 AND estado NOT IN `+estadosFinais, id, estado)
	return err
}

// FinalizarTarefa grava o resultado (uma vez só).
func (s *Store) FinalizarTarefa(ctx context.Context, id, estado, erro string, resumo json.RawMessage) error {
	if len(resumo) == 0 {
		resumo = nil
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE tarefas SET estado=$2, erro=$3, resumo=$4, fim=now(),
		       progresso = CASE WHEN $2 = 'sucesso' THEN 100 ELSE progresso END
		 WHERE id=$1 AND estado NOT IN `+estadosFinais, id, estado, erro, resumo)
	return err
}

// EventoTarefa é um passo reportado pelo agente.
type EventoTarefa struct {
	TarefaID  string             `json:"tarefa_id"`
	Seq       int64              `json:"seq"`
	Em        time.Time          `json:"em"`
	Etapa     string             `json:"etapa"`
	Nivel     string             `json:"nivel"`
	Mensagem  string             `json:"mensagem"`
	Progresso float64            `json:"progresso"`
	Metricas  map[string]float64 `json:"metricas,omitempty"`
}

// GravarEventoTarefa grava o evento (idempotente) e avança o progresso da tarefa.
// `novo` é false quando o evento já existia (reenvio depois de uma queda).
func (s *Store) GravarEventoTarefa(ctx context.Context, e EventoTarefa) (novo bool, err error) {
	met, err := json.Marshal(e.Metricas)
	if err != nil || e.Metricas == nil {
		met = []byte(`{}`)
	}
	err = pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO tarefa_eventos (tarefa_id, seq, em, etapa, nivel, mensagem, progresso, metricas)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (tarefa_id, seq) DO NOTHING`,
			e.TarefaID, e.Seq, e.Em, e.Etapa, e.Nivel, e.Mensagem, e.Progresso, met)
		if err != nil {
			return err
		}
		novo = tag.RowsAffected() == 1
		if !novo {
			return nil
		}
		_, err = tx.Exec(ctx, `
			UPDATE tarefas SET progresso = GREATEST(progresso, $2),
			       estado = CASE WHEN estado IN ('na_fila','enviada') THEN 'executando' ELSE estado END,
			       inicio = coalesce(inicio, now())
			 WHERE id=$1 AND estado NOT IN `+estadosFinais, e.TarefaID, e.Progresso)
		return err
	})
	return novo, err
}

// EventosTarefa devolve os eventos depois de `aposSeq` (0 = todos), em ordem.
func (s *Store) EventosTarefa(ctx context.Context, tarefaID string, aposSeq int64) ([]EventoTarefa, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tarefa_id, seq, em, etapa, nivel, mensagem, progresso, metricas
		  FROM tarefa_eventos WHERE tarefa_id=$1 AND seq > $2 ORDER BY seq LIMIT 5000`, tarefaID, aposSeq)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []EventoTarefa{}
	for rows.Next() {
		var e EventoTarefa
		var met []byte
		if err := rows.Scan(&e.TarefaID, &e.Seq, &e.Em, &e.Etapa, &e.Nivel, &e.Mensagem, &e.Progresso, &met); err != nil {
			return nil, err
		}
		if len(met) > 2 {
			if err := json.Unmarshal(met, &e.Metricas); err != nil {
				return nil, err
			}
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
