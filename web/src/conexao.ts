// Estado real da conexão com o painel.
//
// Por que existe: o rodapé do menu exibia `<FreshnessIndicator status="live" />`
// — uma string fixa no código. O selo dizia "ao vivo" com o servidor fora do ar,
// com o Wi-Fi caído e com o deploy no meio da troca. Um indicador que só sabe
// dizer "está tudo bem" não é um indicador; é um enfeite, e ainda por cima um que
// mente justamente na hora em que alguém olha para ele.
//
// A fonte da verdade é o próprio tráfego da app: toda chamada autenticada que
// passa por `api()` avisa aqui se voltou ou não. Sem sonda extra, sem
// polling próprio — se a interface está conseguindo falar com o painel, está no
// ar; se a última tentativa falhou, está reconectando.
//
// O que NÃO entra aqui: 401 (sessão expirada) e 4xx de regra de negócio. Esses
// significam "o servidor respondeu, e respondeu não" — a conexão está ótima.
// Só falha de rede e 5xx contam como conexão perdida.

export type EstadoConexao = "no-ar" | "reconectando";

let estado: EstadoConexao = "no-ar";
// Quando a última chamada VOLTOU bem. Alimenta o "· há Xs" do selo.
let ultimoSucesso: number | null = null;

type Ouvinte = (e: EstadoConexao) => void;
const ouvintes = new Set<Ouvinte>();

function publicar(novo: EstadoConexao) {
  if (novo === estado) return;
  estado = novo;
  for (const cb of ouvintes) cb(novo);
}

/** Registrado por `api()` quando uma chamada autenticada volta com resposta. */
export function marcarSucesso() {
  ultimoSucesso = Date.now();
  publicar("no-ar");
}

/**
 * Registrado por `api()` quando a chamada não chegou ao servidor (falha de rede)
 * ou voltou 5xx. `status` ausente = nem chegou a haver resposta HTTP.
 */
export function marcarFalha(status?: number) {
  // Resposta do servidor que não seja 5xx é servidor vivo dizendo "não".
  if (status != null && status < 500) return;
  publicar("reconectando");
}

/** Sem nenhuma resposta há este tanto (s), o rodapé sonda o painel sozinho. */
export const IDADE_PARA_SONDAR = 30;

export function estadoConexao(): EstadoConexao {
  return estado;
}

/** Segundos desde a última resposta boa, ou null se ainda não houve nenhuma. */
export function idadeUltimoSucesso(agora: number = Date.now()): number | null {
  if (ultimoSucesso == null) return null;
  return Math.max(0, Math.round((agora - ultimoSucesso) / 1000));
}

export function ouvirConexao(cb: Ouvinte): () => void {
  ouvintes.add(cb);
  return () => {
    ouvintes.delete(cb);
  };
}

/** Só para teste: devolve o módulo ao estado inicial. */
export function resetarConexaoParaTeste() {
  estado = "no-ar";
  ultimoSucesso = null;
  ouvintes.clear();
}
