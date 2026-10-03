// Package config lê a configuração do server por env vars (prefixo REVOADA_).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func Env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// HTTPAddr é a porta HTTP do server (default :8091, o gateway usa :8090).
func HTTPAddr() string { return Env("REVOADA_SERVER_ADDR", ":8091") }

// Postgres DSN (metadados: usuários, sessões).
func Postgres() string {
	return Env("REVOADA_PG_DSN", "postgres://revoada:revoada@127.0.0.1:5433/revoada?sslmode=disable")
}

// ClickHouse (leitura de telemetria).
func ClickHouse() (addr, user, pass, db string) {
	return Env("REVOADA_CH_ADDR", "http://127.0.0.1:8123"),
		Env("REVOADA_CH_USER", "revoada"),
		Env("REVOADA_CH_PASSWORD", "revoada"),
		Env("REVOADA_CH_DB", "revoada")
}

// JWTSecret assina os tokens de acesso. EM PRODUÇÃO defina REVOADA_JWT_SECRET.
func JWTSecret() string {
	return Env("REVOADA_JWT_SECRET", "dev-secret-troque-em-producao")
}

// PublicURL é a URL base do frontend, usada em links das notificações.
func PublicURL() string { return Env("REVOADA_PUBLIC_URL", "http://localhost:5173") }

// HostDashboardUID, se definido, gera deep links por host nas notificações.
func HostDashboardUID() string { return Env("REVOADA_HOST_DASHBOARD_UID", "") }

// DeployToken autentica o endpoint de anotação de deploy (service account do CI).
// Vazio por padrão → o endpoint /api/events/deploy fica DESATIVADO (seguro por
// padrão). Defina REVOADA_DEPLOY_TOKEN para habilitá-lo.
func DeployToken() string { return Env("REVOADA_DEPLOY_TOKEN", "") }

// SecureCookies marca os cookies de sessão como Secure (só trafegam sob HTTPS).
// Ative em produção atrás de TLS com REVOADA_SECURE_COOKIES=1. Default desligado para
// não quebrar deploys ainda em HTTP puro.
func SecureCookies() bool { return Env("REVOADA_SECURE_COOKIES", "") == "1" }

const defaultJWTSecret = "dev-secret-troque-em-producao"

// devEnvs são os ambientes (REVOADA_ENV) onde o segredo JWT default (inseguro) é
// tolerado, para não quebrar o fluxo de desenvolvimento local.
var devEnvs = map[string]bool{"dev": true, "development": true, "local": true, "test": true}

// IsDevEnv reporta se REVOADA_ENV indica um ambiente de desenvolvimento.
func IsDevEnv() bool {
	return devEnvs[strings.ToLower(strings.TrimSpace(os.Getenv("REVOADA_ENV")))]
}

// ValidateSecrets faz fail-fast dos segredos críticos: se REVOADA_JWT_SECRET estiver
// vazio ou no valor default inseguro, o boot só é permitido em ambiente de
// desenvolvimento (REVOADA_ENV ∈ {dev,development,local,test}). Em qualquer outro
// caso — inclusive REVOADA_ENV vazio ou "production" — devolve erro para abortar a
// inicialização, evitando que o server suba assinando tokens com segredo público.
func ValidateSecrets() error {
	s := JWTSecret()
	if (s == "" || s == defaultJWTSecret) && !IsDevEnv() {
		return fmt.Errorf("REVOADA_JWT_SECRET não definido ou no valor default INSEGURO: " +
			"defina um segredo forte (>=32 bytes) via REVOADA_JWT_SECRET, " +
			"ou rode em desenvolvimento com REVOADA_ENV=development (dev|local|test)")
	}
	return nil
}

// InsecureDefaults devolve avisos sobre segredos ainda no valor default (inseguro).
// O main loga isso de forma proeminente no boot para não passar despercebido em prod.
// Em produção o boot já é abortado por ValidateSecrets; isto cobre o aviso em dev.
func InsecureDefaults() []string {
	var w []string
	if JWTSecret() == defaultJWTSecret {
		w = append(w, "REVOADA_JWT_SECRET está no default INSEGURO, defina um segredo forte (>=32 bytes) ou qualquer um poderá forjar tokens de admin")
	}
	return w
}

