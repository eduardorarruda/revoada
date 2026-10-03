# Auto-atualização dos agentes

## Por que existe

Correção de veracidade de métrica é inútil enquanto não chega à frota. Antes
disto, toda correção ficava parada no servidor do painel até alguém entrar por
SSH em cada host.

Pior do que o atraso é a **frota misturada**. A 0.8.0 mudou o significado de três
métricas que já existiam:

| Métrica | Antes da 0.8.0 | A partir da 0.8.0 |
|---|---|---|
| `system.cpu.utilization` | instantâneo de 500 ms | média do intervalo entre coletas |
| `container.cpu.utilization` | escala do Docker (100% = um núcleo) | escala da máquina |
| rede | todas as interfaces (inclusive loopback e veths) | só interfaces físicas |

Enquanto metade da frota roda 0.7.x e metade roda 0.8.x, a mesma tela compara
servidores que medem CPU de maneiras **diferentes**, com a mesma cor e a mesma
autoridade. Isso não é atraso de release: é conclusão errada tomada com
confiança.

## A restrição que define o desenho

O agente roda como o usuário **`revoada`** (não-root), numa unit systemd com
`ProtectSystem=strict`, e o binário vive em `/usr/local/bin/revoada-agent`,
que é de **root**.

**O agente não consegue sobrescrever o próprio binário — e não deve.** No dia em
que conseguir, comprometer o agente passa a valer virar root no servidor
monitorado.

Então a troca é feita em duas mãos:

```
┌─ AGENTE (revoada, sem privilégio) ────────────────────────────────────────┐
│ 1. Pergunta ao painel, de hora em hora com jitter, qual versão rodar   │
│ 2. Baixa para /var/lib/revoada-agent/update/                      │
│ 3. Confere o SHA-256                                                   │
│ 4. EXECUTA o binário baixado e confere que ele diz ser a versão certa  │
│ 5. Sai com código 0                                                    │
└────────────────────────────────────────────────────────────────────────┘
┌─ SYSTEMD (root) ───────────────────────────────────────────────────────┐
│ 6. ExecStartPre=-+/usr/local/lib/revoada/promote-update.sh        │
│    reconfere o SHA-256, confere o magic ELF, guarda cópia do binário   │
│    atual e move o novo para /usr/local/bin                             │
│ 7. Restart=always reergue o serviço, já com a versão nova              │
└────────────────────────────────────────────────────────────────────────┘
```

O único componente privilegiado do caminho é o systemd — que já era o único
componente privilegiado do serviço.

### Os dois prefixos do `ExecStartPre` não são decoração

```
ExecStartPre=-+/usr/local/lib/revoada/promote-update.sh
```

- **`+`** roda aquela linha como root, ignorando `User=revoada` e
  `ProtectSystem=strict`. Sem ele, `/usr` é somente-leitura e a promoção falha.
  **Exige systemd ≥ 231.**
- **`-`** faz uma falha do promotor ser ignorada. Sem ele, um script ausente ou
  com defeito impediria o serviço de subir: a atualização derrubaria a coleta.

## Como desligar

### Num host específico

Edite `/etc/revoada/agent.yaml`:

```yaml
auto_update: false
```

e `systemctl restart revoada-agent`. Ou reinstale com `--no-auto-update`.

### Na instalação

```sh
sh install.sh --key <CHAVE> --gateway <URL> --no-auto-update
```

### Na frota inteira

Pelo painel, via a política global (ver *O que falta ligar*). Com a
auto-atualização global desligada, o painel responde `atualizar: false` a todo
agente — nenhum host precisa ser tocado.

## Como fixar a versão (pin)

O pin é a forma de **segurar a frota** sem desligar a função. Fixado em `0.8.5`
enquanto o painel publica `0.9.0`, o servidor responde:

```json
{"atualizar": false, "versao": "0.9.0",
 "motivo": "frota fixada em 0.8.5; o painel publica 0.9.0"}
```

