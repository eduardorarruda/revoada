// Utilitários puros para consumir um fluxo SSE (text/event-stream) recebido via
// fetch + ReadableStream — como não dá para fazer POST com EventSource, lemos o
// corpo em streaming e cortamos os eventos na mão. Isolados aqui para poderem ser
// testados sem rede (ver sse.test.ts).

// Extrai o payload JSON de um bloco de evento SSE (uma ou mais linhas). Junta as
// linhas `data:` conforme a spec do SSE; devolve null quando não há dado útil
// (comentários `:` de heartbeat, linhas vazias ou o marcador `[DONE]`).
export function parseSSEBlock<T>(block: string): T | null {
  const dataLines = block
    .split("\n")
    .map((l) => l.replace(/\r$/, ""))
    .filter((l) => l.startsWith("data:"))
    .map((l) => l.slice(5).replace(/^ /, ""));
  if (dataLines.length === 0) return null;
  const data = dataLines.join("\n").trim();
  if (!data || data === "[DONE]") return null;
  try {
    return JSON.parse(data) as T;
  } catch {
    // Bloco malformado/parcial: ignoramos em vez de derrubar o stream inteiro.
    return null;
  }
}

// Consome mais um pedaço de texto do stream: concatena com o que sobrou, corta
// todos os eventos completos (separados por linha em branco) e devolve os eventos
// achados mais o resto ainda incompleto. Puro: não guarda estado próprio.
export function drainSSE<T>(pending: string, chunk: string): { events: T[]; rest: string } {
  let buf = (pending + chunk).replace(/\r\n/g, "\n");
  const events: T[] = [];
  for (;;) {
    const sep = buf.indexOf("\n\n");
    if (sep === -1) break;
    const raw = buf.slice(0, sep);
    buf = buf.slice(sep + 2);
    const payload = parseSSEBlock<T>(raw);
    if (payload !== null) events.push(payload);
  }
  return { events, rest: buf };
}
