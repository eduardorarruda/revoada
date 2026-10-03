// Qual série de CPU um GRÁFICO deve pedir para um servidor.
//
// # O problema
//
// A partir do agente 0.8.1 a CPU virou duas séries disjuntas: `system.cpu.utilization`
// é só o que a máquina consumiu, e `system.cpu.steal` é o que o hipervisor negou. O
// número que o painel destaca é a SOMA (`system.cpu.utilization.total`), porque é ela
// que responde "esta máquina está sob pressão?" — medido no srv-02, 3,8% de consumo
// com 12,7% de roubo: quem olhasse só o consumo veria "ocioso" e estaria enganado.
//
// Só que agentes anteriores à 0.8.1 NÃO publicam `.total`. Neles, `utilization` já é a
// soma (o steal estava dentro). Os dois nomes carregam o mesmo número; o que muda é
// qual deles existe.
//
// Nas telas agregadas (mural, cartão do servidor) o servidor resolve isso lendo as
// duas e preferindo a que existir. Um GRÁFICO não tem essa saída: ele pede UMA métrica
// ao /api/query, e pedir a errada desenha um painel vazio. Por isso a escolha aqui é
// pela versão do agente, que o inventário já informa.
//
// Versão ausente ou ilegível cai no legado de propósito: é o que a frota inteira tinha
// antes desta mudança, então o pior caso é o comportamento anterior — nunca um gráfico
// em branco.

const SERIE_SOMA = "system.cpu.utilization.total";
const SERIE_LEGADA = "system.cpu.utilization";

/** Compara com 0.8.1 sem depender de biblioteca de semver: as versões do agente são
 *  MAIOR.MENOR.PATCH numéricas. Qualquer coisa fora desse formato é "não sei". */
function publicaSoma(versao?: string | null): boolean {
  const partes = (versao ?? "").trim().replace(/^v/, "").split(".");
  if (partes.length !== 3) return false;
  const [ma, me, pa] = partes.map((p) => Number(p));
  if (![ma, me, pa].every((n) => Number.isInteger(n) && n >= 0)) return false;
  if (ma !== 0) return ma > 0; // 1.x.x e acima publicam
  if (me !== 8) return me > 8;
  return pa >= 1;
}

/** cpuSerieDoHost devolve o nome da métrica de CPU a pedir para este servidor. */
export function cpuSerieDoHost(agentVersion?: string | null): string {
  return publicaSoma(agentVersion) ? SERIE_SOMA : SERIE_LEGADA;
}
