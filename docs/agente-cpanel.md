# Instalar o agente em servidores cPanel

O `revoada-agent` é um binário Go único (estático, sem dependências). Ele **não é
instalado pela interface do cPanel** — roda no nível do sistema operacional. O que
muda entre um servidor e outro é se você tem **root (WHM)** ou apenas uma **conta
cPanel** (hospedagem compartilhada).

Há dois modos de execução:

| Modo | Comando | Quando usar |
|---|---|---|
| Daemon (contínuo) | `revoada-agent` (default) | Servidor com **systemd** (VPS/dedicado com root) |
| Cron (one-shot) | `revoada-agent once` | Sem systemd — **hospedagem compartilhada** ou preferência por cron |

> `revoada-agent once` coleta as métricas **uma vez e sai**; o cron o chama a cada
> minuto. Se o gateway estiver fora, os dados ficam no `buffer_dir` e são reenviados
> no próximo tick. A descoberta de serviços (mais pesada) fica no `revoada-agent
> discover`, que deve rodar com frequência menor (ex.: a cada 15 min).

---

## Cenário 1 — Servidor próprio com WHM/root (VPS ou dedicado)

Você tem SSH root. O cPanel é irrelevante para a instalação.

### Opção A (recomendada): systemd

```sh
curl -fsSL https://<seu-gateway>/install.sh | sh -s -- \
  --key <CHAVE> --gateway http://<seu-gateway>:8090
```

Cria o serviço `revoada-agent` rodando como usuário **não-root** `revoada`. Monitora o
servidor inteiro (CPU, RAM, disco, todas as contas cPanel, MySQL, etc.).

### Opção B: cron (se preferir, ou se o systemd estiver restrito)

```sh
curl -fsSL https://<seu-gateway>/install.sh | sh -s -- \
  --key <CHAVE> --gateway http://<seu-gateway>:8090 --cron
```

Em vez do systemd, o instalador cria estas linhas no crontab do usuário `revoada`:

```cron
* * * * * /usr/local/bin/revoada-agent -config /etc/revoada/agent.yaml once >/dev/null 2>&1
*/15 * * * * /usr/local/bin/revoada-agent -config /etc/revoada/agent.yaml discover >/dev/null 2>&1
```

O instalador cai **automaticamente** nesse modo se `systemctl` não existir no host.

> **cPanel/CloudLinux (AlmaLinux):** o instalador detecta `yum` e instala as
> dependências (curl, cron). O usuário `revoada` é criado como conta de sistema com
> `nologin` — o cron executa os jobs via `/bin/sh` normalmente, mesmo com `nologin`.

---

## Cenário 2 — Hospedagem compartilhada (conta cPanel, sem root)

Aqui **não há systemd nem `useradd`** — você só tem sua conta. Use o **Cron Jobs**
do cPanel chamando `revoada-agent once`. Passo a passo:

### 1. Coloque o binário na sua home

Via SSH (se habilitado) ou o Gerenciador de Arquivos do cPanel, baixe o binário
para `~/bin` (Linux x86_64):

```sh
mkdir -p ~/bin ~/revoada/buffer
curl -fsSL https://<seu-gateway>/revoada-agent -o ~/bin/revoada-agent
chmod 0755 ~/bin/revoada-agent
```

### 2. Crie o `~/revoada/agent.yaml`

```yaml
gateway_url: http://<seu-gateway>:8090
key: <CHAVE>
hostname: cliente-x-web1        # OBRIGATÓRIO: nome único (o hostname do host é compartilhado!)
buffer_dir: /home/<seu-usuario>/revoada/buffer   # precisa ser gravável pela sua conta
interval_seconds: 60
```

### 3. Teste

```sh
~/bin/revoada-agent -config ~/revoada/agent.yaml doctor
~/bin/revoada-agent -config ~/revoada/agent.yaml once
```

### 4. Agende no cPanel → "Trabalhos Agendados" (Cron Jobs)

Métricas a cada 1 minuto:

```
* * * * *   /home/<seu-usuario>/bin/revoada-agent -config /home/<seu-usuario>/revoada/agent.yaml once >/dev/null 2>&1
```

(Opcional) Descoberta de serviços a cada 15 minutos:

```
*/15 * * * *   /home/<seu-usuario>/bin/revoada-agent -config /home/<seu-usuario>/revoada/agent.yaml discover >/dev/null 2>&1
```

### Limitações importantes da hospedagem compartilhada

- **Visão parcial:** rodando como usuário sem privilégio, o agente enxerga menos do
  host (você vê sua conta/processos, não a máquina inteira). Para visão total, só no
  Cenário 1.
- **`hostname` único é obrigatório** no `agent.yaml` — vários clientes no mesmo host
  físico teriam o mesmo hostname e colidiriam no inventário.
- **Saída de rede:** o provedor precisa permitir conexão de saída até
  `<seu-gateway>:8090`. Se o host bloqueia portas altas, exponha o gateway em 80/443.
- **Limites de processo (LVE/CloudLinux):** jobs de cron muito frequentes ou pesados
  podem ser barrados — por isso `once` é leve (métricas só) e a descoberta é separada.
- **Execução de binário:** alguns provedores desabilitam executar binários próprios
  na home. Se `once` não roda, não há como instalar o agente nesse plano.
- **Termos de uso:** confirme que o provedor permite cron de monitoramento.

---

## Referência de comandos

```
revoada-agent                    # daemon (loop contínuo) — default, para systemd
revoada-agent once               # coleta métricas uma vez e sai — para cron/cPanel
revoada-agent discover           # envia descoberta de serviços uma vez e sai
revoada-agent doctor             # diagnóstico (config, coleta, conectividade com o gateway)
revoada-agent version
```

Todos aceitam `-config <caminho>` (default `/etc/revoada/agent.yaml`).
