// Dicionário de métricas — fonte ÚNICA da verdade para rótulo amigável, unidade e
// descrição de cada métrica técnica. Dashboards, /explore, /logs, /alerts e os
// modais de regra consomem daqui, para o nome técnico (ex.: "agent.self.cpu.percent")
// nunca vazar cru na interface e para toda unidade ter origem explícita.
//
// O backend NÃO carrega unidade (não há coluna `unit` no ClickHouse nem no wire
// OTLP), então a unidade mora aqui, no front. Métrica sem entrada cai num rótulo
// DERIVADO do nome técnico (não quebra a tela) e é sinalizada: `known === false`
// (o dev vê um aviso no console e há teste garantindo cobertura das métricas reais).

/** Unidade de medida — determina o formatador aplicado ao valor. */
export type Unit =
  | "percent" // 0..100, sufixo "%"
  | "bytes" // base 1024 (B/KB/MB/GB/TB)
  | "bytes_per_s" // taxa de bytes, sufixo "/s"
  | "count" // contagem inteira (escala compacta se grande)
  | "duration_s" // duração em segundos (ms/s/min/h)
  | "duration_ms" // duração já medida em MILISSEGUNDOS (as fases de sondagem HTTP)
  | "days" // dias inteiros (ex.: validade restante de certificado)
  | "cores" // núcleos de CPU
  | "bool" // 0/1 (estado)
  | "none"; // número puro, sem unidade

export interface MetricMeta {
  /** Rótulo amigável para exibir (ex.: "CPU (%)"). */
  label: string;
  unit: Unit;
  /** Descrição curta para tooltip / modo avançado. */
  description: string;
  /** false quando a métrica não está no dicionário (rótulo derivado do nome técnico). */
  known: boolean;
}

