package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// EstadoMFA é o que o login e a tela de segurança precisam saber do 2FA de um usuário.
// SegredoCifrado é um cofre.Segredo serializado — nunca o segredo em texto puro.
type EstadoMFA struct {
	Ativo          bool
	SegredoCifrado json.RawMessage
	UltimoPasso    int64
	Recuperacao    int // quantos códigos de recuperação ainda valem
}

// MFA lê o estado do 2FA.
func (s *Store) MFA(ctx context.Context, userID int64) (EstadoMFA, error) {
	var e EstadoMFA
	var seg []byte
	err := s.pool.QueryRow(ctx,
		`SELECT mfa_ativo, mfa_segredo, mfa_ultimo_passo, cardinality(mfa_recuperacao) FROM users WHERE id=$1`, userID,
	).Scan(&e.Ativo, &seg, &e.UltimoPasso, &e.Recuperacao)
	if errors.Is(err, pgx.ErrNoRows) {
		return e, ErrNotFound
	}
	e.SegredoCifrado = seg
	return e, err
}

// GuardarSegredoMFAPendente grava o segredo novo (cifrado) sem ativar o 2FA: só vale
// depois que o usuário provar que o app gera códigos certos (ConfirmarMFA).
// Recusa se o 2FA já estiver ativo — trocar exige desativar antes (com reautenticação).
func (s *Store) GuardarSegredoMFAPendente(ctx context.Context, userID int64, segredoCifrado []byte) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET mfa_segredo=$2, mfa_ultimo_passo=0 WHERE id=$1 AND NOT mfa_ativo`, userID, segredoCifrado)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflito
	}
	return nil
}

// ErrConflito: a operação não se aplica ao estado atual (ex.: 2FA já ativo).
var ErrConflito = errors.New("estado não permite esta operação")

// AtivarMFA liga o 2FA e grava os hashes dos códigos de recuperação.
func (s *Store) AtivarMFA(ctx context.Context, userID int64, passo int64, hashesRecuperacao []string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET mfa_ativo=TRUE, mfa_ultimo_passo=$2, mfa_recuperacao=$3
		  WHERE id=$1 AND NOT mfa_ativo AND mfa_segredo IS NOT NULL`, userID, passo, hashesRecuperacao)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrConflito
	}
	return nil
}

// DesativarMFA apaga segredo e códigos de recuperação.
func (s *Store) DesativarMFA(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE users SET mfa_ativo=FALSE, mfa_segredo=NULL, mfa_ultimo_passo=0, mfa_recuperacao='{}' WHERE id=$1`, userID)
	return err
}

// AvancarPassoMFA registra o passo de tempo do código aceito. A condição no WHERE
// torna isso atômico: dois logins simultâneos com o MESMO código — só um vence.
func (s *Store) AvancarPassoMFA(ctx context.Context, userID, passo int64) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET mfa_ultimo_passo=$2 WHERE id=$1 AND mfa_ultimo_passo < $2`, userID, passo)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// ConsumirCodigoRecuperacao queima um código de recuperação (pelo hash). Devolve
// false se o código não existe ou já foi usado.
func (s *Store) ConsumirCodigoRecuperacao(ctx context.Context, userID int64, hash string) (bool, error) {
	tag, err := s.pool.Exec(ctx,
		`UPDATE users SET mfa_recuperacao = array_remove(mfa_recuperacao, $2)
		  WHERE id=$1 AND $2 = ANY(mfa_recuperacao)`, userID, hash)
	if err != nil {
		return false, err
	}
	return tag.RowsAffected() == 1, nil
}

// LimiteFalhasLogin é quantas senhas erradas seguidas são toleradas antes do bloqueio.
const LimiteFalhasLogin = 5

// RegistrarFalhaLogin soma uma falha e, a partir do limite, bloqueia por tempo
// crescente: 1 min, 2, 4, 8… até 1 h. Devolve até quando ficou bloqueado (nil = livre).
func (s *Store) RegistrarFalhaLogin(ctx context.Context, userID int64) (*time.Time, error) {
	var ate *time.Time
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		UPDATE users SET
		    falhas_login = falhas_login + 1,
		    bloqueado_ate = CASE WHEN falhas_login + 1 >= %d
		        THEN now() + make_interval(secs => LEAST(3600, 60 * power(2, falhas_login + 1 - %d)))
		        ELSE bloqueado_ate END
		 WHERE id=$1
		 RETURNING bloqueado_ate`, LimiteFalhasLogin, LimiteFalhasLogin), userID).Scan(&ate)
	return ate, err
}

// ZerarFalhasLogin limpa o contador depois de um login certo.
func (s *Store) ZerarFalhasLogin(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET falhas_login=0, bloqueado_ate=NULL WHERE id=$1 AND falhas_login > 0`, userID)
	return err
}
