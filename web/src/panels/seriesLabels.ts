// Rótulos de série enxutos para a legenda dos gráficos.
//
// Problema que isto resolve: antes a legenda despejava TODAS as labels de cada
// série (`agent.version=0.7.0 arch=x86_64 host=srv1 host.cpu.cores=2 kernel=… os=linux
// platform=ubuntu …`), virando uma parede de texto ilegível. Quase sempre o que
// interessa é UM eixo — normalmente o servidor. As demais labels ou são iguais em
// todas as séries (viram ruído) ou são metadados do host que variam junto com ele
// (kernel, nº de núcleos, versão do agente) e não ajudam a distinguir a linha.
//
// Estratégia: mostrar só o que DISTINGUE as séries entre si.
// - O host vira o nome amigável, sem prefixo (ex.: "web-01").
// - Metadados de host/agente são suprimidos (não são a dimensão de interesse).
// - Dimensões reais que variam (mountpoint, device, cpu, interface…) entram como
//   `chave=valor`.
// - Labels iguais em todas as séries somem.

type Labels = Record<string, string>;

// Chaves que identificam o "servidor" — resolvidas para o nome amigável.
const HOST_KEYS = ["host", "host.name"];

function isHostKey(k: string): boolean {
  return HOST_KEYS.includes(k);
}

// Metadados de host/agente: descrevem a máquina, não a série. Poluem a legenda
// porque variam junto com o host (que já é a identidade). `host.*` e `agent.*`
// cobrem o grosso; os avulsos abaixo não têm prefixo.
// `image` entra aqui porque o agente manda o nome da imagem E o digest para o mesmo
// container: sem filtrar, a legenda ganhava DUAS linhas para um único container (uma
// pelo nome, outra pelo digest), como se fossem dois. A identidade do container é o
// `container`/`name`, não a imagem de onde ele saiu.
const NOISE_KEYS = new Set(["os", "platform", "arch", "kernel", "image"]);

function isNoise(k: string): boolean {
  return NOISE_KEYS.has(k) || k.startsWith("agent.") || (k.startsWith("host.") && !isHostKey(k));
}

function hostOf(labels: Labels): string | undefined {
  for (const k of HOST_KEYS) if (labels[k]) return labels[k];
  return undefined;
}

// Descobre as chaves cujo valor NÃO é igual em todas as séries (as que distinguem).
function varyingKeys(series: { labels: Labels }[]): string[] {
  const keys = new Set<string>();
  for (const s of series) for (const k of Object.keys(s.labels)) keys.add(k);
  const out: string[] = [];
  for (const k of keys) {
    let first: string | undefined;
    let differs = false;
    for (const s of series) {
      const v = s.labels[k] ?? "";
      if (first === undefined) first = v;
      else if (v !== first) {
        differs = true;
        break;
      }
    }
    if (differs) out.push(k);
  }
  return out;
}

/**
 * Constrói um rótulo curto por série. Precisa de TODAS as séries do gráfico juntas
 * para saber o que varia entre elas.
 *
 * @param series    séries retornadas pela consulta (cada uma com seu mapa de labels)
 * @param fallback  texto quando nada distingue a série (nome da métrica/painel)
 * @param hostLabel resolve o hostname técnico para um nome amigável
 * @param groupBy   dimensão de agrupamento explícita do usuário (tem prioridade)
 */
export function buildSeriesLabels(
  series: { labels: Labels }[],
  fallback: string,
  hostLabel: (h: string) => string,
  groupBy?: string,
): string[] {
  // Agrupamento explícito vence: o rótulo é só aquela dimensão.
  if (groupBy) {
    return series.map((s) => {
      const v = s.labels[groupBy];
      if (v == null || v === "") return fallback;
      return isHostKey(groupBy) ? hostLabel(v) : `${groupBy}=${v}`;
    });
  }

  const varying = varyingKeys(series);
  // Dimensões reais que distinguem (fora host e fora ruído), host primeiro/alfabético.
  const dims = varying
    .filter((k) => !isHostKey(k) && !isNoise(k))
    .sort((a, b) => a.localeCompare(b));

  return series.map((s) => {
    const parts: string[] = [];
    const host = hostOf(s.labels);
    if (host) parts.push(hostLabel(host));
    for (const k of dims) {
      const v = s.labels[k];
      if (v != null && v !== "") parts.push(`${k}=${v}`);
    }
    if (parts.length) return parts.join(" · ");
    // Sem host nem dimensão limpa: cai para o que varia (inclui ruído) só para não
    // colidir duas séries no mesmo rótulo; senão, o texto de fallback.
    const raw = varying
      .filter((k) => s.labels[k] != null && s.labels[k] !== "")
      .map((k) => `${k}=${s.labels[k]}`);
    return raw.join(" · ") || fallback;
  });
}