// Entradas conhecidas. As chaves são exatamente os nomes que o agente emite
// (conferidos no código do agente/gateway). Ao adicionar métrica nova no agente,
// adicione aqui — o teste `dict.test.ts` falha se uma métrica real ficar de fora.
const KNOWN: Record<string, Omit<MetricMeta, "known">> = {
  // Sistema
  "system.cpu.utilization": {
    label: "CPU consumida (%)",
    unit: "percent",
    description:
      "Só o que os processos deste servidor realmente EXECUTARAM, sem o tempo em que ficaram na fila esperando o hipervisor. Serve para responder “meu software está pesado?”. Para julgar se a máquina está sob pressão, use “CPU (%)”, que soma esta parcela com a roubada, num VPS com o host físico lotado, o consumo pode ser baixo enquanto o servidor arrasta. Publicada assim a partir do agente 0.8.1; antes dele esta métrica já vinha com o roubo dentro.",
  },
  "system.cpu.utilization.total": {
    label: "CPU (%)",
    unit: "percent",
    description:
      "O tempo total em que os processos deste servidor quiseram o processador, tendo conseguido ou não. É a soma de “CPU consumida” com “CPU roubada pelo hipervisor”, e é o número que o Revoada mostra como CPU nos cartões, no mural e nos gráficos, porque é ele que responde “esta máquina está sob pressão?”. Quando estiver alto, olhe as duas parcelas: consumo alto é software pesado (você resolve); roubo alto é servidor físico lotado do provedor (só o suporte deles resolve).",
  },
  "system.memory.utilization": { label: "RAM (%)", unit: "percent", description: "Uso de memória do servidor." },
  "system.cpu.steal": {
    label: "CPU roubada pelo hipervisor (%)",
    unit: "percent",
    description:
      "Fatia do tempo de CPU que a máquina virtual PEDIU e não recebeu porque o hipervisor a entregou a outro hóspede. Acima de ~5% de forma sustentada a lentidão não é da sua aplicação: é do servidor físico (vizinho barulhento ou overbooking do provedor). É uma série SEPARADA de “CPU (%)”, sem sobreposição: somar as duas dá o tempo total em que seus processos não estavam parados por vontade própria.",
  },
  "system.memory.used": { label: "RAM usada", unit: "bytes", description: "Memória em uso." },
  "system.memory.total": { label: "RAM total", unit: "bytes", description: "Memória física total." },
  "system.memory.available": {
    label: "RAM disponível",
    unit: "bytes",
    description:
      "Memória que o sistema pode entregar a um processo novo sem recorrer a swap, inclui cache/buffers recuperáveis. É o número honesto de “quanta RAM ainda sobra”, mais útil que a memória livre pura.",
  },
  "system.filesystem.utilization": { label: "Disco (%)", unit: "percent", description: "Uso do sistema de arquivos." },
  "system.filesystem.used": { label: "Disco usado", unit: "bytes", description: "Espaço em disco em uso." },
  "system.filesystem.total": { label: "Disco total", unit: "bytes", description: "Capacidade total do disco." },
  "system.filesystem.available": {
    label: "Disco livre",
    unit: "bytes",
    description:
      "Espaço ainda gravável no ponto de montagem. Pode ser menor que “total menos usado”, porque o ext4 reserva uma fatia para o root, é este o número que decide se a gravação vai falhar.",
  },
  "system.paging.utilization": { label: "Swap (%)", unit: "percent", description: "Uso da área de troca (swap)." },
  "system.paging.used": { label: "Swap usado", unit: "bytes", description: "Swap em uso." },
  "system.paging.total": { label: "Swap total", unit: "bytes", description: "Swap total." },
  "system.processes.count": { label: "Processos", unit: "count", description: "Número de processos em execução." },
  "system.uptime": {
    label: "Tempo desde o último boot",
    unit: "duration_s",
    description: "Há quanto tempo o servidor está ligado sem reiniciar. Uma queda brusca deste número indica que a máquina rebootou.",
  },
  "host.cpu.cores": { label: "Núcleos de CPU", unit: "cores", description: "Quantidade de núcleos lógicos." },
  // Carga média (load average): NÃO é percentual. É o tamanho médio da fila de
  // processos prontos para rodar; compara-se com o NÚMERO DE NÚCLEOS, não com 100.
  "system.cpu.load_average.1m": {
    label: "Carga média (1 min)",
    unit: "none",
    description:
      "Número médio de processos disputando CPU no último minuto. NÃO é porcentagem: compare com a quantidade de núcleos, 4,0 num servidor de 4 núcleos já significa fila cheia.",
  },
  "system.cpu.load_average.5m": {
    label: "Carga média (5 min)",
    unit: "none",
    description:
      "Número médio de processos disputando CPU nos últimos 5 minutos. NÃO é porcentagem: compare com a quantidade de núcleos do servidor.",
  },
  "system.cpu.load_average.15m": {
    label: "Carga média (15 min)",
    unit: "none",
    description:
      "Número médio de processos disputando CPU nos últimos 15 minutos. NÃO é porcentagem: compare com a quantidade de núcleos. É a leitura de tendência (a de 1 min é ruidosa).",
  },
  // Rede — contadores ACUMULADOS desde o boot, exatamente com os nomes que o agente
  // emite (`bytes_recv`/`bytes_sent`). Os nomes `.receive`/`.transmit` NUNCA
  // existiram no armazenamento: pedi-los devolvia série vazia, e a aba Rede do
  // detalhe do servidor ficava eternamente "sem dados".
  "system.network.io.bytes_recv": {
    label: "Rede recebida (acumulado)",
    unit: "bytes",
    description: "Total de bytes recebidos pela interface desde o boot (contador que só cresce). A taxa por segundo aparece na listagem de servidores.",
  },
  "system.network.io.bytes_sent": {
    label: "Rede enviada (acumulado)",
    unit: "bytes",
    description: "Total de bytes enviados pela interface desde o boot (contador que só cresce). A taxa por segundo aparece na listagem de servidores.",
  },
  // Containers
  "container.cpu.utilization": { label: "CPU do container (%)", unit: "percent", description: "Quanto da MÁQUINA INTEIRA este container consome, 0 a 100%, a mesma escala do CPU do servidor, então os containers somam para o total. Não é a convenção do docker stats (onde 100% = um núcleo); mudou no agente 0.8.0." },
  "container.memory.utilization": {
    label: "RAM do container (%)",
    unit: "percent",
    description:
      "Memória usada dividida pelo LIMITE do container. Atenção ao denominador: sem `--memory`/`mem_limit` definido, o limite é a RAM da máquina inteira, conferido em dev, os 7 containers reportavam limite = MemTotal do host. Então 80% significa \"80% do meu limite\" num container com teto e \"80% da máquina\" noutro sem teto, com o mesmo semáforo 70/90 aplicado aos dois. Confira `container.memory.limit` antes de concluir que o container está perto de estourar.",
  },
  "container.memory.usage": { label: "RAM do container", unit: "bytes", description: "Memória usada pelo container." },
  "container.memory.limit": { label: "Limite de RAM do container", unit: "bytes", description: "Limite de memória do container. Igual à RAM total da máquina quando nenhum limite foi definido na criação do container." },
  "container.restarts": {
    label: "Reinícios do container (acumulado)",
    unit: "count",
    description:
      "Total de reinícios do container desde que ele foi CRIADO, contador acumulado, não uma taxa. Zera quando o container é recriado (docker compose up recriando o serviço, deploy, mudança de imagem), então uma queda a zero é troca de container, não melhora de estabilidade. Para achar crash-loop, olhe a variação no período, não o valor absoluto; e nunca some entre containers.",
  },
  "container.running": { label: "Container em execução", unit: "bool", description: "1 = em execução, 0 = parado." },
  // Sem esta entrada o Explore mostrava o número CRU: "container.state 5" não diz
  // nada a ninguém. O valor é o código do estado (ver codigoEstado em
  // agent/internal/collect/containers.go) e o detalhe legível vai nos rótulos —
  // por isso a legenda precisa estar aqui, onde a tela procura o significado.
  "container.state": {
    label: "Estado do container (código)",
    unit: "none",
    description:
      "Estado do container como código numérico: 1 = em execução, 2 = criado, 3 = pausado, 4 = reiniciando, 5 = encerrado, 6 = morto, 7 = sendo removido, 0 = desconhecido. Existe como série própria porque o estado muda o tempo todo, e deixá-lo nos rótulos das métricas numéricas fazia a série do container ACABAR a cada mudança, no gráfico isso se lê como “parou de medir”, que é igualzinho a um agente morto. O nome e a imagem legíveis vão nos rótulos desta métrica.",
  },
  // Sondagem sintética de sites (checks HTTP). Estas NÃO vêm do agente: quem as grava
  // é o PRÓPRIO servidor (server/internal/sitecheck/checker.go). Ficavam de fora do
  // dicionário porque o teste de cobertura só transcrevia o agente — e, como a lista
  // do seletor do /explore sai de `SELECT DISTINCT metric`, elas apareciam lá com o
  // nome técnico humanizado e SEM unidade: "114,23", sem dizer se era ms, s ou nada.
  "synthetic.http.total_ms": {
    label: "Tempo total da sondagem",
    unit: "duration_ms",
    description: "Tempo da requisição inteira, do início até o fim da resposta. É o número que o usuário sente. Sempre gravado, inclusive quando a sondagem falha.",
  },
  "synthetic.http.ttfb_ms": {
    label: "Tempo até o primeiro byte (TTFB)",
    unit: "duration_ms",
    description:
      "Tempo do início da requisição até o primeiro byte da resposta chegar, mede o quanto o servidor demora a começar a responder. Só existe quando a resposta começou: sondagem que falhou antes disso NÃO grava esta métrica (em vez de gravar zero, que se leria como \"respondeu instantaneamente\").",
  },
  "synthetic.http.dns_ms": {
    label: "Tempo de resolução DNS",
    unit: "duration_ms",
    description: "Tempo para traduzir o domínio em endereço IP. Só é gravado quando houve consulta de DNS de verdade (não aparece ao sondar um IP ou com o nome já em cache).",
  },
  "synthetic.http.connect_ms": {
    label: "Tempo de conexão TCP",
    unit: "duration_ms",
    description: "Tempo para abrir a conexão TCP com o servidor. Só é gravado quando a conexão chegou a ser estabelecida.",
  },
  "synthetic.http.tls_ms": {
    label: "Tempo de handshake TLS",
    unit: "duration_ms",
    description: "Tempo para negociar a conexão segura (HTTPS). Só é gravado quando houve handshake, site em HTTP puro e falha antes do TLS não geram ponto.",
  },
  "synthetic.http.cert_days_left": {
    label: "Validade restante do certificado",
    unit: "days",
    description: "Dias até o certificado TLS expirar. Só é gravado quando há certificado válido lido na sondagem. O painel avisa em 30, 15 e 7 dias.",
  },
  "synthetic.http.up": {
    label: "Site respondeu",
    unit: "bool",
    description: "1 = a sondagem teve sucesso, 0 = falhou. É a única métrica de fase sempre presente junto do total: quando ela vale 0, as fases que não chegaram a acontecer simplesmente não têm ponto.",
  },
  // Agente (auto-observação)
  "agent.self.cpu.percent": { label: "CPU do agente (%)", unit: "percent", description: "Uso de CPU do próprio agente." },
  "agent.self.memory.rss_bytes": { label: "RSS do agente", unit: "bytes", description: "Memória residente do agente." },
  "agent.self.memory.heap_bytes": { label: "Heap do agente", unit: "bytes", description: "Heap Go do agente." },
  "agent.self.goroutines": { label: "Goroutines do agente", unit: "count", description: "Goroutines ativas no agente." },
};

