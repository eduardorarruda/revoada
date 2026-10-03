package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// ORDENS DE AUTO-DESINSTALAÇÃO — ver o comentário de agent_uninstalls no schema.
//
// O ciclo de vida de uma ordem tem quatro marcos, e cada um responde a uma pergunta
// diferente do operador:
//
//	ordered_at  — "eu mandei?"            (a ordem existe)
//	sent_at     — "o agente ouviu?"       (o painel entregou na consulta dele)
//	reported_at — "ele conseguiu?"        (o agente respondeu o que aconteceu)
//	result      — "deu o quê?"            ('ok' ou o motivo da falha)
//
// Sem os quatro, a tela só saberia dizer "mandei" — e "mandei" para uma máquina
// desligada é indistinguível de "removido", que é exatamente a confusão que a
// exclusão de servidor existe para não criar.
type AgentUninstall struct {
	Serverkey  string     `json:"-"` // NUNCA sai em JSON: é credencial
	Hostname   string     `json:"hostname"`
	OrderedAt  time.Time  `json:"ordered_at"`
	OrderedBy  string     `json:"ordered_by,omitempty"`
	SentAt     *time.Time `json:"sent_at,omitempty"`
	ReportedAt *time.Time `json:"reported_at,omitempty"`
	Result     string     `json:"result,omitempty"`
}

// Resultados possíveis de uma ordem. Os dois primeiros são literais que a faxina
// consulta; qualquer outro texto é a MENSAGEM DE FALHA do agente, e é ela que segura
// a ordem viva para o operador ver e o agente tentar de novo.
const (
	// UninstallOK: acabou nesta máquina (macOS e Windows removem na hora).
	UninstallOK = "ok"
	// UninstallIniciada: aceita, termina no restart seguinte (Linux, promotor root).
	// NÃO é "concluída" — ver o comentário de EstadoEmCurso no pacote selfuninstall.
	UninstallIniciada = "iniciada"
)

// Estado devolve o estágio em uma palavra, para a tela não ter que deduzir.
func (u AgentUninstall) Estado() string {
	switch {
	case u.ReportedAt != nil && u.Result == UninstallOK:
		return "concluida"
	case u.ReportedAt != nil && u.Result == UninstallIniciada:
		return "em_curso"
	case u.ReportedAt != nil:
		return "falhou"
	case u.SentAt != nil:
		return "entregue"
	default:
		return "aguardando"
	}
}

// OrderAgentUninstall registra a ordem e REVOGA a chave na mesma transação.
//
// As duas coisas juntas, e nesta ordem, porque separá-las cria os dois defeitos que
// esta feature existe para evitar: revogar sem registrar deixa um agente batendo em
// 401 para sempre sem ninguém ter mandado nada; registrar sem revogar deixa o host
// continuar ingerindo — e reaparecendo no inventário — enquanto a ordem não é
// entregue, que pode ser uma hora ou nunca.
func (s *Store) OrderAgentUninstall(ctx context.Context, serverkey, hostname, quem string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `UPDATE agents SET revoked = TRUE WHERE serverkey = $1`, serverkey); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO agent_uninstalls (serverkey, hostname, ordered_by)
		VALUES ($1,$2,$3)
		ON CONFLICT (serverkey) DO UPDATE SET
			hostname = EXCLUDED.hostname, ordered_by = EXCLUDED.ordered_by,
			ordered_at = now(), sent_at = NULL, reported_at = NULL, result = ''`,
		serverkey, hostname, quem); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// PendingAgentUninstall devolve a ordem pendente daquela chave, ou nil. É consultada
// no caminho quente da consulta do agente, então é uma leitura por chave primária.
func (s *Store) PendingAgentUninstall(ctx context.Context, serverkey string) (*AgentUninstall, error) {
	var u AgentUninstall
	err := s.pool.QueryRow(ctx, `
		SELECT serverkey, hostname, ordered_at, ordered_by, sent_at, reported_at, result
		FROM agent_uninstalls WHERE serverkey = $1`, serverkey).
		Scan(&u.Serverkey, &u.Hostname, &u.OrderedAt, &u.OrderedBy, &u.SentAt, &u.ReportedAt, &u.Result)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &u, nil
}

// MarkAgentUninstallSent carimba a entrega da ordem. Idempotente: só grava a PRIMEIRA
// entrega, porque é ela que responde "a partir de quando o agente sabia".
func (s *Store) MarkAgentUninstallSent(ctx context.Context, serverkey string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE agent_uninstalls SET sent_at = COALESCE(sent_at, now()) WHERE serverkey = $1`, serverkey)
	return err
}

