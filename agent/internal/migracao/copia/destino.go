package copia

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/plano"
	"github.com/eduardorarruda/revoada/core/migracao/transformar"
)

// Tabelas de controle no destino (prefixo revoada_, no mesmo schema): o checkpoint
// é gravado NA MESMA TRANSAÇÃO do lote — se o agente cair entre o commit e o aviso
// ao painel, a retomada lê daqui e não duplica nem pula linha.
const (
	tabelaControle  = "revoada_controle"
	tabelaManifesto = "revoada_manifesto"
)

// limiteParametros do PostgreSQL por comando (65 535), com folga.
const limiteParametros = 60000

type destino struct {
	db *sql.DB
	d  dialeto
}

func (g destino) controle() string  { t, _ := g.d.tabela(tabelaControle); return t }
func (g destino) manifesto() string { t, _ := g.d.tabela(tabelaManifesto); return t }

// garantirControle cria as tabelas de controle. A coluna `conteudo` (somas do
// conteúdo de cada coluna, conteudo.go) entrou depois: ADD COLUMN IF NOT EXISTS com
// padrão vazio mantém compatível um destino com checkpoints de um agente antigo — e
// checkpoint sem conteúdo nunca vira "conteúdo conferido" (executar.go).
func (g destino) garantirControle(ctx context.Context) error {
	_, err := g.db.ExecContext(ctx, fmt.Sprintf(`
CREATE TABLE IF NOT EXISTS %s (
  execucao TEXT NOT NULL, tabela TEXT NOT NULL, ultima_chave TEXT NOT NULL DEFAULT '',
  linhas BIGINT NOT NULL DEFAULT 0, soma TEXT NOT NULL DEFAULT '', concluida BOOLEAN NOT NULL DEFAULT false,
  atualizado_em TIMESTAMPTZ NOT NULL DEFAULT now(), PRIMARY KEY (execucao, tabela));
CREATE TABLE IF NOT EXISTS %s (
  execucao TEXT PRIMARY KEY, conteudo JSONB NOT NULL, atualizado_em TIMESTAMPTZ NOT NULL DEFAULT now());
ALTER TABLE %s ADD COLUMN IF NOT EXISTS conteudo TEXT NOT NULL DEFAULT ''`,
		g.controle(), g.manifesto(), g.controle()))
	if err != nil {
		return fmt.Errorf("criando as tabelas de controle no destino (o usuário precisa de CREATE no schema): %w", err)
	}
	return nil
}

type executor interface {
	ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error)
	QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row
}

func (g destino) lerManifesto(ctx context.Context, q executor, execucao string) (*plano.Manifesto, error) {
	var b []byte
	err := q.QueryRowContext(ctx, "SELECT conteudo FROM "+g.manifesto()+" WHERE execucao = $1", execucao).Scan(&b)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m plano.Manifesto
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return &m, nil
}

func (g destino) gravarManifesto(ctx context.Context, q executor, m plano.Manifesto) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, "INSERT INTO "+g.manifesto()+` (execucao, conteudo) VALUES ($1, $2)
ON CONFLICT (execucao) DO UPDATE SET conteudo = EXCLUDED.conteudo, atualizado_em = now()`, m.Execucao, b)
	return err
}

type checkpoint struct {
	chave     string
	linhas    int64
	soma      string
	concluida bool
	// conteudo: as somas por coluna (JSON, conteudo.go). Vazio = checkpoint de um
	// agente que ainda não conferia conteúdo.
	conteudo string
}

func (g destino) lerCheckpoint(ctx context.Context, execucao, tabela string) (checkpoint, error) {
	var c checkpoint
	err := g.db.QueryRowContext(ctx, "SELECT ultima_chave, linhas, soma, concluida, conteudo FROM "+g.controle()+
		" WHERE execucao = $1 AND tabela = $2", execucao, tabela).Scan(&c.chave, &c.linhas, &c.soma, &c.concluida, &c.conteudo)
	if errors.Is(err, sql.ErrNoRows) {
		return checkpoint{}, nil
	}
	return c, err
}

func (g destino) gravarCheckpoint(ctx context.Context, q executor, execucao, tabela string, c checkpoint) error {
	_, err := q.ExecContext(ctx, "INSERT INTO "+g.controle()+` (execucao, tabela, ultima_chave, linhas, soma, concluida, conteudo)
VALUES ($1, $2, $3, $4, $5, $6, $7) ON CONFLICT (execucao, tabela) DO UPDATE SET ultima_chave = EXCLUDED.ultima_chave,
linhas = EXCLUDED.linhas, soma = EXCLUDED.soma, concluida = EXCLUDED.concluida, conteudo = EXCLUDED.conteudo,
atualizado_em = now()`,
		execucao, tabela, c.chave, c.linhas, c.soma, c.concluida, c.conteudo)
	return err
}

// casasTempo lê do catálogo do destino as casas de segundo de cada coluna de data/
// hora (timestamp(0), time(3)…): a foto do schema não guarda, e o PostgreSQL
// arredonda para elas ao gravar.
func (g destino) casasTempo(ctx context.Context, tabela string) (map[string]int, error) {
	rows, err := g.db.QueryContext(ctx, `SELECT column_name, datetime_precision FROM information_schema.columns
WHERE table_schema = $1 AND table_name = $2 AND datetime_precision IS NOT NULL`, g.d.esquema, tabela)
	if err != nil {
		return nil, fmt.Errorf("lendo a precisão das colunas de %s: %w", tabela, err)
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var nome string
		var p int
		if err := rows.Scan(&nome, &p); err != nil {
			return nil, err
		}
		out[nome] = p
	}
	return out, rows.Err()
}

