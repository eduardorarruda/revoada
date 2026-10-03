package provision

import (
	"context"
	"fmt"
	"strings"

	"github.com/eduardorarruda/revoada/server/internal/auth"
)

// Step é uma linha da devolutiva passo a passo do provisionamento. Serializável
// (SSE/JSON). Detail NUNCA contém a credencial nem a serverkey em claro.
type Step struct {
	Step   string `json:"step"`   // ssh | serverkey | install | service
	Status string `json:"status"` // ok | erro
	Detail string `json:"detail"`
}

const (
	StatusOK  = "ok"
	StatusErr = "erro"
)

// KeyStore é o mínimo que o orquestrador precisa do store para gerar a serverkey
// (satisfeito por *store.Store via CreateAgent/DeleteAgent). Abstrair permite
// testar sem banco. DeleteAgent é usado para desfazer a chave recém-criada quando
// a instalação falha depois dela (evita serverkeys órfãs acumulando a cada retry).
type KeyStore interface {
	CreateAgent(ctx context.Context, serverkey, tenant, hostname string) error
	DeleteAgent(ctx context.Context, serverkey string) error
}

// Params reúne os dados de um (re)provisionamento.
type Params struct {
	Name       string // nome amigável do alvo (vira hostname da serverkey)
	Tenant     string // default "default"
	Target     Target
	InstallURL string // ex.: https://painel/install.sh
	GatewayURL string // ex.: https://gateway
	// AgentFlags são flags extras passadas ao install.sh (limites de recurso da unit
	// systemd + soft caps). Já vêm aspadas/prontas; string vazia => usa os defaults
	// embutidos no install.sh. Ver AgentResourceFlags.
	AgentFlags string
	// Serverkey vazia => gera uma nova (novo alvo). Preenchida => reaproveita a
	// existente (atualização/reprovisionamento; install.sh é idempotente).
	Serverkey string
}

// Result é o desfecho de um provisionamento bem-sucedido, para persistência.
type Result struct {
	HostKeyFP string // fingerprint pinado do host key visto
	Serverkey string // chave gerada ou reaproveitada
	Hostname  string // hostname REAL da máquina (via `hostname` por SSH); elo com `hosts`
}