// MarkAgentUninstallReported grava o que o agente relatou.
func (s *Store) MarkAgentUninstallReported(ctx context.Context, serverkey, result string) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE agent_uninstalls SET reported_at = now(), result = $2 WHERE serverkey = $1`, serverkey, result)
	return err
}

// NÃO existe listagem de ordens abertas aqui de propósito. Havia uma, escrita para
// uma tela que não foi feita: nenhuma rota a chamava, nenhum teste a exercitava, e
// ela devolvia a serverkey em claro para quem quer que a chamasse um dia. Enquanto a
// tela não existir, o estado de uma ordem se lê no log do servidor e na trilha (a
// exclusão que a criou fica registrada com quem pediu). Ver Estado() acima: ele já é
// o vocabulário pronto para quando a tela for feita.

// SweepFinishedAgentUninstalls apaga de vez as chaves cuja desinstalação já cumpriu o
// seu papel. Duas condições, e as duas precisam existir:
//
//   - CONFIRMADA e em silêncio há mais que `graca`: o agente disse que executou. A
//     carência não é cerimônia — no Linux a remoção acontece no restart seguinte, pelo
//     promotor root, e o agente confirma ANTES de morrer. Apagar a chave no mesmo
//     instante tiraria o chão de uma desinstalação que ainda está em curso;
//   - ABANDONADA há mais que `desistir`: a ordem nunca foi entregue (máquina desligada,
//     descomissionada, sem rede). Guardar uma chave viva para sempre à espera de um
//     agente que não volta é pior do que assumir a perda: a chave é credencial.
//
// Devolve as chaves apagadas para o chamador registrar.
// As duas remoções — a ordem e a chave — vão na MESMA transação. Fora dela, um erro
// no `DELETE FROM agents` deixaria a credencial viva para sempre: a ordem que a
// traria de volta para a próxima faxina já teria sido apagada pelo RETURNING, e o
// único vestígio seria uma linha de log. Credencial órfã não se recupera sozinha.
func (s *Store) SweepFinishedAgentUninstalls(ctx context.Context, graca, desistir time.Duration) ([]AgentUninstall, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// `result IN (ok, iniciada)` é a diferença entre encerrar e ESCONDER. Sem o
	// filtro, uma ordem que FALHOU (o caso mais provável hoje: promotor root anterior
	// a esta feature) era apagada 15 min depois junto com a chave — e com ela sumia a
	// mensagem que dizia o que fazer, o canal pelo qual o agente tentaria de novo na
	// hora seguinte, e qualquer vestígio de que aquele host ficou com um agente
	// órfão. A falha agora fica até o prazo de desistência: enquanto ela existe, o
	// agente recebe a ordem de novo a cada consulta, então reinstalar o agente resolve
	// sozinho.
	rows, err := tx.Query(ctx, `
		DELETE FROM agent_uninstalls
		WHERE (reported_at IS NOT NULL AND result = ANY($3) AND reported_at < now() - $1::interval)
		   OR (ordered_at < now() - $2::interval)
		RETURNING serverkey, hostname, ordered_at, ordered_by, sent_at, reported_at, result`,
		graca.String(), desistir.String(), []string{UninstallOK, UninstallIniciada})
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var fechadas []AgentUninstall
	for rows.Next() {
		var u AgentUninstall
		if err := rows.Scan(&u.Serverkey, &u.Hostname, &u.OrderedAt, &u.OrderedBy,
			&u.SentAt, &u.ReportedAt, &u.Result); err != nil {
			return nil, err
		}
		fechadas = append(fechadas, u)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	// A linha de `agent_uninstalls` saiu acima; agora a chave em si. Um erro aqui
	// desfaz TAMBÉM o DELETE de cima, e a ordem volta para a faxina seguinte.
	for _, u := range fechadas {
		if _, derr := tx.Exec(ctx, `DELETE FROM agents WHERE serverkey = $1`, u.Serverkey); derr != nil {
			return nil, derr
		}
		// A política de atualização daquela chave sai junto. Ela não tem FK para
		// `agents` (tabelas de esquemas diferentes) e, neste fluxo, a limpeza de
		// órfãs do hostadmin não a alcança: quando ela roda, a chave ainda existe de
		// propósito. Sem esta linha, cada auto-desinstalação deixaria uma linha de
		// política do último relato de um agente que não existe mais.
		if _, derr := tx.Exec(ctx, `DELETE FROM agent_update_policy WHERE serverkey = $1`, u.Serverkey); derr != nil {
			return nil, derr
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return fechadas, nil
}
