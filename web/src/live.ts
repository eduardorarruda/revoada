// Cliente de tempo real (WebSocket) para atualização de painéis.
import { getAccessToken, type QueryResponse } from "./api";
import { marcarSucesso } from "./conexao";

export type LiveStatus = "live" | "reconnecting";
export interface Sub {
  panelId: number;
  metric: string;
  filters?: Record<string, string>;
  // group_by: chaves de label para colapsar as séries (ex.: ["host"]). Propagado ao
  // backend para o painel multi-host atualizar em tempo real com a mesma granularidade.
  group_by?: string[];
  agg?: string;
  step?: number;
  window?: number;
}

type UpdateCb = (data: QueryResponse, at: number) => void;
type StatusCb = (s: LiveStatus) => void;

// Mesma leitura de visibilidade usada por hooks/usePolling.ts — um único conceito de
// "ninguém está olhando" no produto inteiro.
function abaEscondida(): boolean {
  return typeof document !== "undefined" && document.visibilityState === "hidden";
}

export class LiveClient {
  private ws: WebSocket | null = null;
  private subs: Sub[] = [];
  private updateCbs = new Map<number, Set<UpdateCb>>();
  private statusCbs = new Set<StatusCb>();
  private backoff = 500;
  private closed = false;
  private retry: ReturnType<typeof setTimeout> | null = null;
  private ouvindoVisibilidade = false;

  // Pausa por visibilidade: com a aba escondida NÃO existe assinatura viva.
  //
  // Do outro lado deste socket o servidor RECONSULTA cada painel assinado a cada 1
  // segundo. Uma aba de dashboard esquecida em segundo plano sustentava 420
  // consultas/min (7 painéis) no ClickHouse 24 h por dia — medido em 7,4% de um
  // núcleo por aba. `usePolling` já respeitava a regra; o caminho MAIS CARO do
  // produto era justamente o que a ignorava.
  //
  // Ao voltar à aba, reconecta e re-assina: a primeira mensagem do servidor repõe a
  // série inteira da janela, então nada de dado se perde no intervalo pausado.
  private readonly aoTrocarVisibilidade = () => {
    if (this.closed) return;
    if (abaEscondida()) this.pausar();
    else this.retomar();
  };

  connect() {
    this.closed = false; // permite reconectar após um close() (ex.: StrictMode remonta)
    if (!this.ouvindoVisibilidade && typeof document !== "undefined") {
      document.addEventListener("visibilitychange", this.aoTrocarVisibilidade);
      this.ouvindoVisibilidade = true;
    }
    this.abrir();
  }

  private abrir() {
    if (this.closed || this.ws) return;
    if (abaEscondida()) {
      // Ninguém está olhando: fica desconectado de propósito, sem consumir servidor.
      this.emitStatus("reconnecting");
      return;
    }
    const token = getAccessToken();
    if (!token) return;
    const proto = location.protocol === "https:" ? "wss" : "ws";
    const ws = new WebSocket(`${proto}://${location.host}/api/live?access=${encodeURIComponent(token)}`);
    this.ws = ws;

    ws.onopen = () => {
      this.backoff = 500;
      this.emitStatus("live");
      this.sendSubscribe();
    };
    ws.onmessage = (ev) => {
      try {
        const msg = JSON.parse(ev.data);
        marcarSucesso(); // dado ao vivo chegando = painel respondendo
        if (msg.type === "update") {
          const cbs = this.updateCbs.get(msg.panelId);
          cbs?.forEach((cb) => cb(msg.data, msg.at));
        }
      } catch {
        /* ignore */
      }
    };
    ws.onclose = () => {
      if (this.closed || this.ws !== ws) return; // pausa/close já tratou este socket
      this.ws = null;
      this.emitStatus("reconnecting");
      this.retry = setTimeout(() => this.abrir(), this.backoff);
      this.backoff = Math.min(this.backoff * 2, 10000);
    };
    ws.onerror = () => ws.close();
  }

  // pausar: derruba o socket sem agendar reconexão (a volta é disparada pela
  // visibilidade, não pelo backoff).
  private pausar() {
    this.cancelarRetry();
    this.descartarSocket();
    this.emitStatus("reconnecting");
  }

  private retomar() {
    if (this.ws) return;
    this.backoff = 500; // a aba voltou: reconecta na hora, sem herdar o backoff
    this.abrir();
  }

  private cancelarRetry() {
    if (this.retry !== null) {
      clearTimeout(this.retry);
      this.retry = null;
    }
  }

  // Solta o socket atual sem deixar o handler de close reagendar nada.
  private descartarSocket() {
    const ws = this.ws;
    this.ws = null;
    if (!ws) return;
    ws.onclose = null;
    ws.onerror = null;
    ws.close();
  }

  close() {
    this.closed = true;
    this.cancelarRetry();
    if (this.ouvindoVisibilidade && typeof document !== "undefined") {
      document.removeEventListener("visibilitychange", this.aoTrocarVisibilidade);
      this.ouvindoVisibilidade = false;
    }
    this.descartarSocket();
  }

  setSubscriptions(subs: Sub[]) {
    this.subs = subs;
    this.sendSubscribe();
  }

  private sendSubscribe() {
    if (this.ws?.readyState === WebSocket.OPEN) {
      this.ws.send(JSON.stringify({ type: "subscribe", panels: this.subs }));
    }
  }

  onUpdate(panelId: number, cb: UpdateCb): () => void {
    let set = this.updateCbs.get(panelId);
    if (!set) {
      set = new Set();
      this.updateCbs.set(panelId, set);
    }
    set.add(cb);
    return () => set!.delete(cb);
  }

  onStatus(cb: StatusCb): () => void {
    this.statusCbs.add(cb);
    return () => this.statusCbs.delete(cb);
  }

  private emitStatus(s: LiveStatus) {
    this.statusCbs.forEach((cb) => cb(s));
  }
}