// Provision executa os 4 passos e emite progresso via emit. Interrompe no primeiro
// erro, emitindo o Step de erro (redigido) e devolvendo o erro. Segredos jamais
// aparecem em Step.Detail nem no erro retornado.
func Provision(ctx context.Context, ks KeyStore, runner Runner, p Params, emit func(Step)) (Result, error) {
	var res Result
	tenant := p.Tenant
	if tenant == "" {
		tenant = "default"
	}

	// Passo 1: SSH.
	sess, err := runner.Connect(ctx, p.Target)
	if err != nil {
		emit(Step{Step: "ssh", Status: StatusErr, Detail: redact(err.Error(), p)})
		return res, err
	}
	defer func() { _ = sess.Close() }()
	fp := sess.HostKey()
	res.HostKeyFP = fp
	emit(Step{Step: "ssh", Status: StatusOK, Detail: "SSH OK (host key " + fp + ")"})

	// Passo 2: serverkey (gera ou reaproveita). A chave NÃO vai para o progresso.
	key := p.Serverkey
	createdKey := false
	if key == "" {
		key, err = auth.RandomToken()
		if err != nil {
			emit(Step{Step: "serverkey", Status: StatusErr, Detail: "falha ao gerar serverkey"})
			return res, err
		}
		// Rótulo cosmético da serverkey (o hostname REAL só é conhecido após o install).
		if err := ks.CreateAgent(ctx, key, tenant, p.Name); err != nil {
			emit(Step{Step: "serverkey", Status: StatusErr, Detail: "falha ao registrar serverkey"})
			return res, err
		}
		createdKey = true
		emit(Step{Step: "serverkey", Status: StatusOK, Detail: "chave gerada"})
	} else {
		emit(Step{Step: "serverkey", Status: StatusOK, Detail: "chave existente reaproveitada"})
	}
	res.Serverkey = key

	// rollbackKey desfaz (best-effort) a serverkey criada NESTA execução quando a
	// INSTALAÇÃO falha — sem isso, cada retry deixaria uma chave órfã na tabela
	// agents. Só é seguro antes de a chave chegar ao host: se o install já gravou o
	// agent.yaml (passo 4 em diante), deletar a chave silenciaria um host que pode
	// subir sozinho depois (Restart=always). Contexto sem cancelamento: o cliente
	// pode ter fechado a aba.
	rollbackKey := func() {
		if !createdKey {
			return
		}
		if derr := ks.DeleteAgent(context.WithoutCancel(ctx), key); derr == nil {
			res.Serverkey = ""
		}
	}

	// Passo 3: instalar o agente (curl | sudo sh). Redige a chave de qualquer saída.
	cmd := BuildInstallCommand(p.InstallURL, key, p.GatewayURL, p.AgentFlags)
	stdout, err := sess.Run(ctx, cmd)
	if err != nil {
		emit(Step{Step: "install", Status: StatusErr, Detail: redactKey(trimOut(stdout), key, p)})
		rollbackKey()
		return res, fmt.Errorf("instalação do agente falhou: %w", err)
	}
	// Hostname REAL da máquina, extraído do stdout do install (marcador REVOADA_HOSTNAME
	// escrito pelo install.sh) — mesmo $(hostname) que o agent.yaml usa e que o agente
	// reporta. É o elo AUTORITATIVO com `hosts`. Fica "" se ausente (install antigo);
	// nesse caso o handler NÃO grava alias/hostname-de-alvo com palpite — evita o host
	// fantasma que o fallback para o nome amigável criava.
	res.Hostname = parseInstalledHostname(stdout)
	emit(Step{Step: "install", Status: StatusOK, Detail: "agente instalado"})

	// Passo 4: serviço ativo. O install fez `systemctl enable --now`, mas o `is-active`
	// pode correr com o start (ou com o stop/start de um reprovisionamento) e devolver
	// "activating"/"inactive" por um instante — por isso ESPERAMOS estabilizar (até
	// ~20s, saindo assim que ativo), em vez de checar uma única vez.
	//
	// CRÍTICO: aqui NUNCA fazemos rollback da serverkey. O agent.yaml já está no host e
	// o agente pode já estar no ar (Restart=always); apagar a chave o deixaria mudo
	// (401). O rollback só é seguro quando a INSTALAÇÃO falha (a chave ainda não chegou
	// ao host) — que é onde ele continua sendo chamado, acima.
	out, err := sess.Run(ctx, serviceReadyCmd)
	active := strings.TrimSpace(out)
	if err != nil || active != "active" {
		detail := "serviço não confirmou ativo após ~20s"
		if active != "" {
			detail = "serviço em estado: " + active
		}
		emit(Step{Step: "service", Status: StatusErr, Detail: redactKey(detail, key, p)})
		return res, fmt.Errorf("serviço revoada-agent não confirmou ativo (estado=%q)", active)
	}
	emit(Step{Step: "service", Status: StatusOK, Detail: "serviço ativo"})

	return res, nil
}

// serviceReadyCmd espera o serviço do agente estabilizar: até ~20 tentativas (1s
// cada), saindo assim que `systemctl is-active` = "active"; ecoa o estado final. O
// loop roda no HOST (não em Go), então uma única sessão SSH cobre toda a espera e o
// fake de teste (que casa por substring "systemctl is-active") continua funcionando.
const serviceReadyCmd = `for i in $(seq 1 20); do s=$(systemctl is-active revoada-agent 2>/dev/null); [ "$s" = active ] && break; sleep 1; done; echo "$s"`

