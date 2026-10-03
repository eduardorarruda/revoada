package upgrade

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/agent/internal/migracao/copia"
	up "github.com/eduardorarruda/revoada/core/migracao/upgrade"
	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
	fb "github.com/nakagami/firebirdsql"
)

// intervaloEventos: a saída do gbak tem uma linha por objeto; o painel recebe no
// máximo uma a cada segundo (mais a última).
const intervaloEventos = time.Second

// Upgrade sobe o banco para o Firebird 5 (ARQUITETURA §9.5): backup pelo serviço da versão
// ANTIGA (o gbak que sabe ler aquele ODS), restore pelo serviço do FB5 num arquivo
// NOVO (create, nunca replace) e validação — contagem de linhas de cada tabela,
// valor de cada gerador e quantidade de objetos. O original não é alterado; reverter
// é descartar o banco novo.
func Upgrade(ctx context.Context, bruto []byte, r Relator) (any, error) {
	esp, err := lerEspecificacao(bruto)
	if err != nil {
		return nil, err
	}
	if esp.Destino == nil || esp.Diretorio == "" {
		return nil, fmt.Errorf("o upgrade precisa do servidor Firebird 5 (destino) e do diretório compartilhado")
	}
	inicio := time.Now()
	sOri, err := senha(r, "origem")
	if err != nil {
		return nil, err
	}
	sDest, err := senha(r, "destino")
	if err != nil {
		return nil, err
	}
	res := &up.ResumoUpgrade{Execucao: esp.Execucao, NovoBanco: esp.Destino.Banco,
		Backup:   juntar(esp.Diretorio, "revoada_"+esp.Execucao+".fbk"),
		Reverter: "descartar o banco novo (" + esp.Destino.Banco + "); o original nunca foi alterado"}
	defer func() { res.DuracaoMS = time.Since(inicio).Milliseconds() }()

	progresso := func(etapa string, base, faixa float64) func(string) {
		var n int
		ultimo := time.Time{}
		return func(l string) {
			n++
			if time.Since(ultimo) < intervaloEventos {
				return
			}
			ultimo = time.Now()
			// o gbak não diz o total; a barra anda devagar dentro da faixa da etapa
			pct := base + faixa*(1-1/(1+float64(n)/200))
			r.Evento(etapa, agentev1.EventoTarefa_INFO, l, pct, map[string]float64{"linhas_gbak": float64(n)})
		}
	}

	r.Evento("backup", agentev1.EventoTarefa_INFO, "backup pelo serviço do servidor antigo → "+res.Backup, 1, nil)
	if err := servico(ctx, esp.Origem, sOri, spbBackup(esp.Origem.Banco, res.Backup, false), progresso("backup", 1, 44)); err != nil {
		return res, fmt.Errorf("backup: %w", err)
	}
	if err := r.Pausa(ctx); err != nil {
		return res, err
	}
	msg := "restore no Firebird 5 → " + esp.Destino.Banco
	if esp.CharsetFix != "" {
		msg += " (FIX_FSS " + esp.CharsetFix + ")"
	}
	r.Evento("restore", agentev1.EventoTarefa_INFO, msg, 45, nil)
	if err := servico(ctx, *esp.Destino, sDest, spbRestore(res.Backup, esp.Destino.Banco, esp.CharsetFix, esp.Workers),
		progresso("restore", 45, 45)); err != nil {
		return res, fmt.Errorf("restore (o original segue intacto): %w", err)
	}
	r.Evento("validar", agentev1.EventoTarefa_INFO, "conferindo linhas, geradores e objetos nos dois bancos", 91, nil)
	if err := validar(ctx, esp, sOri, sDest, res); err != nil {
		return res, fmt.Errorf("validação: %w", err)
	}
	if !res.TudoConfere {
		return res, fmt.Errorf("o banco novo não confere com o original (veja o resumo); não use o banco novo — descarte e investigue")
	}
	r.Evento("validar", agentev1.EventoTarefa_INFO, fmt.Sprintf("upgrade concluído: Firebird %s, ODS %s, %d tabelas conferidas",
		res.Versao, res.ODS, len(res.Tabelas)), 100, nil)
	return res, nil
}

