// Package config carrega a configuração do agente (/etc/revoada/agent.yaml).
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	// Canal com o painel (ARQUITETURA §7): identidade por certificado e tarefas assinadas.
	Canal Canal `yaml:"canal"`

	GatewayURL      string   `yaml:"gateway_url"`      // ex: http://gateway:8090
	Key             string   `yaml:"key"`              // chave de ingestão (tabela agents)
	Hostname        string   `yaml:"hostname"`         // sobrescreve o hostname detectado
	IntervalSeconds int      `yaml:"interval_seconds"` // coleta de métricas (default 15)
	BufferDir       string   `yaml:"buffer_dir"`       // WAL em disco quando o gateway está fora
	CollectLogs     bool     `yaml:"collect_logs"`     // envia logs dos arquivos abaixo
	LogPaths        []string `yaml:"log_paths"`        // arquivos de log a seguir (tail)
	// Coletores de logs do servidor inteiro (Fase D). Todos default false (opt-in):
	// exigem afrouxar o sandbox do systemd (ver deploy/agent/revoada-agent.service).
	CollectJournald   bool `yaml:"collect_journald"`    // journald via journalctl -o json --follow
	CollectDockerLogs bool `yaml:"collect_docker_logs"` // logs de containers via socket do Docker
	CollectSyslog     bool `yaml:"collect_syslog"`      // /var/log/syslog + /var/log/messages
	CollectDmesg      bool `yaml:"collect_dmesg"`       // ring buffer do kernel via /dev/kmsg
	// CollectFilesystems: "all" (default) coleta disco por montagem; "root" só o `/`.
	CollectFilesystems string `yaml:"collect_filesystems"`
	// CollectContainers: coleta métricas/estado por container Docker. Ponteiro para
	// distinguir "ausente" (default true) de "false" explícito. Só age se houver socket.
	CollectContainers *bool    `yaml:"collect_containers"`
	Probe             bool     `yaml:"probe"`          // marca este agente como sonda (P6.3)
	ProbeLocation     string   `yaml:"probe_location"` // rótulo da região da sonda
	ProbeURLs         []string `yaml:"probe_urls"`     // URLs sondadas por esta sonda

	// ─── Salvaguardas de não-sobrecarga (caps configuráveis) ───────────────────
	// LogMaxLinesPerSec limita quantas LINHAS de log/segundo o agente envia (soma de
	// todas as fontes). Protege o host contra floods (um container/serviço tagarela).
	// Ponteiro: ausente => default seguro (defaultLogRate); 0 explícito => sem limite.
	LogMaxLinesPerSec *int `yaml:"log_max_lines_per_sec"`
	// LogMaxBytesPerSec limita quantos BYTES de log/segundo o agente envia (soma de
	// todas as fontes). Ponteiro: ausente => default (defaultLogByteRate); 0 => sem
	// limite.
	//
	// Por que os DOIS limites existem: o teto de linhas/s sozinho garante a coisa
	// errada. A garantia declarada sempre foi em volume ("~1 MB/s a 200 B/linha"),
	// mas o que o código media era CONTAGEM. Uma aplicação que loga JSON estruturado
	// de 4 KB por linha — Spring Boot, Rails com lograge, qualquer coisa com stack
	// trace serializada — cabe folgada dentro de 5000 linhas/s e manda 20 MB/s: 1,7 TB
	// por dia saindo do link do servidor do cliente, com o agente convencido de que
	// está dentro do limite. Byte é a unidade que o link, a franquia e o disco do
	// gateway cobram; linha não é unidade de nada.
	LogMaxBytesPerSec *int `yaml:"log_max_bytes_per_sec"`
	// MaxContainers limita quantos containers têm métricas coletadas por ciclo.
	// 0 (default) => sem limite explícito (só o orçamento de 10s do coletor).
	MaxContainers int `yaml:"max_containers"`
	// BufferMaxFiles é o teto do WAL em disco (lotes OTLP quando o gateway está fora).
	// <=0 => default (defaultBufferFiles). Ao estourar, descarta o lote mais antigo.
	BufferMaxFiles int `yaml:"buffer_max_files"`
	// SelfMetrics: emite o próprio consumo do agente (agent.self.*) a cada ciclo, para
	// PROVAR o footprint ao vivo. Ponteiro: ausente => default true; false desliga.
	SelfMetrics *bool `yaml:"self_metrics"`

	// Resources: limites de recurso que o PRÓPRIO agente aplica no boot (editáveis
	// via agent.yaml + restart, sem reinstalar). Complementam a cerca dura do systemd
	// (MemoryMax/CPUQuota na unit). Ver Config.Resources* abaixo.
	ResourceLimits ResourceLimits `yaml:"resources"`

	// ─── Auto-atualização (ver agent/internal/selfupdate) ──────────────────────
	// PanelURL é o endereço do painel — é a ele que o agente pergunta qual versão
	// deveria estar rodando, e é dele (e só dele) que aceita baixar o binário.
	// Vazio DESLIGA a auto-atualização: sem saber a quem perguntar, não há o que
	// fazer. É o que mantém inerte todo agente instalado antes desta função.
	PanelURL string `yaml:"panel_url"`
	// AutoUpdate é o opt-out do operador. Ponteiro para distinguir "ausente"
	// (default: ligado) de `auto_update: false` explícito. O painel também
	// consegue segurar a frota do lado dele (desligamento global e pin); esta
	// chave é o freio local, para o host que não pode mudar sozinho.
	AutoUpdate *bool `yaml:"auto_update"`
	// UpdateDir é onde o binário novo é estagiado. Precisa ser gravável pelo
	// usuário do agente E ser o MESMO caminho que o promotor root conhece — por
	// isso o install.sh escreve o literal aqui em vez de deixar os dois lados
	// adivinharem. Vazio => irmão do buffer_dir.
	UpdateDir string `yaml:"update_dir"`
	// UpdateCheckMinutes é o intervalo entre consultas (com jitter aplicado em
	// cima). <=0 => default de 60 minutos.
	UpdateCheckMinutes int `yaml:"update_check_minutes"`
	// UpdateApply diz o que fazer depois de estagiar: "systemd" (pede o reinício,
	// e o ExecStartPre=+ da unit promove) ou "stage" (só deixa pronto). O default
	// é "stage" porque é o único seguro em toda parte: o install.sh só escreve
	// "systemd" onde de fato instalou o promotor.
	UpdateApply string `yaml:"update_apply"`
}

