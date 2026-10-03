// Package authz resolve e aplica o escopo de servidores de cada usuário (quais hosts
// ele pode ver/editar). É a espinha dorsal do enforcement por usuário: os handlers HTTP
// injetam um *Scope no contexto (via os wrappers do httpapi) e os construtores de SQL do
// ClickHouse e as consultas Postgres restringem os resultados aos hosts permitidos.
//
// Regra central: administrador tem Scope.Admin=true e NÃO é filtrado em lugar nenhum.
// Usuário comum só enxerga os hosts do seu escopo; sem nenhum host, não vê nada (o
// predicado SQL vira `1=0`, nunca vazio). Chamadas de sistema (evaluator de alertas,
// TV pública) não injetam Scope e portanto não são filtradas — comportamento desejado.
package authz

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// Scope é o conjunto de servidores que um usuário pode ver/editar. Imutável após criado.
type Scope struct {
	Admin  bool
	UserID int64
	view   map[string]bool
	edit   map[string]bool
}

// NewAdminScope devolve um escopo irrestrito (admin).
func NewAdminScope(userID int64) *Scope {
	return &Scope{Admin: true, UserID: userID}
}

// NewScope monta um Scope de usuário comum a partir da sua permissão efetiva (lista de
// ServerPerm). Exportado para o Resolver e para testes de enforcement em outros pacotes.
func NewScope(userID int64, perms []store.ServerPerm) *Scope {
	view := make(map[string]bool, len(perms))
	edit := make(map[string]bool)
	for _, p := range perms {
		if p.CanView || p.CanEdit || p.Notify {
			view[p.Hostname] = true
		}
		if p.CanEdit {
			edit[p.Hostname] = true
		}
	}
	return &Scope{UserID: userID, view: view, edit: edit}
}

// CanView informa se o usuário pode ver um hostname. Admin sempre pode.
func (s *Scope) CanView(hostname string) bool {
	return s.Admin || s.view[hostname]
}

// CanEdit informa se o usuário pode editar (mutar) recursos de um hostname.
func (s *Scope) CanEdit(hostname string) bool {
	return s.Admin || s.edit[hostname]
}

// ViewHosts devolve, ordenados, os hostnames que o usuário pode ver (vazio para admin —
// admin não precisa de lista). Útil para consultas Postgres com `= ANY($1)`.
func (s *Scope) ViewHosts() []string {
	if s.Admin {
		return nil
	}
	out := make([]string, 0, len(s.view))
	for h := range s.view {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// EditHosts devolve, ordenados, os hostnames que o usuário pode editar.
func (s *Scope) EditHosts() []string {
	if s.Admin {
		return nil
	}
	out := make([]string, 0, len(s.edit))
	for h := range s.edit {
		out = append(out, h)
	}
	sort.Strings(out)
	return out
}

// HostPredicate devolve um predicado booleano SQL (ClickHouse) que restringe `col` (ex.:
// `labels['host']`) aos hosts que o usuário pode ver. Regras:
//   - admin  → "" (sem restrição; o chamador NÃO adiciona nada ao WHERE)
//   - com N hosts → "col IN ('a','b',...)"
//   - com ZERO hosts → "1=0" (bloqueia tudo — NUNCA devolve "" para não-admin)
//
// Como o IN nunca inclui a string vazia, linhas sem label de host somem para não-admin —
// é assim que a regra "dado sem host = só admin vê" é aplicada automaticamente.
func (s *Scope) HostPredicate(col string) string {
	if s.Admin {
		return ""
	}
	hosts := s.ViewHosts()
	if len(hosts) == 0 {
		return "1=0"
	}
	lits := make([]string, len(hosts))
	for i, h := range hosts {
		lits[i] = chLiteral(h)
	}
	return col + " IN (" + strings.Join(lits, ", ") + ")"
}

// chLiteral escapa uma string para literal do ClickHouse (aspa simples e barra invertida).
func chLiteral(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, "\\", "\\\\"), "'", "\\'") + "'"
}

// --- contexto ---

type ctxKey int

const scopeKey ctxKey = 0

// WithScope injeta o escopo no contexto (feito pelos wrappers do httpapi).
func WithScope(ctx context.Context, s *Scope) context.Context {
	return context.WithValue(ctx, scopeKey, s)
}

// ScopeFrom recupera o escopo do contexto. ok=false quando não há escopo (chamada de
// sistema: evaluator, TV pública) — nesses casos NÃO se filtra.
func ScopeFrom(ctx context.Context) (*Scope, bool) {
	s, ok := ctx.Value(scopeKey).(*Scope)
	return s, ok
}

// --- resolver com cache ---

type cacheEntry struct {
	scope   *Scope
	expires time.Time
}

// Resolver calcula o Scope de um usuário a partir do store, com cache em memória por
// userID (TTL curto). Seguro para uso concorrente.
type Resolver struct {
	st  *store.Store
	ttl time.Duration
	mu  sync.RWMutex
	m   map[int64]cacheEntry
	now func() time.Time
}

// NewResolver cria um Resolver com o TTL dado (use 30s em produção).
func NewResolver(st *store.Store, ttl time.Duration) *Resolver {
	return &Resolver{st: st, ttl: ttl, m: make(map[int64]cacheEntry), now: time.Now}
}

// Resolve devolve o Scope do usuário. Admin curto-circuita (não consulta permissões).
// Usa cache por userID; em erro de banco, propaga (o chamador decide — negar acesso).
func (r *Resolver) Resolve(ctx context.Context, userID int64, role string) (*Scope, error) {
	if role == "admin" {
		return NewAdminScope(userID), nil
	}
	now := r.now()
	r.mu.RLock()
	if e, ok := r.m[userID]; ok && now.Before(e.expires) {
		r.mu.RUnlock()
		return e.scope, nil
	}
	r.mu.RUnlock()

	perms, err := r.st.EffectiveServerPerms(ctx, userID)
	if err != nil {
		return nil, err
	}
	scope := NewScope(userID, perms)
	r.mu.Lock()
	r.m[userID] = cacheEntry{scope: scope, expires: now.Add(r.ttl)}
	r.mu.Unlock()
	return scope, nil
}

// Invalidate descarta o cache de um usuário (userID > 0) ou de TODOS (userID == 0, usado
// quando um grupo muda e afeta vários usuários). Chamado pelo useradmin ao salvar.
func (r *Resolver) Invalidate(userID int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if userID == 0 {
		r.m = make(map[int64]cacheEntry)
		return
	}
	delete(r.m, userID)
}
