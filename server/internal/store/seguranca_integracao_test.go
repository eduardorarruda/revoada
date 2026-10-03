package store

import (
	"context"
	"os"
	"testing"
	"time"
)

// Testes de integração com um Postgres de verdade. Rodam só com
// REVOADA_TEST_PG=<dsn> (ex.: um postgres:16 descartável em Docker); sem a variável,
// são pulados — o CI unitário não precisa de banco.
func abrirTeste(t *testing.T) *Store {
	t.Helper()
	dsn := os.Getenv("REVOADA_TEST_PG")
	if dsn == "" {
		t.Skip("REVOADA_TEST_PG não definido")
	}
	ctx := context.Background()
	s, err := Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("conectar: %v", err)
	}
	t.Cleanup(s.Close)
	return s
}

func novoUsuario(t *testing.T, s *Store, role string) int64 {
	t.Helper()
	id, err := s.CreateUser(context.Background(), "u-"+time.Now().Format("150405.000000000"), "hash", role)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestMigracoesSobemDescemESobemDeNovo(t *testing.T) {
	s := abrirTeste(t)
	ctx := context.Background()
	// o Connect já migrou: rodar de novo não aplica nada
	if feitas, err := s.Migrar(ctx); err != nil || len(feitas) != 0 {
		t.Fatalf("segunda execução: %v %v", feitas, err)
	}
	// rollback até a base e subida de novo (as .down.sql precisam funcionar de verdade)
	desfeitas, err := s.Desfazer(ctx, 1)
	if err != nil || len(desfeitas) < 3 {
		t.Fatalf("desfazer: %v %v", desfeitas, err)
	}
	if _, err := s.Desfazer(ctx, 0); err == nil {
		t.Fatal("abaixo da base não pode")
	}
	if feitas, err := s.Migrar(ctx); err != nil || len(feitas) != len(desfeitas) {
		t.Fatalf("subir de novo: %v %v", feitas, err)
	}
	est, err := s.EstadoMigracoes(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range est {
		if !e.Aplicada {
			t.Fatalf("migration %d pendente depois de migrar", e.Versao)
		}
	}
}

func TestMFANoBanco(t *testing.T) {
	s := abrirTeste(t)
	ctx := context.Background()
	id := novoUsuario(t, s, "operador")

	if err := s.AtivarMFA(ctx, id, 1, nil); err != ErrConflito {
		t.Fatalf("ativar sem segredo deveria dar conflito, veio %v", err)
	}
	if err := s.GuardarSegredoMFAPendente(ctx, id, []byte(`{"cifrado":"eA=="}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.AtivarMFA(ctx, id, 100, []string{"h1", "h2"}); err != nil {
		t.Fatal(err)
	}
	if err := s.GuardarSegredoMFAPendente(ctx, id, []byte(`{}`)); err != ErrConflito {
		t.Fatalf("trocar segredo com 2FA ativo deveria dar conflito, veio %v", err)
	}
	e, err := s.MFA(ctx, id)
	if err != nil || !e.Ativo || e.UltimoPasso != 100 || e.Recuperacao != 2 {
		t.Fatalf("estado %+v err %v", e, err)
	}

	// replay: o mesmo passo não avança duas vezes
	if ok, _ := s.AvancarPassoMFA(ctx, id, 101); !ok {
		t.Fatal("passo novo deveria avançar")
	}
	if ok, _ := s.AvancarPassoMFA(ctx, id, 101); ok {
		t.Fatal("mesmo passo não pode valer duas vezes")
	}

	// código de recuperação vale uma vez
	if ok, _ := s.ConsumirCodigoRecuperacao(ctx, id, "h1"); !ok {
		t.Fatal("h1 deveria valer")
	}
	if ok, _ := s.ConsumirCodigoRecuperacao(ctx, id, "h1"); ok {
		t.Fatal("h1 não pode valer duas vezes")
	}

	u, _ := s.UserByID(ctx, id)
	if !u.MFAAtivo {
		t.Fatal("UserByID deveria trazer mfa_ativo")
	}
	if err := s.DesativarMFA(ctx, id); err != nil {
		t.Fatal(err)
	}
	if e, _ := s.MFA(ctx, id); e.Ativo || len(e.SegredoCifrado) != 0 || e.Recuperacao != 0 {
		t.Fatalf("desativar não limpou: %+v", e)
	}
}

func TestBloqueioProgressivoDoLogin(t *testing.T) {
	s := abrirTeste(t)
	ctx := context.Background()
	id := novoUsuario(t, s, "leitor")
	var ate *time.Time
	for i := 1; i <= LimiteFalhasLogin; i++ {
		var err error
		ate, err = s.RegistrarFalhaLogin(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if i < LimiteFalhasLogin && ate != nil {
			t.Fatalf("bloqueou cedo demais, na falha %d", i)
		}
	}
	if ate == nil || time.Until(*ate) < 50*time.Second || time.Until(*ate) > 70*time.Second {
		t.Fatalf("na 5ª falha deveria bloquear ~1 min, veio %v", ate)
	}
	ate2, _ := s.RegistrarFalhaLogin(ctx, id)
	if time.Until(*ate2) < 110*time.Second {
		t.Fatalf("na 6ª falha deveria dobrar para ~2 min, veio %v", time.Until(*ate2))
	}
	if err := s.ZerarFalhasLogin(ctx, id); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UserByID(ctx, id); u.BloqueadoAte != nil {
		t.Fatal("zerar deveria liberar")
	}
}

func TestSessaoLembraMFA(t *testing.T) {
	s := abrirTeste(t)
	ctx := context.Background()
	id := novoUsuario(t, s, "admin")
	if err := s.CreateSession(ctx, id, "hash-sessao-"+time.Now().String(), time.Now().Add(time.Hour), true, time.Now()); err != nil {
		t.Fatal(err)
	}
}

func TestAuditoriaEncadeadaDetectaAdulteracao(t *testing.T) {
	s := abrirTeste(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		if err := s.InsertAudit(ctx, AuditEntry{ActorName: "teste", Method: "POST", Path: "/api/x", Resource: "x",
			Status: 200, Payload: map[string]any{"b": 2, "a": i}, IP: "203.0.113.1", Origem: "mcp"}); err != nil {
			t.Fatal(err)
		}
	}
	r, err := s.VerificarIntegridadeAuditoria(ctx)
	if err != nil || !r.Integra {
		t.Fatalf("corrente recém-gravada deveria estar íntegra: %+v err %v", r, err)
	}

	// alguém edita uma linha direto no banco
	var alvo int64
	if err := s.pool.QueryRow(ctx, `SELECT max(id) - 1 FROM audit_log`).Scan(&alvo); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE audit_log SET actor_name='outro' WHERE id=$1`, alvo); err != nil {
		t.Fatal(err)
	}
	r, _ = s.VerificarIntegridadeAuditoria(ctx)
	if r.Integra || r.PrimeiraRuim == nil || *r.PrimeiraRuim != alvo {
		t.Fatalf("edição não detectada: %+v", r)
	}
	// desfaz a edição e apaga a linha do meio
	if _, err := s.pool.Exec(ctx, `UPDATE audit_log SET actor_name='teste' WHERE id=$1`, alvo); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM audit_log WHERE id=$1`, alvo); err != nil {
		t.Fatal(err)
	}
	r, _ = s.VerificarIntegridadeAuditoria(ctx)
	if r.Integra || r.PrimeiraRuim == nil || *r.PrimeiraRuim != alvo+1 {
		t.Fatalf("remoção não detectada: %+v", r)
	}
	// deixa o banco de teste limpo para as próximas execuções
	if _, err := s.pool.Exec(ctx, `DELETE FROM audit_log`); err != nil {
		t.Fatal(err)
	}
}
