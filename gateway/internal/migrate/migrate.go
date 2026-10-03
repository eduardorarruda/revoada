// Package migrate aplica migrations SQL do ClickHouse em ordem, registrando
// o que já foi aplicado numa tabela schema_migrations (idempotente).
package migrate

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/eduardorarruda/revoada/gateway/internal/chhttp"
)

const bootstrap = `CREATE TABLE IF NOT EXISTS schema_migrations
(
    version    String,
    applied_at DateTime DEFAULT now()
)
ENGINE = MergeTree
ORDER BY version`

// Runner aplica/reverte migrations.
type Runner struct {
	ch  *chhttp.Client
	dir string
}

// New cria um Runner apontando para o diretório de migrations.
func New(ch *chhttp.Client, dir string) *Runner {
	return &Runner{ch: ch, dir: dir}
}

// splitStatements quebra um arquivo .sql em instruções individuais.
// Ignora linhas de comentário (--) e instruções vazias.
func splitStatements(sql string) []string {
	// 1) remove linhas de comentário inteiras ANTES de dividir — um comentário
	//    pode conter ';' e quebraria a divisão de instruções.
	var b strings.Builder
	for _, ln := range strings.Split(sql, "\n") {
		t := strings.TrimSpace(ln)
		if t == "" || strings.HasPrefix(t, "--") {
			continue
		}
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	// 2) divide por ';' o SQL já sem comentários.
	var out []string
	for _, raw := range strings.Split(b.String(), ";") {
		if stmt := strings.TrimSpace(raw); stmt != "" {
			out = append(out, stmt)
		}
	}
	return out
}

// versions lista as versões (nome sem sufixo) dos arquivos com dado sufixo, em ordem.
func (r *Runner) versions(suffix string) ([]string, error) {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return nil, fmt.Errorf("lendo %s: %w", r.dir, err)
	}
	var vs []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), suffix) {
			continue
		}
		vs = append(vs, strings.TrimSuffix(e.Name(), suffix))
	}
	sort.Strings(vs)
	return vs, nil
}

func (r *Runner) applied(ctx context.Context) (map[string]bool, error) {
	out, err := r.ch.Exec(ctx, "SELECT version FROM schema_migrations FORMAT TabSeparated")
	if err != nil {
		return nil, err
	}
	set := map[string]bool{}
	for _, ln := range strings.Split(strings.TrimSpace(out), "\n") {
		if v := strings.TrimSpace(ln); v != "" {
			set[v] = true
		}
	}
	return set, nil
}

// Up aplica todas as migrations .up.sql ainda não aplicadas. Devolve as versões aplicadas nesta rodada.
func (r *Runner) Up(ctx context.Context) ([]string, error) {
	if _, err := r.ch.Exec(ctx, bootstrap); err != nil {
		return nil, fmt.Errorf("bootstrap schema_migrations: %w", err)
	}
	done, err := r.applied(ctx)
	if err != nil {
		return nil, err
	}
	vs, err := r.versions(".up.sql")
	if err != nil {
		return nil, err
	}
	var newly []string
	for _, v := range vs {
		if done[v] {
			continue
		}
		sqlBytes, err := os.ReadFile(filepath.Join(r.dir, v+".up.sql"))
		if err != nil {
			return newly, err
		}
		for _, stmt := range splitStatements(string(sqlBytes)) {
			if _, err := r.ch.Exec(ctx, stmt); err != nil {
				return newly, fmt.Errorf("migration %s: %w", v, err)
			}
		}
		if _, err := r.ch.Exec(ctx, fmt.Sprintf("INSERT INTO schema_migrations (version) VALUES ('%s')", v)); err != nil {
			return newly, fmt.Errorf("registrando %s: %w", v, err)
		}
		newly = append(newly, v)
	}
	return newly, nil
}

// Down reverte a última migration aplicada (usando o .down.sql correspondente).
func (r *Runner) Down(ctx context.Context) (string, error) {
	done, err := r.applied(ctx)
	if err != nil {
		return "", err
	}
	vs, err := r.versions(".up.sql")
	if err != nil {
		return "", err
	}
	// última aplicada = maior versão em `done`
	var last string
	for _, v := range vs {
		if done[v] {
			last = v
		}
	}
	if last == "" {
		return "", nil
	}
	sqlBytes, err := os.ReadFile(filepath.Join(r.dir, last+".down.sql"))
	if err != nil {
		return "", fmt.Errorf("sem .down.sql para %s: %w", last, err)
	}
	for _, stmt := range splitStatements(string(sqlBytes)) {
		if _, err := r.ch.Exec(ctx, stmt); err != nil {
			return "", fmt.Errorf("down %s: %w", last, err)
		}
	}
	if _, err := r.ch.Exec(ctx, fmt.Sprintf("ALTER TABLE schema_migrations DELETE WHERE version = '%s'", last)); err != nil {
		return "", err
	}
	return last, nil
}