// vazia diz se a tabela não tem nenhuma linha.
func (g destino) vazia(ctx context.Context, tabela string) (bool, error) {
	t, err := g.d.tabela(tabela)
	if err != nil {
		return false, err
	}
	var x int
	err = g.db.QueryRowContext(ctx, "SELECT 1 FROM "+t+" LIMIT 1").Scan(&x)
	if errors.Is(err, sql.ErrNoRows) {
		return true, nil
	}
	return false, err
}

// inserir grava linhas com INSERT de várias linhas por comando. Valores vão como
// parâmetro; UsarPadrao vira DEFAULT. OVERRIDING SYSTEM VALUE deixa gravar o id
// original em coluna identity (depois o finalizar acerta a sequência).
func (g destino) inserir(ctx context.Context, q executor, tabela string, cols []esquema.Coluna, linhas [][]any) error {
	if len(linhas) == 0 {
		return nil
	}
	t, err := g.d.tabela(tabela)
	if err != nil {
		return err
	}
	nomes := make([]string, len(cols))
	for i, c := range cols {
		if nomes[i], err = g.d.id(c.Nome); err != nil {
			return err
		}
	}
	cabeca := fmt.Sprintf("INSERT INTO %s (%s) OVERRIDING SYSTEM VALUE VALUES ", t, strings.Join(nomes, ", "))
	porComando := max(limiteParametros/max(len(cols), 1), 1)
	for ini := 0; ini < len(linhas); ini += porComando {
		fim := min(ini+porComando, len(linhas))
		var b strings.Builder
		b.WriteString(cabeca)
		args := make([]any, 0, (fim-ini)*len(cols))
		for i, ln := range linhas[ini:fim] {
			if i > 0 {
				b.WriteString(", ")
			}
			b.WriteByte('(')
			for j, v := range ln {
				if j > 0 {
					b.WriteString(", ")
				}
				if v == transformar.UsarPadrao {
					b.WriteString("DEFAULT")
					continue
				}
				args = append(args, v)
				fmt.Fprintf(&b, "$%d", len(args))
			}
			b.WriteByte(')')
		}
		if _, err := q.ExecContext(ctx, b.String(), args...); err != nil {
			return fmt.Errorf("gravando em %s: %w", tabela, err)
		}
	}
	return nil
}

// nomeAuxiliar gera o nome da staging/cópia prévia, dentro dos 63 bytes do PG. O
// hash cobre execução E tabela inteira: dois nomes longos com o mesmo começo não
// colidem quando o final é cortado.
func nomeAuxiliar(tipo, execucao, tabela string) string {
	h := sha256.Sum256([]byte(execucao + "\x00" + tabela))
	n := fmt.Sprintf("revoada_%s_%s_%s", tipo, hex.EncodeToString(h[:6]), tabela)
	if len(n) > 63 {
		n = n[:63]
	}
	return n
}

// ---------------------------------------------------------------- checksum

// soma é um checksum que não depende da ordem: soma (mod 2^64) do hash de cada
// chave. Calculado ao gravar e de novo lendo o destino — se bater, chegaram as
// mesmas linhas (nem a mais, nem a menos, nem trocadas).
type soma uint64

func (s *soma) add(chave string) {
	h := sha256.Sum256([]byte(chave))
	var x uint64
	for _, b := range h[:8] {
		x = x<<8 | uint64(b)
	}
	*s += soma(x)
}

func (s soma) String() string { return fmt.Sprintf("%016x", uint64(s)) }

// somaInvalida marca a tabela cuja coluna de conferência recebeu DEFAULT em alguma
// linha: o valor real só existe no destino, então a conferência fica só na contagem.
const somaInvalida = "sem_checksum"

func lerSoma(t string) soma {
	var x uint64
	_, _ = fmt.Sscanf(t, "%x", &x)
	return soma(x)
}

// reler lê a tabela do destino UMA vez, em sequência, e devolve a contagem, a soma
// das chaves (checksum) e as somas do conteúdo de cada coluna — calculadas sobre o
// que o PostgreSQL de fato guardou, com a mesma forma canônica usada ao gravar.
func (g destino) reler(ctx context.Context, tabela string, dest []esquema.Coluna, iSoma int, c conferencia) (int64, soma, somasConteudo, error) {
	s := novasSomas(len(dest))
	t, err := g.d.tabela(tabela)
	if err != nil {
		return 0, 0, s, err
	}
	sel, err := g.listaColunas(dest)
	if err != nil {
		return 0, 0, s, err
	}
	rows, err := g.db.QueryContext(ctx, "SELECT "+sel+" FROM "+t)
	if err != nil {
		return 0, 0, s, err
	}
	defer rows.Close()
	var n int64
	var sk soma
	vals := make([]any, len(dest))
	ptrs := make([]any, len(dest))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	for rows.Next() {
		if err := rows.Scan(ptrs...); err != nil {
			return 0, 0, s, err
		}
		n++
		sk.add(chaveNormal(vals[iSoma], dest[iSoma]))
		s.add(c, vals)
	}
	return n, sk, s, rows.Err()
}

// listaColunas: as colunas gravadas, entre aspas, na ordem do mapeamento.
func (g destino) listaColunas(dest []esquema.Coluna) (string, error) {
	cols := make([]string, len(dest))
	for i, c := range dest {
		q, err := g.d.id(c.Nome)
		if err != nil {
			return "", err
		}
		cols[i] = q
	}
	return strings.Join(cols, ", "), nil
}

// chaveNormal deixa o valor no MESMO formato dos dois lados (gravado e relido).
func chaveNormal(v any, c esquema.Coluna) string {
	if v == nil {
		return ""
	}
	aj, _, vio := transformar.Ajustar(v, c)
	if vio != nil || aj == transformar.UsarPadrao {
		return transformar.ChaveTexto(v)
	}
	return transformar.ChaveTexto(aj)
}