/** Nomes das métricas reais emitidas pelo agente — usado pelo teste de cobertura. */
export const KNOWN_METRIC_NAMES = Object.keys(KNOWN);

// Agregações que produzem um número SEM SIGNIFICADO para certas métricas. O seletor
// de agregação as desabilita e mostra o motivo, em vez de deixar o operador montar um
// painel que parece certo e não é.
const AGG_BLOQUEADA: Record<string, Record<string, string>> = {
  "container.restarts": {
    sum: "é um contador acumulado desde a criação do container: somar baldes soma o mesmo passado várias vezes. Use Máximo (max) para o total no período, ou compare o início e o fim.",
  },
};

/**
 * aggBloqueada devolve o MOTIVO de a agregação não valer para a métrica, ou null
 * quando ela é válida.
 */
export function aggBloqueada(metric: string, agg: string): string | null {
  const porNome = AGG_BLOQUEADA[metric]?.[agg];
  if (porNome) return porNome;
  if (/_total$/.test(metric) && (agg === "sum" || agg === "avg")) return MOTIVO_ACUMULADO;
  return null;
}

// Regras de derivação por sufixo, para métricas fora do dicionário: a unidade é
// inferida do fim do nome (ex.: "*.utilization" → percent). Ordem importa.
//
// O SEPARADOR ACEITO É PONTO **OU** UNDERSCORE (`[._]`), e isso não é detalhe.
// As métricas do agente usam ponto (`system.memory.used`); as do próprio painel usam
// underscore, por serem expostas no formato Prometheus (`revoada_nats_stream_bytes`,
// `revoada_agent_ts_age_seconds`). Exigindo ponto, TODA métrica do painel caía em
// unidade "none" e era desenhada como número pelado: bytes viravam "2576", segundos
// viravam "47". É o mesmo defeito que a rede do agente já teve, do outro lado do
// separador.
const SUFFIX_UNIT: [RegExp, Unit][] = [
  [/[._](utilization|percent|pct|usage_percent|steal|iowait)$/, "percent"],
  // `bytes_recv`/`bytes_sent`/`available` entram aqui porque são os sufixos reais do
  // agente: sem eles, uma métrica de rede fora do dicionário saía crua na tela
  // ("5.583.457.484,00") em vez de "5,2 GB".
  [/[._](bytes|heap_bytes|rss_bytes|bytes_recv|bytes_sent|used|total|free|available|usage|limit|cache|buffers)$/, "bytes"],
  // `_ms` antes das durações em segundos: quem já vem em milissegundos (as fases da
  // sondagem HTTP e qualquer métrica de aplicação com o mesmo sufixo) não pode cair em
  // `duration_s`, que leria 114 ms como 114 segundos.
  [/_ms$/, "duration_ms"],
  [/[._](cores|cpus)$/, "cores"],
  [/[._](running|up|healthy|enabled|active)$/, "bool"],
  [/[._](seconds|duration|uptime|latency|elapsed)$/, "duration_s"],
  [/[._](count|total_count|restarts|goroutines|connections|sessions|processes|threads|size)$/, "count"],
];