**O pin nunca vira uma URL de download quando a versão fixada não é a publicada.**
O `dist` é plano — um binário por plataforma, sem subpasta por versão —, então a
única URL disponível serviria `0.9.0` com o rótulo `0.8.5`. O agente pegaria a
divergência na checagem de sanidade, mas só depois de gastar a banda e reportar
erro de hora em hora. Responder "não atualize" é literalmente o que o operador
pediu ao fixar a versão.

> Consequência prática: **pin só funciona para segurar, não para voltar.** Não há
> como fixar numa versão que o painel não publica mais. Voltar de versão exige
> republicar o artefato antigo no `dist`.

## O que acontece quando falha

A regra absoluta é: **falha de atualização nunca interrompe a coleta.** Um host
defasado é um problema; um host que parou de coletar por causa do atualizador é
um host cego, que é estritamente pior.

| Falha | O que acontece |
|---|---|
| Painel inacessível | Log em `warn`, nova tentativa no próximo intervalo |
| Resposta sem SHA-256 | Nada é baixado — recusado antes do download |
| **SHA-256 divergente** | Arquivo apagado, nada instalado, estado `erro_download` reportado |
| Tamanho diferente do anunciado | Download abortado |
| Binário não executa | Não é promovido, estágio descartado |
| Binário diz outra versão | Não é promovido (dist meio sincronizado) |
| Painel manda versão mais velha | Recusado — o agente **nunca** faz downgrade sozinho |
| Painel manda a mesma versão | Recusado — seria laço de reinício permanente |
| `doctor` sai != 0 | **Não impede a troca** (gateway fora do ar não é defeito do binário) |
| `doctor` não consegue rodar | Impede a troca |
| Estagiou e o serviço voltou na versão antiga | Ao 2º estágio o agente desiste daquela versão e reporta `promocao_nao_ocorreu` |
| Versão promovida sobe e estoura | Após 3 starts sem marca de saúde, o promotor **restaura o binário anterior** |
| Pânico dentro do atualizador | Contido; a coleta segue |

### O ciclo de desfazimento (rollback)

O agente se declara são gravando `update/saudavel` depois de **2 minutos de pé** —
não no boot, porque um binário que estoura no primeiro ciclo de coleta também
chega ao boot. Enquanto a marca não existe, o promotor conta os starts; ao
terceiro, restaura `/usr/local/bin/revoada-agent.bak-anterior`.

Com `RestartSec=5`, um binário quebrado é desfeito em ~15 segundos, sem SSH.

### Onde ver o estado

- **Versão em uso**: já aparece no inventário do painel (`hosts.agent_version`),
  reportada pelo agente a cada ciclo. Funciona hoje, inclusive em macOS e Windows.
- **Versão desejada e resultado da última tentativa**: viajam no mesmo POST da
  consulta horária. Hoje vão para o log do painel; a persistência por agente
  depende do recorte em *O que falta ligar*.
- **No host**: `journalctl -u revoada-agent | grep auto-atualização` e o
  arquivo `/var/lib/revoada-agent/update/estado.json`.

## Jitter: por que a primeira consulta demora

O intervalo é de uma hora, mas **cada espera é sorteada entre 30 e 90 minutos**,
inclusive a primeira.

Sem isso, quando a frota volta junta de uma queda, todo agente sobe no mesmo
segundo, consulta no mesmo segundo e **baixa dezenas de MB no mesmo segundo**,
contra o mesmo painel. Nesta base já se mediu o retorno de uma queda gerar 105× o
regime de escrita com **um** agente; multiplicado pela frota, é derrubar o painel
na hora em que ele mais precisa responder.

O sorteio usa `crypto/rand` porque `math/rand` sem semente é idêntico em todo
processo — e uma frota inteira sorteando o mesmo "aleatório" é exatamente a
boiada que o jitter existe para evitar.

## Cobertura por plataforma — o que funciona e o que não