// RetentionDays é o TTL (em dias) das tabelas de série que crescem sem limite,
// via REVOADA_RETENTION_DAYS (default 120). valid=false quando a env estava presente
// mas inválida (não-numérica ou <=0): nesse caso devolve o default e o caller loga
// o aviso (config não tem logger).
func RetentionDays() (days int, valid bool) {
	const def = 120
	v := strings.TrimSpace(os.Getenv("REVOADA_RETENTION_DAYS"))
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def, false
	}
	return n, true
}

// AuditRetentionDays é o TTL (em dias) da trilha de auditoria, via
// REVOADA_AUDIT_RETENTION_DAYS (default 365). É bem mais longo que o das séries:
// a trilha responde "quem mexeu nisso?" meses depois. Mesma convenção de valid.
func AuditRetentionDays() (days int, valid bool) {
	const def = 365
	v := strings.TrimSpace(os.Getenv("REVOADA_AUDIT_RETENTION_DAYS"))
	if v == "" {
		return def, true
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def, false
	}
	return n, true
}

// AgentInstallURL é a URL do instalador de 1 linha do agente (install.sh) usada
// no provisionamento SSH (Fase G). O comando remoto vira
// `curl -fsSL {AgentInstallURL} | sudo sh -s -- --key ... --gateway {GatewayPublicURL}`.
// Default aponta para o próprio painel (PublicURL + /install.sh), mas em produção
// convém setar REVOADA_AGENT_INSTALL_URL explicitamente (host público do painel).
func AgentInstallURL() string {
	return Env("REVOADA_AGENT_INSTALL_URL", strings.TrimRight(PublicURL(), "/")+"/install.sh")
}

// GatewayPublicURL é a URL pública do gateway de ingestão (para onde o agente
// remoto envia telemetria). Usada no comando de instalação do provisionamento SSH.
// Default :8090 no mesmo host do painel (ajuste REVOADA_GATEWAY_PUBLIC_URL em prod).
func GatewayPublicURL() string {
	return Env("REVOADA_GATEWAY_PUBLIC_URL", "http://127.0.0.1:8090")
}

// AgentDistDir é o diretório onde o deploy publica os artefatos do agente
// (install.sh, install-macos.sh, revoada-agent, revoada-agent.exe) — o
// MESMO que o nginx serve. O painel lê dali para montar o instalador com a chave
// embutida, de modo que o arquivo baixado nunca fique defasado do que está no ar.
// Em produção o diretório é montado somente-leitura no container do server; no
// dev, `dist/` do repositório é o que o `make agent-linux` preenche. Vazio
// desliga a geração (o painel explica em vez de falhar sem contexto).
func AgentDistDir() string {
	return Env("REVOADA_AGENT_DIST_DIR", "dist")
}

// DataDir é o diretório de dados do painel (REVOADA_DATA_DIR). Padrão: o diretório
// de configuração do usuário no SO (Linux: ~/.config/revoada; Windows: %AppData%\revoada;
// macOS: ~/Library/Application Support/revoada). Em container, aponte para um volume.
func DataDir() string {
	if v := strings.TrimSpace(os.Getenv("REVOADA_DATA_DIR")); v != "" {
		return v
	}
	base, err := os.UserConfigDir()
	if err != nil {
		return "dados"
	}
	return filepath.Join(base, "revoada")
}

// ChaveMestraArquivo é onde fica a chave mestra do cofre (REVOADA_CHAVE_MESTRA).
// FORA do banco por definição: quem rouba o banco não leva a chave. Faça backup
// separado — perder este arquivo torna ilegíveis os segredos guardados.
func ChaveMestraArquivo() string {
	return Env("REVOADA_CHAVE_MESTRA", filepath.Join(DataDir(), "chave-mestra.json"))
}

// VaultKeyEnv é o valor bruto de REVOADA_VAULT_KEY (hex ou base64 de 32 bytes).
// Vazio => o cofre deriva a chave de REVOADA_JWT_SECRET (ver internal/provision).
// EM PRODUÇÃO defina REVOADA_VAULT_KEY dedicada para o cofre de credenciais SSH.
func VaultKeyEnv() string { return os.Getenv("REVOADA_VAULT_KEY") }

