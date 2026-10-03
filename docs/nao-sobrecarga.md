# Garantia de não-sobrecarga do agente

Este documento responde à pergunta do gestor: **“como garantir que o agente não vai
pesar nos servidores monitorados?”** com (1) medição real do footprint, (2) as
salvaguardas configuráveis que impõem um teto, e (3) como provar tudo ao vivo.

Resumo em uma linha: **na configuração padrão o agente custa ~0,1% de um núcleo e
~20 MB de RAM, e envia ~1,5 KB a cada 15 s.** Os coletores caros são opt-in e têm
limites configuráveis. Veja também [comunicacao-push-vs-pull.md](comunicacao-push-vs-pull.md).

---

## 1. Footprint medido (números reais)

Medição feita neste repositório com o binário do próprio agente
(`agent/cmd/revoada-agent`), amostrando `/proc/<pid>` por ~75 s (host de 4
núcleos) e capturando o tamanho real do lote OTLP no WAL.

| Recurso | Base (host + self) | Com métricas por container |
|---|---|---|
| **CPU** | 0,09% de **um** núcleo em média (pico 0,40%) → ~0,02% do host | idem + custo do `docker stats` (ver §2) |
| **RAM (RSS)** | ~20 MB (heap Go ~1,6 MB) | ~20 MB |
| **Rede (egress)** | **1,5 KB/tick** (1 POST/15 s) · 28 métricas | ~11 KB/tick neste host (stack Docker de dev) |
| **Rede/dia** | ~8,5 MiB/dia | ~62 MiB/dia (proporcional ao nº de containers) |
| **Goroutines** | ~10 | ~10 |
| **Disco** | WAL só quando o gateway cai (0 em operação normal no modo serviço; no modo cron, 1 gravação de ~60 bytes por coleta para lembrar a leitura de CPU) | idem |

Interpretação para o gestor: o agente base é **desprezível** — menos de um décimo
de 1% de um núcleo e ~20 MB, comparável a um daemon de sistema qualquer. O custo
cresce só com o que você **liga explicitamente** (containers, logs).

> O próprio agente publica essas métricas (`agent.self.*`) — o número acima não é
> promessa, é o que aparece no painel. A RSS auto-reportada (20,2 MB) bateu com a
> medição externa (20,6 MB).

### Separando o barato do caro

- **Barato (sempre ligado):** métricas de host via gopsutil (CPU/RAM/disco/rede/
  swap/uptime/processos). A CPU é medida pela **diferença entre a leitura atual e
  a anterior** (`agent/internal/collect/cpu.go`), cobrindo o intervalo inteiro de
  15s sem segurar o processador; só a primeira leitura de cada processo usa uma
  janela de 500ms, e mesmo essa é um `sleep`, não trabalho de CPU. Custo
  O(nº de montagens) + O(nº de processos).
- **Médio (opt-in, default ligado se há Docker):** métricas por container
  (`collect_containers`). Cada container = 1 `inspect` + (se em execução) 1
  `stats` que bloqueia ~1 s no daemon. Paralelismo limitado (8) e orçamento de 10 s
  por ciclo (`agent/internal/collect/containers.go`) — nunca estoura o tick de 15 s.
- **Caro (opt-in, default DESLIGADO):** coletores de log do servidor inteiro
  (journald/docker/syslog/kernel). São a maior variável de custo e por isso exigem
  ligar o toggle **e** afrouxar o sandbox do systemd (ver
  `deploy/agent/revoada-agent.service`).

---

## 2. Salvaguardas configuráveis (o teto)

Todas em `agent.yaml` (ver `agent/config.example.yaml`), com defaults seguros:

| Knob | Default | O que garante |
|---|---|---|
| `interval_seconds` | 15 | Ritmo da coleta de métricas. Aumente para reduzir custo (ex.: 30/60 s). |
| `collect_containers` | `true` (se há socket) | `false` remove todo o custo de `docker stats`. |
| `max_containers` | `0` (sem teto explícito) | Teto de containers por ciclo; prioriza os **em execução** e descarta o excedente. Use em hosts com dezenas/centenas de containers. |
| `log_max_lines_per_sec` | `5000` | **Rate limit global de logs** (todas as fontes somadas). Protege contra floods (um container tagarela): o excedente é descartado e um aviso periódico é injetado no stream. `0` = sem limite. |
| `collect_journald` / `_docker_logs` / `_syslog` / `_dmesg` | `false` | Coletores de log caros ficam desligados por padrão. |
| `buffer_max_files` | `5000` | Teto do WAL em disco (~20,8 h a 15 s). Ao estourar, descarta o lote **mais antigo** — o disco nunca cresce sem limite. |
| `collect_filesystems` | `all` | `root` restringe a coleta de disco só ao `/`. |
| `self_metrics` | `true` | Emite `agent.self.*` para você auditar o footprint ao vivo. |

Back-pressure já existente (não precisa configurar): canais internos limitados
(4096/4096/1024) que **bloqueiam o produtor** quando o envio não vaza (em vez de
acumular memória), teto de 1 MiB por linha de log, e flush de log em lotes
(200 registros / 2 s).

### Por que isso é uma *garantia*, não um “deve ficar ok”

O caminho caro (logs) agora tem um **teto rígido de linhas/s** independente do
comportamento da aplicação monitorada. Mesmo que um serviço entre em loop cuspindo
100 mil linhas/s, o agente envia no máximo `log_max_lines_per_sec` e descarta o
resto **contando e avisando** — o host não vira refém do log. O caminho de métricas
já era limitado por construção (1 POST/tick, orçamento de 10 s para containers).

---

## 3. Como provar ao vivo (critério de aceite)

1. **Footprint contínuo:** abra a “Visão do Host”, selecione o servidor do agente e
   consulte as métricas `agent.self.cpu.percent`, `agent.self.memory.rss_bytes` e
   `agent.self.goroutines` (Explore → métrica `agent.self.*`). Elas mostram, em
   tempo real, que o agente é leve — inclusive sob carga.
2. **Respeito ao cap de log:** configure `log_max_lines_per_sec: 50` e gere um flood
   (`yes | logger` ou um container em loop). No painel (Logs), o volume estabiliza no
   teto e aparece o aviso `rate limit de logs: N linha(s) descartada(s)…`.
3. **Respeito ao cap de container:** em um host com muitos containers, defina
   `max_containers: 10`; o ciclo permanece dentro do tick de 15 s e coleta os 10
   containers em execução prioritariamente.

### Reproduzindo a medição

```bash
# Suba o stack de dev e o gateway/server (ver docs/runbook-dev.md), então:
cd agent && go build -o /tmp/revoada-agent ./cmd/revoada-agent
/tmp/revoada-agent -config /caminho/agent.yaml       # loop contínuo
# Em outro terminal, amostre o processo:
PID=$(pgrep -f revoada-agent); \
  awk -v p=$PID 'BEGIN{while(1){getline l < ("/proc/"p"/statm"); split(l,a," "); \
  print a[2]*4096/1e6 " MB RSS"; close("/proc/"p"/statm"); system("sleep 5")}}'
```

Tamanho do lote OTLP por tick: rode `revoada-agent once` apontando para um
gateway inexistente — o lote vai para o WAL (`buffer_dir`); `ls -l` no diretório
mostra os bytes exatos daquele envio.