// ResourceLimits são os tetos de recurso aplicados em runtime pelo agente.
type ResourceLimits struct {
	// MemoryLimitMB vira GOMEMLIMIT (soft): ao se aproximar, o GC fica mais agressivo
	// para segurar o heap — evita o pico de memória do agente competir com o host.
	// 0 => não aplica (deixa só a cerca do systemd). Deve ter folga sobre o uso real
	// (um valor baixo demais provoca thrashing de GC).
	MemoryLimitMB int `yaml:"memory_limit_mb"`
	// MaxProcs limita GOMAXPROCS (nº de núcleos que o runtime usa em paralelo).
	// 0 => automático (todos os núcleos). Segurar em 1–2 mantém a coleta leve e
	// previsível mesmo num host de muitos núcleos.
	MaxProcs int `yaml:"max_procs"`
}

// Defaults das salvaguardas. Generosos o bastante para nunca afetar operação normal
// (um host comum produz muito menos que isto), mas presentes para dar a garantia.
const (
	defaultLogRate = 5000 // linhas/s somadas
	// defaultLogByteRate é o teto de VOLUME, que é o que a garantia sempre disse
	// (~1 MB/s) e o que o teto de linhas nunca mediu. 1 MiB/s ≈ 86 GB/dia no pior
	// caso sustentado: já é generoso demais para operação normal (um host comum
	// produz kilobytes por segundo), e ao mesmo tempo impede o caso real que passava
	// batido — linha de JSON de 4 KB × 5000 linhas/s = 20 MB/s = 1,7 TB/dia.
	defaultLogByteRate = 1 << 20 // 1 MiB/s somado entre todas as fontes
	defaultBufferFiles = 5000    // ~20,8h de fila a ticks de 15s
)

// LogRateLimit devolve o limite de linhas/s (0 = ilimitado). Ausente => default.
func (c Config) LogRateLimit() int {
	if c.LogMaxLinesPerSec == nil {
		return defaultLogRate
	}
	return *c.LogMaxLinesPerSec
}

// LogByteRateLimit devolve o limite de BYTES/s de log (0 = ilimitado). Ausente =>
// default. É consumido pelo token bucket de bytes em internal/logtail (ao lado do
// de linhas): os dois valem ao mesmo tempo, e o que estourar primeiro corta.
//
// Ter os dois não é redundância. O teto de linhas protege o CAMINHO (parse, envio,
// número de registros no ClickHouse); o de bytes protege o LINK e o DISCO. Uma
// aplicação tagarela de linhas curtas estoura o primeiro; uma aplicação com JSON de
// 4 KB/linha estoura só o segundo — e era exatamente essa que passava inteira.
func (c Config) LogByteRateLimit() int {
	if c.LogMaxBytesPerSec == nil {
		return defaultLogByteRate
	}
	return *c.LogMaxBytesPerSec
}

// BufferFiles devolve o teto de arquivos do WAL (sempre > 0).
func (c Config) BufferFiles() int {
	if c.BufferMaxFiles <= 0 {
		return defaultBufferFiles
	}
	return c.BufferMaxFiles
}

// SelfMetricsEnabled: default true; só desliga com self_metrics: false explícito.
func (c Config) SelfMetricsEnabled() bool {
	return c.SelfMetrics == nil || *c.SelfMetrics
}

// AllFilesystems indica se a coleta de disco deve iterar todas as montagens
// físicas (default) ou apenas o `/`. Config antiga sem o campo => todas.
func (c Config) AllFilesystems() bool {
	return c.CollectFilesystems != "root"
}

// Containers indica se deve coletar métricas por container Docker. Config antiga
// sem o campo => true; só é desligada com `collect_containers: false` explícito.
func (c Config) Containers() bool {
	return c.CollectContainers == nil || *c.CollectContainers
}