// CONTADOR ACUMULADO: o sufixo `_total` é a convenção do formato Prometheus e vale
// para QUALQUER métrica, não só para as que alguém lembrou de cadastrar. Somar ou
// mediar um acumulado dentro de um balde não produz número com significado — produz
// uma reta subindo com cara de gráfico legítimo. Medido: `revoada_writer_rows_written_total`
// com agg=avg devolveu 2962 · 9697 · 11126, que é a MÉDIA de um acumulado, e não a
// taxa que quem pediu queria ver.
const MOTIVO_ACUMULADO =
  "é um contador acumulado desde que o serviço subiu: somar ou mediar baldes repete o mesmo passado. Use Máximo (max) para o valor no fim do período, ou compare início e fim para ter a taxa.";

// humanize transforma "system.paging.used" → "System paging used" como fallback de
// rótulo (só quando a métrica não está no dicionário). Mantém legível sem inventar.
function humanize(name: string): string {
  const s = name.replace(/[._]+/g, " ").trim();
  return s.charAt(0).toUpperCase() + s.slice(1);
}

let warned: Set<string> | null = null;

/**
 * metricMeta resolve o metadado de uma métrica. Entrada conhecida → `known:true`.
 * Desconhecida → rótulo derivado do nome técnico + unidade inferida do sufixo
 * (`known:false`), e um aviso único no console em desenvolvimento (nunca em prod)
 * para o time perceber que falta cadastrar a métrica.
 */
export function metricMeta(name: string): MetricMeta {
  const hit = KNOWN[name];
  if (hit) return { ...hit, known: true };

  let unit: Unit = "none";
  for (const [re, u] of SUFFIX_UNIT) {
    if (re.test(name)) {
      unit = u;
      break;
    }
  }
  // Sinaliza a lacuna em desenvolvimento (uma vez por métrica), sem poluir produção.
  // `import.meta.env` não é tipado neste projeto (sem vite/client) → cast pontual.
  const dev = (import.meta as unknown as { env?: { DEV?: boolean } }).env?.DEV;
  if (dev) {
    warned ??= new Set();
    if (!warned.has(name)) {
      warned.add(name);
      console.warn(`[métricas] sem entrada no dicionário: "${name}" (rótulo/unidade derivados). Cadastre em web/src/metrics/dict.ts`);
    }
  }
  return { label: humanize(name), unit, description: "", known: false };
}

/** Rótulo amigável direto (atalho para metricMeta(name).label). */
export function metricLabel(name: string): string {
  return metricMeta(name).label;
}