| Plataforma | Baixa e verifica | Troca o binário sozinha | Observação |
|---|---|---|---|
| **Linux + systemd ≥ 231** | sim | **sim** | Cobertura completa. Debian 9+, Ubuntu 16.10+, RHEL/CentOS 8+ |
| **Linux + systemd < 231** | sim | não | CentOS 7 (systemd 219) não tem o prefixo `+`. O install.sh **detecta e omite** a linha — escrevê-la tornaria a unit inválida e o serviço não subiria. Fica em modo "só estaga"; a troca ocorre na próxima execução do install.sh |
| **Linux por cron** (cPanel, sem systemd) | sim | não | Não há quem promova: `revoada-agent once` é um processo por minuto, e o binário é de root. Modo "só estaga" |
| **linux/arm64** | não | não | O `make agent` só compila `linux/amd64`. O painel responde "não publica binário para linux/arm64" em vez de servir o binário errado |
| **macOS (launchd)** | **não** | não | Desligado explicitamente no `install-macos.sh`. O launchd não tem equivalente ao `ExecStartPre=+`, e o agente não-root não sobrescreve binário de root. Atualizar = rodar o instalador de novo |
| **Windows** | **não** | não | O instalador do Windows não escreve `panel_url`, então o atualizador fica inerte. Sem serviço equivalente para promover o binário |

Em macOS e Windows a **versão em uso continua visível no painel**, então dá para
saber quais hosts estão para trás — só a troca é manual.

## Segurança

### O que está protegido

- **HTTPS obrigatório.** `panel_url` em `http://` desliga a auto-atualização
  (exceto no laço local). O que desce por este cano vira código executado como
  root no reinício seguinte; por HTTP puro, quem está no caminho troca o binário
  **e** o checksum juntos, e verificar o checksum que o atacante mandou não prova
  nada.
- **Mesma origem.** A URL do download chega dentro da resposta, e resposta é
  dado, não ordem. O agente só aceita baixar do mesmo esquema+host do
  `panel_url` que veio do `agent.yaml`. Sem isso, quem conseguisse responder no
  lugar do painel escolheria de onde vem o binário.
- **Checksum obrigatório**, calculado pelo painel a partir do arquivo que ele
  **realmente serve** — nunca digitado à mão, nunca herdado de um build anterior.
  Conferido duas vezes: pelo agente antes de estagiar, e pelo promotor root antes
  de instalar.
- **Teto de tamanho** (256 MB) e recusa de link simbólico no estágio.
- **Chave revogada não recebe binário.**

### O risco residual, declarado

O diretório de estágio é gravável pelo `revoada` e o promotor roda como root. Quem
**já** tiver execução de código como `revoada` pode escrever um binário arbitrário
no estágio, escrever o `.meta` com o checksum correspondente e obter execução
como root no próximo start do serviço.

O checksum não fecha esse buraco: quem escreve o binário também escreve o
checksum. As verificações do promotor (SHA-256, ELF, link simbólico) protegem
contra **corrupção e acidente**, não contra um `revoada` hostil.

> **Recomendação:** para ser defensável, o manifesto precisa de **assinatura
> criptográfica** — o painel assina `sha256+versão` com uma chave privada, e a
> chave pública é gravada pelo `install.sh` num arquivo de root (fora do alcance
> do `revoada`) que o promotor verifica. Isso corta o caminho `revoada → root`, porque
> o agente deixa de conseguir forjar um manifesto válido.
>
> Não foi construído aqui de propósito: inventar infraestrutura de chaves por
> conta própria (geração, rotação, distribuição, o que fazer quando a chave
> vaza) é uma decisão de arquitetura que precisa ser tomada, não improvisada.

Vale a proporção: antes desta função, atualizar a frota exigia SSH como root em
cada host — um caminho para root muito mais largo. O risco residual acima é menor
do que o que ele substitui, mas é real e deve ser fechado.

## O que falta ligar

Os arquivos a seguir estão **fora** do recorte desta frente e precisam do
recorte de quem os possui. Sem eles a rota existe e funciona, mas **não está
registrada** e o pin/desligamento global não persistem.

### 1. Rota (`server/internal/httpapi/httpapi.go`)

