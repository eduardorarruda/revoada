// Agentes (ARQUITETURA §7): quem está conectado ao painel, inscrição por token de uso único,
// revogação e a tarefa de diagnóstico acompanhada ao vivo.
import { useCallback, useEffect, useRef, useState } from "react";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { Badge, Button, Card, FormField, PageHeader } from "../components";
import { LastSeen } from "../components/LastSeen";
import { Modal } from "../components/Modal";
import { useToast } from "../components/Toast";
import {
  acompanharTarefa,
  controlarTarefa,
  criarDiagnostico,
  criarTokenAgente,
  isAdmin,
  listarAgentes,
  podeOperar,
  revogarAgente,
  type Agente,
  type EventoTarefa,
  type Tarefa,
  type TokenAgente,
} from "../api";
import { Bando } from "../motion";
import "./agentes.css";

// Nome de cada tipo de tarefa para gente; o código (o que vai no agent.yaml)
// continua no title, para quem precisa copiar.
const NOME_TAREFA: Record<string, string> = {
  "diagnostico.eco": "Teste do canal",
  "migracao.esquema": "Ler estrutura do banco",
  "migracao.simular": "Simular migração",
  "migracao.executar": "Executar migração",
  "migracao.verificar": "Verificar migração",
  "migracao.reverter": "Reverter migração",
  "firebird.diagnostico": "Diagnóstico Firebird",
  "firebird.upgrade": "Upgrade Firebird",
  "firebird.descartar": "Descartar upgrade",
  "deploy.aplicar": "Deploy",
};

const PRESENCA: Record<string, { rotulo: string; estado: "ok" | "warn" | "crit" | "neutral"; explica: string }> = {
  online: { rotulo: "Online", estado: "ok", explica: "Mandou sinal de vida nos últimos 15 segundos." },
  instavel: { rotulo: "Instável", estado: "warn", explica: "Perdeu um ou dois sinais de vida (a cada 10 s)." },
  offline: { rotulo: "Offline", estado: "crit", explica: "Sem sinal há mais de 30 s — o painel já avisou pelos canais de alerta." },
  nunca_conectou: { rotulo: "Aguardando", estado: "neutral", explica: "Inscrito, mas ainda não conectou." },
};

const NOME_SO: Record<string, string> = { linux: "Linux", windows: "Windows", darwin: "macOS" };