// parseInstalledHostname extrai o hostname REAL do stdout do install.sh (linha
// "REVOADA_HOSTNAME=<host>"). Devolve "" quando o marcador não está presente
// (install.sh antigo) — o chamador trata "" como "hostname desconhecido" e NÃO
// grava alias/hostname-de-alvo com palpite, evitando host fantasma.
func parseInstalledHostname(stdout string) string {
	for _, line := range strings.Split(stdout, "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "REVOADA_HOSTNAME="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

// BuildInstallCommand monta o comando remoto do instalador de 1 linha. Cada valor
// é aspado em single-quote para robustez de shell (as chaves são base64url e as
// URLs são simples, mas quoting é higiene). Exportado para teste.
//
// Passa --agent-url explicitamente: o install.sh precisa saber DE ONDE baixar o
// binário do agente (sem isso ele aborta em "instalando o binário"). O binário é
// servido no mesmo host do install.sh, em /revoada-agent.
func BuildInstallCommand(installURL, key, gatewayURL, extraFlags string) string {
	cmd := fmt.Sprintf("curl -fsSL %s | sudo sh -s -- --key %s --gateway %s --agent-url %s",
		shellQuote(installURL), shellQuote(key), shellQuote(gatewayURL), shellQuote(deriveAgentURL(installURL)))
	if extraFlags = strings.TrimSpace(extraFlags); extraFlags != "" {
		cmd += " " + extraFlags
	}
	return cmd
}

// AgentResourceFlags formata os limites de recurso como flags do install.sh. Os
// valores são numéricos (do settings store), então não precisam de shell-quote; o
// formato casa exatamente com o parser de flags do install.sh.
func AgentResourceFlags(memMaxMB, memHighMB, cpuQuotaPct, nice, tasksMax, memSoftMB, maxProcs int) string {
	return fmt.Sprintf("--mem-max %dM --mem-high %dM --cpu-quota %d%% --nice %d --tasks-max %d --mem-soft %d --max-procs %d",
		memMaxMB, memHighMB, cpuQuotaPct, nice, tasksMax, memSoftMB, maxProcs)
}

// deriveAgentURL descobre a URL do binário do agente a partir da URL do install.sh:
// mesmo host, caminho /revoada-agent. Ex.: https://p/install.sh →
// https://p/revoada-agent. Se a URL não terminar em /install.sh, troca só o
// último segmento do caminho.
func deriveAgentURL(installURL string) string {
	const agentPath = "revoada-agent"
	if i := strings.LastIndex(installURL, "/"); i >= 0 {
		return installURL[:i+1] + agentPath
	}
	return agentPath
}

// BuildUninstallCommand monta o comando remoto de DESINSTALAÇÃO do agente. É
// autossuficiente (systemctl/rm/userdel), NÃO usa curl/install.sh — logo não
// depende de --agent-url nem de alcançar o painel, e é imune aos furos do install.
// Idempotente (tudo tolera ausência). Ecoa UNINSTALL_OK ao final para o chamador
// confirmar. Roda via sudo (o Run já conecta como usuário com acesso).
func BuildUninstallCommand() string {
	return "sudo sh -c '" + strings.Join([]string{
		"systemctl disable --now revoada-agent 2>/dev/null || true",
		"rm -f /etc/systemd/system/revoada-agent.service",
		"systemctl daemon-reload 2>/dev/null || true",
		"systemctl reset-failed revoada-agent 2>/dev/null || true",
		"crontab -u revoada -r 2>/dev/null || true",
		"rm -f /usr/local/bin/revoada-agent /usr/local/bin/revoada-agent.bak-*",
		"rm -rf /etc/revoada /var/lib/revoada-agent",
		"if id revoada >/dev/null 2>&1; then userdel revoada 2>/dev/null || true; fi",
		"echo UNINSTALL_OK",
	}, "; ") + "'"
}

// shellQuote envolve s em aspas simples, escapando aspas simples internas.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// redactKey remove ocorrências da serverkey de um texto (defesa em profundidade).
func redactKey(s, key string, p Params) string {
	if key != "" {
		s = strings.ReplaceAll(s, key, "***")
	}
	return redact(s, p)
}

// redact remove qualquer eco acidental da credencial SSH de mensagens de erro.
func redact(s string, p Params) string {
	if len(p.Target.secret) > 0 {
		s = strings.ReplaceAll(s, string(p.Target.secret), "***")
	}
	return s
}

// trimOut limita o tamanho da saída echoada no progresso.
func trimOut(s string) string {
	s = strings.TrimSpace(s)
	const max = 800
	if len(s) > max {
		return s[len(s)-max:]
	}
	return s
}