A rota é autenticada por serverkey (`X-Revoada-Key`), não por sessão — logo não
entra nos wrappers `admin()`/`protected()`. Deve ir com limite por IP, como a
rota de inscrição:

```go
if d.AgentUpdate != nil {
    mux.Handle("POST /api/agent/update-check", http.HandlerFunc(authRL.wrap(d.AgentUpdate.Check)))
}
```

### 2. Wiring (`server/cmd/server/main.go`)

```go
artefatos := installer.NewArtefatos(os.DirFS(config.AgentDistDir()))
// O 4º argumento é a FonteDePolitica; passe nil até o item 3 existir.
agentUpdate := agents.NewUpdateHandler(st, artefatos, config.PublicURL(), nil, log)
```

### 3. Persistência (`server/internal/store/`)

Implementar `agents.FonteDePolitica`:

- **Global**: uma chave nova em `app_settings` (ex.: `agent_auto_update`) com
  `{"desligado": bool, "pin": "x.y.z"}`, no mesmo molde de
  `GetAgentResourceLimits`/`SetAgentResourceLimits`.
- **Por agente**: colunas em `agents`. Como o servidor e o gateway aplicam schema
  na ordem em que subirem, o `ALTER` precisa existir nos **dois**
  (`server/internal/store/schema.sql` e `gateway/internal/pg/schema.sql`), com
  `IF EXISTS`:

```sql
ALTER TABLE IF EXISTS agents ADD COLUMN IF NOT EXISTS auto_update_off  BOOLEAN NOT NULL DEFAULT FALSE;
ALTER TABLE IF EXISTS agents ADD COLUMN IF NOT EXISTS pinned_version   TEXT;
ALTER TABLE IF EXISTS agents ADD COLUMN IF NOT EXISTS update_state     TEXT;
ALTER TABLE IF EXISTS agents ADD COLUMN IF NOT EXISTS update_error     TEXT;
ALTER TABLE IF EXISTS agents ADD COLUMN IF NOT EXISTS update_target    TEXT;
ALTER TABLE IF EXISTS agents ADD COLUMN IF NOT EXISTS update_reported_at TIMESTAMPTZ;
```

Enquanto `FonteDePolitica` for `nil`, o comportamento é: atualizar sempre que
houver versão mais nova, e o relato do agente vai só para o log. **Quando a fonte
existe e falha ao responder, o painel segura a frota** — um banco fora do ar não
pode virar "atualiza todo mundo porque não consegui ler o pin".

### 4. `VERSION` no dist (`Makefile`)

O painel prefere `<dist>/VERSION` para saber a versão publicada e cai numa
constante compilada (`versaoAgentePadrao`) quando o arquivo não existe. Hoje o
`make agent` não escreve o arquivo. Basta:

```make
	echo "$(AGENT_VERSION)" > dist/VERSION
```

Há um teste (`TestFallbackDeVersaoAcompanhaOAgente`) que falha se a constante do
servidor divergir do `var version` do agente.

## Verificar a instalação

O `install.sh` roda como root em servidor de cliente; toda mudança nele deve ser
provada em container descartável, **nunca na máquina local nem em produção**:

```sh
make agent-linux          # precisa de dist/revoada-agent
docker run --rm -v "$PWD:/repo:ro" debian:12 sh -c '
  apt-get update -qq && apt-get install -y -qq curl cron python3 &&
  (cd /repo/dist && python3 -m http.server 8000 --bind 127.0.0.1 &) && sleep 2 &&
  cp /repo/deploy/agent/prova-instalacao.sh /tmp/p.sh && sh /tmp/p.sh'
```

O script se recusa a rodar fora de um container.

O `prova-instalacao.sh` cobre, entre outras coisas: a unit gerada em systemd
moderno e antigo, o modo cron, a promoção com checksum correto, a recusa de
checksum divergente, de link simbólico e de conteúdo não-ELF, o desfazimento
automático, e a cicatriz do crontab (falha de **leitura** não pode virar
**exclusão** do crontab do cliente).