export function Agentes() {
  const toast = useToast();
  const [agentes, setAgentes] = useState<Agente[] | null>(null);
  const [erro, setErro] = useState<string | null>(null);
  const [novoToken, setNovoToken] = useState(false);
  const [tarefaAberta, setTarefaAberta] = useState<Tarefa | null>(null);

  const carregar = useCallback(async () => {
    try {
      setAgentes(await listarAgentes());
      setErro(null);
    } catch (e) {
      setErro(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    void carregar();
    const t = setInterval(carregar, 5000); // presença muda a cada batimento (10 s)
    return () => clearInterval(t);
  }, [carregar]);

  const diagnostico = async (a: Agente) => {
    try {
      setTarefaAberta(await criarDiagnostico(a.id, { passos: 8, intervalo_ms: 600, mensagem: "eco do Revoada" }));
    } catch (e) {
      toast.error(e instanceof Error ? e.message.replace(/^\d{3}:\s*/, "") : "Não deu para enviar a tarefa.");
    }
  };

  const revogar = async (a: Agente) => {
    if (!window.confirm(`Revogar o agente de ${a.hostname || a.id}? Ele é desconectado na hora e precisa de um token novo para voltar.`))
      return;
    try {
      await revogarAgente(a.id);
      toast.success("Agente revogado.");
      void carregar();
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Não deu para revogar.");
    }
  };

  const ativos = agentes?.filter((a) => !a.revogado) ?? [];
  const revogados = agentes?.filter((a) => a.revogado) ?? [];

  return (
    <div className="page agentes">
      <PageHeader
        title="Agentes"
        subtitle="Cada servidor com o Revoada instalado. A conexão é sempre aberta pelo agente, com certificado próprio (mTLS) — o servidor não expõe porta nenhuma."
        actions={
          isAdmin() ? (
            <Button variant="primary" onClick={() => setNovoToken(true)}>
              Inscrever servidor
            </Button>
          ) : undefined
        }
      />

      {erro && <p className="agentes__erro">{erro}</p>}

      {agentes && ativos.length === 0 && (
        <Card>
          <div className="agentes__vazio">
            <p className="agentes__vazio-titulo">Nenhum servidor inscrito ainda</p>
            <p>
              Gere um token de inscrição, rode o comando no servidor e ele aparece aqui em segundos. O token vale uma vez
              só e vence em 24 horas.
            </p>
            {isAdmin() && (
              <Button variant="primary" onClick={() => setNovoToken(true)}>
                Inscrever o primeiro servidor
              </Button>
            )}
          </div>
        </Card>
      )}

      <ul className="agentes__grade">
        <AnimatePresence initial={false}>
          {ativos.map((a, i) => (
            <CartaoAgente
              key={a.id}
              agente={a}
              indice={i}
              onDiagnostico={() => diagnostico(a)}
              onRevogar={() => revogar(a)}
            />
          ))}
        </AnimatePresence>
      </ul>

      {revogados.length > 0 && (
        <details className="agentes__revogados">
          <summary>{revogados.length} agente(s) revogado(s)</summary>
          <ul>
            {revogados.map((a) => (
              <li key={a.id}>
                {a.hostname || a.id} — {a.id}
              </li>
            ))}
          </ul>
        </details>
      )}

      {novoToken && <ModalToken onClose={() => setNovoToken(false)} />}
      {tarefaAberta && <TarefaAoVivo tarefa={tarefaAberta} onClose={() => setTarefaAberta(null)} />}
    </div>
  );
}

function CartaoAgente({
  agente: a,
  indice,
  onDiagnostico,
  onRevogar,
}: {
  agente: Agente;
  indice: number;
  onDiagnostico: () => void;
  onRevogar: () => void;
}) {
  const reduzir = useReducedMotion();
  const p = PRESENCA[a.estado] ?? PRESENCA.nunca_conectou;
  return (
    <motion.li
      layout={!reduzir}
      initial={reduzir ? false : { opacity: 0, y: 12 }}
      animate={{ opacity: 1, y: 0 }}
      exit={reduzir ? undefined : { opacity: 0, scale: 0.96 }}
      transition={{ delay: reduzir ? 0 : indice * 0.04, duration: 0.25 }}
      className={`agente agente--${a.estado}`}
    >
      <div className="agente__topo">
        <span className={`agente__pulso agente__pulso--${a.estado}`} aria-hidden="true" />
        <div className="agente__nome">
          <strong>{a.hostname || "servidor sem nome"}</strong>
          <span>{a.rotulo || a.id}</span>
        </div>
        <span title={p.explica}>
          <Badge state={p.estado}>{p.rotulo}</Badge>
        </span>
      </div>
      <dl className="agente__dados">
        <div>
          <dt>Sistema</dt>
          <dd>
            {NOME_SO[a.so] ?? (a.so || "—")} {a.arch}
          </dd>
        </div>
        <div>
          <dt>Versão</dt>
          <dd>{a.versao || "—"}</dd>
        </div>
        <div>
          <dt>Último sinal</dt>
          <dd>
            {/* batimento a cada 10 s: 3 perdidos já é instável */}
            <LastSeen ts={a.visto_em} staleAfterSeconds={30} prefix="sinal" />
          </dd>
        </div>
        <div>
          <dt>Certificado vence</dt>
          <dd>{new Date(a.cert_valido_ate).toLocaleDateString("pt-BR")}</dd>
        </div>
      </dl>
      <div className="agente__caps">
        {a.capacidades.length === 0 ? (
          <span className="agente__caps-vazio">Nenhum tipo de tarefa liberado neste servidor (canal.tarefas_permitidas).</span>
        ) : (
          [...a.capacidades]
            .sort((x, y) => (NOME_TAREFA[x] ?? x).localeCompare(NOME_TAREFA[y] ?? y, "pt-BR"))
            .map((c) => (
              <span key={c} className="agente__cap" title={c}>
                {NOME_TAREFA[c] ?? c}
              </span>
            ))
        )}
      </div>
      {isAdmin() && (
        <div className="agente__acoes">
          <Button onClick={onDiagnostico} disabled={a.estado === "offline" || !a.capacidades.includes("diagnostico.eco")}>
            Testar canal
          </Button>
          <Button variant="ghost" onClick={onRevogar}>
            Revogar
          </Button>
        </div>
      )}
    </motion.li>
  );
}

function ModalToken({ onClose }: { onClose: () => void }) {
  const toast = useToast();
  const [rotulo, setRotulo] = useState("");
  const [token, setToken] = useState<TokenAgente | null>(null);
  const [busy, setBusy] = useState(false);

  const gerar = async () => {
    setBusy(true);
    try {
      setToken(await criarTokenAgente(rotulo.trim()));
    } catch (e) {
      toast.error(e instanceof Error ? e.message : "Não deu para gerar o token.");
    } finally {
      setBusy(false);
    }
  };

  const copiar = (t: string) =>
    navigator.clipboard
      .writeText(t)
      .then(() => toast.success("Copiado."))
      .catch(() => toast.error("Não deu para copiar."));

  return (
    <Modal open onClose={onClose} title="Inscrever um servidor" wide>
      {!token ? (
        <div className="agentes__form">
          <p className="agentes__texto">
            O token vale <strong>uma vez</strong> e vence em <strong>24 horas</strong>. Ele leva a impressão digital do
            certificado deste painel: o agente só aceita conversar com ESTE painel.
          </p>
          <FormField label="Nome para reconhecer o servidor (opcional)">
            <input className="field" value={rotulo} onChange={(e) => setRotulo(e.target.value)} placeholder="ex.: ERP cliente X — produção" />
          </FormField>
          <Button variant="primary" onClick={gerar} disabled={busy}>
            {busy ? "Gerando…" : "Gerar token"}
          </Button>
        </div>
      ) : (
        <div className="agentes__form">
          <p className="agentes__texto">Rode no servidor (com o agente já instalado):</p>
          <ComandoCopiavel rotulo="Linux / macOS" texto={token.comandos.linux_macos} onCopiar={copiar} />
          <ComandoCopiavel rotulo="Windows (PowerShell como administrador)" texto={token.comandos.windows} onCopiar={copiar} />
          <p className="agentes__texto agentes__texto--fraco">
            Depois libere os tipos de tarefa em <code>canal.tarefas_permitidas</code> no <code>agent.yaml</code> (por exemplo{" "}
            <code>diagnostico.eco</code>) e reinicie o serviço. Vence em {new Date(token.expira_em).toLocaleString("pt-BR")}.
          </p>
        </div>
      )}
    </Modal>
  );
}

function ComandoCopiavel({ rotulo, texto, onCopiar }: { rotulo: string; texto: string; onCopiar: (t: string) => void }) {
  return (
    <div className="comando">
      <span className="comando__rotulo">{rotulo}</span>
      <div className="comando__linha">
        <code>{texto}</code>
        <Button onClick={() => onCopiar(texto)}>Copiar</Button>
      </div>
    </div>
  );
}

// TarefaAoVivo acompanha uma tarefa pelo SSE: barra de progresso, linha do tempo dos
// passos e os controles (pausar/retomar/cancelar — ações críticas, pedem 2FA).
export function TarefaAoVivo({ tarefa: inicial, onClose }: { tarefa: Tarefa; onClose: () => void }) {
  const reduzir = useReducedMotion();
  const toast = useToast();
  const [tarefa, setTarefa] = useState<Tarefa>(inicial);
  const [eventos, setEventos] = useState<EventoTarefa[]>([]);
  const fim = useRef<HTMLLIElement>(null);

  useEffect(() => {
    const ctl = new AbortController();
    acompanharTarefa(
      inicial.id,
      (a) => {
        if (a.tarefa) setTarefa(a.tarefa);
        if (a.evento) setEventos((xs) => (xs.some((x) => x.seq === a.evento!.seq) ? xs : [...xs, a.evento!]));
      },
      ctl.signal,
    ).catch(() => undefined);
    return () => ctl.abort();
  }, [inicial.id]);

  useEffect(() => {
    fim.current?.scrollIntoView({ block: "nearest", behavior: reduzir ? "auto" : "smooth" });
  }, [eventos.length, reduzir]);

  const progresso = Math.max(tarefa.progresso, eventos.at(-1)?.progresso ?? 0);
  const terminou = ["sucesso", "falha", "cancelada", "recusada"].includes(tarefa.estado);

  const controlar = async (acao: "pausar" | "retomar" | "cancelar") => {
    try {
      setTarefa(await controlarTarefa(tarefa.id, acao));
    } catch (e) {
      toast.error(e instanceof Error ? e.message.replace(/^\d{3}:\s*/, "") : "Não deu.");
    }
  };

  return (
    <Modal open onClose={onClose} title={`Tarefa ${tarefa.tipo}`} wide>
      <div className="tarefa">
        <div className="tarefa__cabeca">
          <Badge state={tarefa.estado === "sucesso" ? "ok" : terminou ? "crit" : tarefa.estado === "pausada" ? "warn" : "info"}>
            {tarefa.estado.replace("_", " ")}
          </Badge>
          {!terminou && <Bando rotulo={`Tarefa ${tarefa.estado}`} tamanho={56} />}
          <span className="tarefa__id">correlação {tarefa.correlacao_id}</span>
        </div>
        <div className="tarefa__barra" role="progressbar" aria-valuenow={Math.round(progresso)} aria-valuemin={0} aria-valuemax={100}>
          <motion.div
            className={`tarefa__preenchida tarefa__preenchida--${tarefa.estado}`}
            initial={false}
            animate={{ width: `${progresso}%` }}
            transition={reduzir ? { duration: 0 } : { type: "spring", stiffness: 120, damping: 20 }}
          />
          <span className="tarefa__pct">{Math.round(progresso)}%</span>
        </div>
        <ol className="tarefa__linha">
          <AnimatePresence initial={false}>
            {eventos.map((e) => (
              <motion.li
                key={e.seq}
                initial={reduzir ? false : { opacity: 0, x: -8 }}
                animate={{ opacity: 1, x: 0 }}
                className={`tarefa__evento tarefa__evento--${e.nivel}`}
              >
                <time>{new Date(e.em).toLocaleTimeString("pt-BR")}</time>
                <span>{e.mensagem}</span>
              </motion.li>
            ))}
          </AnimatePresence>
          <li ref={fim} aria-hidden="true" />
        </ol>
        {tarefa.erro && <p className="tarefa__erro">{tarefa.erro}</p>}
        {!terminou && podeOperar() && (
          <div className="tarefa__acoes">
            {tarefa.estado === "pausada" ? (
              <Button onClick={() => controlar("retomar")}>Retomar</Button>
            ) : (
              <Button onClick={() => controlar("pausar")}>Pausar</Button>
            )}
            <Button variant="ghost" onClick={() => controlar("cancelar")}>
              Cancelar
            </Button>
          </div>
        )}
      </div>
    </Modal>
  );
}