// ─── Auto-atualização ────────────────────────────────────────────────────────

// AutoUpdateEnabled: default true; só desliga com `auto_update: false` explícito
// ou sem `panel_url`. Os dois testes juntos são de propósito — um agente com
// auto_update ligado e sem painel configurado não tem a quem perguntar, e tratar
// isso como "ligado" só produziria um aviso por hora em todo host antigo.
func (c Config) AutoUpdateEnabled() bool {
	if c.PanelURL == "" {
		return false
	}
	return c.AutoUpdate == nil || *c.AutoUpdate
}

// UpdateDirOrDefault devolve o diretório de estágio. O default é irmão do
// buffer_dir (/var/lib/revoada-agent/update ao lado de .../buffer) porque é
// o único lugar que o agente comprovadamente escreve: o instalador cria
// /var/lib/revoada-agent como revoada e a unit o declara em ReadWritePaths.
// Fora dele, ProtectSystem=strict recusa a escrita e nenhuma atualização ocorre.
func (c Config) UpdateDirOrDefault() string {
	if c.UpdateDir != "" {
		return c.UpdateDir
	}
	return filepath.Join(filepath.Dir(c.BufferDir), "update")
}

// updateCheckMinimo é o piso do intervalo de consulta. Um valor pequeno demais no
// agent.yaml (engano de digitação, `1` lido como segundo) multiplicado pela frota
// vira uma enxurrada de consultas contra o painel — o mesmo estouro de boiada que
// o jitter existe para evitar, só que permanente.
const updateCheckMinimo = 5 * time.Minute

func (c Config) UpdateInterval() time.Duration {
	if c.UpdateCheckMinutes <= 0 {
		return time.Hour
	}
	if d := time.Duration(c.UpdateCheckMinutes) * time.Minute; d > updateCheckMinimo {
		return d
	}
	return updateCheckMinimo
}

// UpdateApplyMode devolve "systemd" só quando escrito explicitamente. Qualquer
// outro valor (inclusive lixo) cai em "stage", que nunca reinicia o serviço —
// errar para o lado de não mexer é a única escolha aceitável aqui.
func (c Config) UpdateApplyMode() string {
	if c.UpdateApply == "systemd" {
		return "systemd"
	}
	return "stage"
}

func (c Config) Interval() time.Duration {
	if c.IntervalSeconds <= 0 {
		return 15 * time.Second
	}
	return time.Duration(c.IntervalSeconds) * time.Second
}

// Load lê e valida o YAML.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("lendo %s: %w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if c.GatewayURL == "" {
		return Config{}, fmt.Errorf("gateway_url obrigatório")
	}
	if c.Key == "" {
		return Config{}, fmt.Errorf("key (chave de ingestão) obrigatória")
	}
	if c.BufferDir == "" {
		c.BufferDir = "/var/lib/revoada-agent/buffer"
	}
	if c.Canal.Dir == "" {
		// ao lado do buffer: é o diretório que o serviço comprovadamente escreve
		c.Canal.Dir = filepath.Join(filepath.Dir(c.BufferDir), "canal")
	}
	if c.Hostname == "" {
		if h, err := os.Hostname(); err == nil {
			c.Hostname = h
		}
	}
	return c, nil
}

// Canal configura o canal com o painel.
type Canal struct {
	// Dir guarda a identidade do agente (certificado, chaves) e os eventos ainda não
	// confirmados. Padrão: <diretório do buffer>/../canal.
	Dir string `yaml:"dir"`
	// TarefasPermitidas é a lista de tipos de tarefa que ESTE servidor aceita do
	// painel. Vazia = nenhuma: quem manda no servidor é o dono do servidor, e um
	// painel comprometido não consegue rodar o que não foi liberado aqui.
	// Ex.: [diagnostico.eco, migracao.simular, firebird.diagnostico]
	TarefasPermitidas []string `yaml:"tarefas_permitidas"`
	// Deploy: as aplicações que o painel pode implantar NESTE servidor (tarefa
	// deploy.aplicar). O painel só manda o nome e a versão; o que roda vem daqui.
	Deploy map[string]AplicacaoDeploy `yaml:"deploy"`
}

// AplicacaoDeploy descreve como implantar uma aplicação neste servidor.
type AplicacaoDeploy struct {
	Tipo      string `yaml:"tipo"`      // compose | script
	Diretorio string `yaml:"diretorio"` // onde fica o compose ou os scripts
	Arquivo   string `yaml:"arquivo"`   // compose: o arquivo (padrão docker-compose.yml)
	// script: caminhos relativos ao diretório; recebem REVOADA_VERSAO (e o rollback
	// também REVOADA_VERSAO_ANTERIOR). .ps1 roda no PowerShell, o resto no sh.
	Script   string `yaml:"script"`
	Rollback string `yaml:"rollback"`
	Saude    string `yaml:"saude"`        // URL de health check (http://127.0.0.1:…)
	EsperaS  int    `yaml:"espera_saude"` // segundos esperando ficar saudável (padrão 60)
}