// TrustedProxies são as redes de onde um X-Forwarded-For pode ser acreditado
// (REVOADA_TRUSTED_PROXIES, lista separada por vírgula). MESMO nome de env e MESMO
// default do gateway — os dois processos ficam atrás do mesmo Traefik/nginx, e ter
// duas noções diferentes de "proxy confiável" foi exatamente o buraco medido: o
// gateway exigia peer confiável antes de olhar o XFF e o server não.
//
// Default: as faixas privadas + loopback, que é onde o Traefik e o nginx do compose
// realmente vivem. Um cliente da internet nunca chega ao server com peer privado.
func TrustedProxies() []string {
	return strings.Split(Env("REVOADA_TRUSTED_PROXIES", "10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,127.0.0.1/32,::1/128"), ",")
}

// PGMaxConns é o tamanho máximo do pool pgx (REVOADA_PG_MAX_CONNS, default 20).
// Valores inválidos ou <=0 caem no default.
func PGMaxConns() int32 {
	const def = 20
	v := strings.TrimSpace(os.Getenv("REVOADA_PG_MAX_CONNS"))
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	return int32(n)
}

// CanalAddr é onde o painel escuta os agentes (gRPC sobre mTLS). Porta própria por
// padrão (ARQUITETURA §21.2); compartilhar a 443 via ALPN fica para depois.
func CanalAddr() string { return Env("REVOADA_CANAL_ADDR", ":7443") }

// CanalHosts são os nomes/IPs que entram no certificado do canal — por onde os
// agentes chegam ao painel (REVOADA_CANAL_HOSTS, separados por vírgula).
func CanalHosts() []string {
	hosts := []string{}
	for _, h := range strings.Split(Env("REVOADA_CANAL_HOSTS", "localhost,127.0.0.1"), ",") {
		if h = strings.TrimSpace(h); h != "" {
			hosts = append(hosts, h)
		}
	}
	if nome, err := os.Hostname(); err == nil && nome != "" {
		hosts = append(hosts, nome)
	}
	return hosts
}

// CanalEnderecoPublico é o host:porta que vai no comando de instalação do agente.
func CanalEnderecoPublico() string {
	if v := strings.TrimSpace(os.Getenv("REVOADA_CANAL_PUBLICO")); v != "" {
		return v
	}
	_, porta, _ := strings.Cut(CanalAddr(), ":")
	return CanalHosts()[0] + ":" + porta
}

func horas(chave string, padrao int) time.Duration {
	n, err := strconv.Atoi(strings.TrimSpace(os.Getenv(chave)))
	if err != nil || n <= 0 {
		n = padrao
	}
	return time.Duration(n) * time.Hour
}

// SessaoInatividade: sem uso por este tempo, a sessão expira (REVOADA_SESSAO_INATIVIDADE_HORAS, padrão 24).
func SessaoInatividade() time.Duration { return horas("REVOADA_SESSAO_INATIVIDADE_HORAS", 24) }

// SessaoMaxima: limite absoluto desde o login (REVOADA_SESSAO_MAX_HORAS, padrão 168 = 7 dias).
func SessaoMaxima() time.Duration { return horas("REVOADA_SESSAO_MAX_HORAS", 168) }

// CORSOrigens: outras origens autorizadas a chamar a API (REVOADA_CORS_ORIGENS, vírgulas).
func CORSOrigens() []string {
	var out []string
	for _, o := range strings.Split(os.Getenv("REVOADA_CORS_ORIGENS"), ",") {
		if o = strings.TrimSpace(o); o != "" {
			out = append(out, o)
		}
	}
	return out
}

// TLS do painel (item 1, HTTPS): certificado próprio (REVOADA_TLS_CERT/REVOADA_TLS_CHAVE)
// ou Let's Encrypt automático para um domínio (REVOADA_TLS_DOMINIO). Sem nada disso,
// o painel espera um proxy com HTTPS na frente (REVOADA_HTTPS_NO_PROXY=1).
func TLSArquivos() (cert, chave string) {
	return os.Getenv("REVOADA_TLS_CERT"), os.Getenv("REVOADA_TLS_CHAVE")
}
func TLSDominio() string    { return strings.TrimSpace(os.Getenv("REVOADA_TLS_DOMINIO")) }
func HTTPSNoProxy() bool    { return os.Getenv("REVOADA_HTTPS_NO_PROXY") == "1" || SecureCookies() }
func MetricasToken() string { return os.Getenv("REVOADA_METRICAS_TOKEN") }
