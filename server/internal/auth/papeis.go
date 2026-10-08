package auth

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// Papéis do painel (ARQUITETURA §14). A checagem acontece SEMPRE no back-end, em cada rota
// sensível — o front só esconde botões por conveniência.
const (
	PapelAdmin    = "admin"    // Administrador
	PapelOperador = "operador" // roda migrações e deploys
	PapelLeitor   = "leitor"   // só visualiza
)

// NormalizarPapel converte qualquer valor (inclusive legados) num papel válido.
// Desconhecido vira leitor: na dúvida, o menor privilégio.
func NormalizarPapel(p string) string {
	switch strings.ToLower(strings.TrimSpace(p)) {
	case PapelAdmin, "administrador":
		return PapelAdmin
	case PapelOperador, "editor":
		return PapelOperador
	default: // "leitor", "viewer", "user" (legado), vazio, lixo
		return PapelLeitor
	}
}

// NomePapel é o rótulo em português para a interface.
func NomePapel(p string) string {
	switch NormalizarPapel(p) {
	case PapelAdmin:
		return "Administrador"
	case PapelOperador:
		return "Operador"
	default:
		return "Leitor"
	}
}

// Permissao é uma ação que exige papel.
type Permissao string

const (
	PermVer               Permissao = "ver"                // painéis, execuções, relatórios
	PermEditarMapeamento  Permissao = "editar_mapeamento"  // criar/editar rascunhos
	PermExecutarMigracao  Permissao = "executar_migracao"  // aprovar, simular, executar, pausar, reverter
	PermRodarDeploy       Permissao = "rodar_deploy"       // disparar deploy e rollback
	PermGerenciarConexoes Permissao = "gerenciar_conexoes" // cadastrar conexões e credenciais
	PermGerenciarAgentes  Permissao = "gerenciar_agentes"  // inscrever/revogar agentes
	PermGerenciarUsuarios Permissao = "gerenciar_usuarios" // usuários, papéis, tokens, MCP
	PermVerCredencial     Permissao = "ver_credencial"     // nunca devolve a senha; só troca/teste
	// PermVerConteudoIA lê prompt e resposta gravados das chamadas de IA. Leitor vê
	// custo, tokens e erro, mas não o conteúdo: prompt costuma trazer dado pessoal.
	PermVerConteudoIA Permissao = "ver_conteudo_ia"
)

// matriz é a tabela da ARQUITETURA §14: quem pode o quê.
var matriz = map[Permissao]map[string]bool{
	PermVer:               {PapelAdmin: true, PapelOperador: true, PapelLeitor: true},
	PermEditarMapeamento:  {PapelAdmin: true, PapelOperador: true},
	PermExecutarMigracao:  {PapelAdmin: true, PapelOperador: true},
	PermRodarDeploy:       {PapelAdmin: true, PapelOperador: true},
	PermGerenciarConexoes: {PapelAdmin: true},
	PermGerenciarAgentes:  {PapelAdmin: true},
	PermGerenciarUsuarios: {PapelAdmin: true},
	PermVerCredencial:     {PapelAdmin: true},
	PermVerConteudoIA:     {PapelAdmin: true, PapelOperador: true},
}

// criticas exigem reautenticação recente (senha/2FA de novo nos últimos minutos).
var criticas = map[Permissao]bool{
	PermExecutarMigracao:  true,
	PermRodarDeploy:       true,
	PermGerenciarConexoes: true,
	PermGerenciarUsuarios: true,
	PermVerCredencial:     true,
	// Prompt de cliente é dado pessoal: ler o conteúdo das conversas pede a mesma
	// confirmação de identidade que ver credencial.
	PermVerConteudoIA: true,
}

// Pode diz se o papel tem a permissão.
func Pode(papel string, p Permissao) bool { return matriz[p][NormalizarPapel(papel)] }

// MFAObrigatorio: administrador e operador precisam de 2FA (ARQUITETURA §21.1).
func MFAObrigatorio(papel string) bool {
	n := NormalizarPapel(papel)
	return n == PapelAdmin || n == PapelOperador
}

// Códigos de erro em JSON que a interface entende e trata sem adivinhar.
const (
	CodigoSemPermissao   = "sem_permissao"
	CodigoMFANecessario  = "mfa_necessario"
	CodigoReautenticacao = "reautenticacao_necessaria"
)

// ErroAcesso é o corpo das respostas 403 de autorização.
type ErroAcesso struct {
	Codigo   string `json:"codigo"`
	Mensagem string `json:"mensagem"`
}

func negar(w http.ResponseWriter, codigo, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(w).Encode(ErroAcesso{Codigo: codigo, Mensagem: msg})
}

// Exige protege uma rota com uma permissão. Deve vir depois do RequireAuth (as
// claims precisam estar no contexto). Para permissões críticas também confere 2FA
// (quando obrigatório para o papel) e reautenticação recente.
func Exige(p Permissao, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, ok := ClaimsFrom(r.Context())
		if !ok {
			negar(w, CodigoSemPermissao, "faça login para continuar")
			return
		}
		if motivo, codigo := avaliar(c, p, time.Now()); codigo != "" {
			negar(w, codigo, motivo)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// avaliar decide o acesso; separado para ser testável sem HTTP.
func avaliar(c Claims, p Permissao, agora time.Time) (mensagem, codigo string) {
	if !Pode(c.Role, p) {
		return "seu papel (" + NomePapel(c.Role) + ") não permite esta ação", CodigoSemPermissao
	}
	if !criticas[p] {
		return "", ""
	}
	if MFAObrigatorio(c.Role) && !c.MFA {
		return "ative a verificação em duas etapas (2FA) para fazer esta ação", CodigoMFANecessario
	}
	if c.Reauth < agora.Unix() {
		return "confirme sua identidade para continuar", CodigoReautenticacao
	}
	return "", ""
}