func validar(ctx context.Context, esp up.Especificacao, sOri, sDest string, res *up.ResumoUpgrade) error {
	velho, err := abrir(ctx, esp.Origem, sOri, "")
	if err != nil {
		return err
	}
	defer velho.Close()
	novo, err := abrir(ctx, *esp.Destino, sDest, "")
	if err != nil {
		return err
	}
	defer novo.Close()
	var maj, mnr int
	if err := novo.QueryRowContext(ctx, "SELECT rdb$get_context('SYSTEM', 'ENGINE_VERSION') FROM rdb$database").Scan(&res.Versao); err != nil {
		return err
	}
	if err := novo.QueryRowContext(ctx, "SELECT MON$ODS_MAJOR, MON$ODS_MINOR FROM MON$DATABASE").Scan(&maj, &mnr); err != nil {
		return err
	}
	res.ODS = fmt.Sprintf("%d.%d", maj, mnr)

	tabelas, err := nomes(ctx, velho, "SELECT TRIM(RDB$RELATION_NAME) FROM RDB$RELATIONS WHERE COALESCE(RDB$SYSTEM_FLAG,0)=0 AND RDB$VIEW_BLR IS NULL AND RDB$EXTERNAL_FILE IS NULL ORDER BY 1")
	if err != nil {
		return err
	}
	res.TudoConfere = true
	for _, t := range tabelas {
		q := "SELECT COUNT(*) FROM " + up.AspasFB(t)
		c := up.Comparacao{Tabela: t}
		if err := velho.QueryRowContext(ctx, q).Scan(&c.Antes); err != nil {
			return err
		}
		if err := novo.QueryRowContext(ctx, q).Scan(&c.Depois); err != nil {
			c.Depois = -1
		}
		c.OK = c.Antes == c.Depois
		res.TudoConfere = res.TudoConfere && c.OK
		res.Tabelas = append(res.Tabelas, c)
	}
	geradores, err := nomes(ctx, velho, "SELECT TRIM(RDB$GENERATOR_NAME) FROM RDB$GENERATORS WHERE COALESCE(RDB$SYSTEM_FLAG,0)=0 ORDER BY 1")
	if err != nil {
		return err
	}
	for _, g := range geradores {
		q := "SELECT GEN_ID(" + up.AspasFB(g) + ", 0) FROM RDB$DATABASE"
		c := up.Comparacao{Tabela: g}
		if err := velho.QueryRowContext(ctx, q).Scan(&c.Antes); err != nil {
			return err
		}
		if err := novo.QueryRowContext(ctx, q).Scan(&c.Depois); err != nil {
			c.Depois = -1
		}
		c.OK = c.Antes == c.Depois
		res.TudoConfere = res.TudoConfere && c.OK
		res.Geradores = append(res.Geradores, c)
	}
	for _, m := range []struct{ nome, q string }{
		{"tabelas e views", "SELECT COUNT(*) FROM RDB$RELATIONS WHERE COALESCE(RDB$SYSTEM_FLAG,0)=0"},
		{"procedures", "SELECT COUNT(*) FROM RDB$PROCEDURES WHERE COALESCE(RDB$SYSTEM_FLAG,0)=0"},
		{"triggers", "SELECT COUNT(*) FROM RDB$TRIGGERS WHERE COALESCE(RDB$SYSTEM_FLAG,0)=0"},
		{"índices", "SELECT COUNT(*) FROM RDB$INDICES WHERE COALESCE(RDB$SYSTEM_FLAG,0)=0"},
		{"geradores", "SELECT COUNT(*) FROM RDB$GENERATORS WHERE COALESCE(RDB$SYSTEM_FLAG,0)=0"},
	} {
		c := up.Comparacao{Tabela: m.nome}
		_ = velho.QueryRowContext(ctx, m.q).Scan(&c.Antes)
		_ = novo.QueryRowContext(ctx, m.q).Scan(&c.Depois)
		c.OK = c.Antes == c.Depois
		if !c.OK {
			// o FB5 cria índices de sistema/objetos próprios em alguns casos: avisa, não reprova
			res.Avisos = append(res.Avisos, fmt.Sprintf("%s: %d no original, %d no novo", m.nome, c.Antes, c.Depois))
		}
		res.Metadados = append(res.Metadados, c)
	}
	return nil
}

func nomes(ctx context.Context, db *sql.DB, q string) ([]string, error) {
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, strings.TrimSpace(n))
	}
	return out, rows.Err()
}

// ResumoDescarte é o resumo de firebird.descartar.
type ResumoDescarte struct {
	Banco    string `json:"banco"`
	Desfeito string `json:"desfeito"`
	Passo    string `json:"proximo_passo"`
}

// Descartar tira o banco NOVO do ar (shutdown completo: ninguém mais conecta nele
// por engano). O driver não apaga arquivo de banco; o resumo diz qual apagar.
func Descartar(ctx context.Context, bruto []byte, r Relator) (any, error) {
	esp, err := lerEspecificacao(bruto)
	if err != nil {
		return nil, err
	}
	if esp.Destino == nil {
		return nil, fmt.Errorf("sem banco novo para descartar")
	}
	s, err := senha(r, "destino")
	if err != nil {
		return nil, err
	}
	mm, err := fb.NewMaintenanceManager(endereco(*esp.Destino), esp.Destino.Usuario, s, opcoesServico(*esp.Destino))
	if err != nil {
		return nil, err
	}
	if err := mm.ShutdownEx(esp.Destino.Banco, fb.OperationModeFull, fb.ShutdownModeExForce, 0); err != nil {
		return nil, fmt.Errorf("tirando o banco novo do ar: %w", err)
	}
	r.Evento("descartar", agentev1.EventoTarefa_INFO, "banco novo em shutdown completo; o original segue em uso", 100, nil)
	return ResumoDescarte{Banco: esp.Destino.Banco, Desfeito: "o banco novo está em shutdown completo (nenhuma conexão aceita)",
		Passo: "apague o arquivo " + esp.Destino.Banco + " no servidor Firebird 5 quando quiser"}, nil
}

// Tarefas são os tipos do upgrade (o main liga no executor do canal).
func Tarefas() map[string]copia.Manipulador {
	return map[string]copia.Manipulador{
		up.TarefaDiagnostico: Diagnosticar,
		up.TarefaUpgrade:     Upgrade,
		up.TarefaDescartar:   Descartar,
	}
}
