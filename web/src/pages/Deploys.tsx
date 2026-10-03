// Deploys (ARQUITETURA §10): histórico de implantações feitas pelos agentes (pela GitHub
// Action ou pela tela), com rollback automático quando o health check falha.
import { useCallback, useEffect, useState } from "react";
import { motion, useReducedMotion } from "motion/react";
import { Eye, Rocket, RotateCcw } from "lucide-react";
import { Badge, Button, Card, FormField, PageHeader } from "../components";
import { Modal } from "../components/Modal";
import { useToast } from "../components/Toast";
import { TarefaAoVivo } from "./Agentes";
import { criarDeploy, listarAgentes, listarDeploys, podeOperar, type Agente, type Tarefa } from "../api";
import "./mcp.css";

type Esp = { aplicacao?: string; versao?: string; ambiente?: string; commit?: string };
type Res = { anterior?: string; revertido?: boolean; saudavel?: boolean };

function msg(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\d{3}:\s*/, "") : "Não deu.";
}

export function Deploys() {
  const toast = useToast();
  const reduzir = useReducedMotion();
  const [itens, setItens] = useState<Tarefa[] | null>(null);
  const [agentes, setAgentes] = useState<Agente[]>([]);
  const [novo, setNovo] = useState(false);
  const [aoVivo, setAoVivo] = useState<Tarefa | null>(null);
  const [carregou, setCarregou] = useState(false);

  const carregar = useCallback(async () => {
    try {
      const [ds, ags] = await Promise.all([listarDeploys(), listarAgentes()]);
      setItens(ds);
      setAgentes(ags.filter((a) => !a.revogado && a.capacidades?.includes("deploy.aplicar")));
      setCarregou(true);
    } catch (e) {
      toast.error(msg(e));
    }
  }, [toast]);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  const nomeAgente = (id: string) => agentes.find((a) => a.id === id)?.hostname ?? id;

  return (
    <div className="page mcp">
      <PageHeader
        title="Deploys"
        subtitle="O agente do servidor implanta (compose ou script liberado no agent.yaml), confere o health check e volta sozinho para a versão anterior se falhar. Cada deploy vira anotação nos gráficos."
        actions={
          podeOperar() && (
            <Button variant="primary" onClick={() => setNovo(true)} disabled={agentes.length === 0}>
              <Rocket size={16} aria-hidden={true} /> Novo deploy
            </Button>
          )
        }
      />
      {carregou && agentes.length === 0 && (
        // Botão desabilitado sem dizer por quê é beco sem saída: diz o que falta.
        <Card title="Nenhum servidor pronto para deploy">
          <p className="mcp__vazio">
            Para liberar, no <code>agent.yaml</code> do servidor: inclua <code>deploy.aplicar</code> em{" "}
            <code>canal.tarefas_permitidas</code>, descreva a aplicação em <code>canal.deploy</code> (compose ou
            script, pasta e health check) e reinicie o agente. O painel só manda o nome e a versão, nunca um comando.
          </p>
        </Card>
      )}
      <Card title="Histórico">
        {itens?.length === 0 && (
          <p className="mcp__vazio">
            Nenhum deploy ainda. Use a Action <code>eduardorarruda/revoada-deploy-action</code> no GitHub ou o botão acima.
          </p>
        )}
        <ul className="mcp__lista">
          {itens?.map((t, i) => {
            const e = (t.especificacao ?? {}) as Esp;
            const r = (t.resumo ?? {}) as Res;
            return (
              <motion.li key={t.id} initial={reduzir ? false : { opacity: 0, x: -8 }} animate={{ opacity: 1, x: 0 }} transition={{ delay: reduzir ? 0 : Math.min(i, 10) * 0.03 }}>
                <Badge state={t.estado === "sucesso" ? "ok" : t.estado === "falha" ? "crit" : "info"}>{t.estado.replace("_", " ")}</Badge>
                <strong>
                  {e.aplicacao} <code>{e.versao}</code>
                </strong>
                {r.revertido && (
                  <span className="mm-chip mm-chip--aviso">
                    <RotateCcw size={12} aria-hidden={true} /> revertido para {r.anterior}
                  </span>
                )}
                <span className="mcp__meta">
                  {nomeAgente(t.agente_id)} · {e.ambiente || "sem ambiente"} · {t.origem === "github_action" ? "GitHub Action" : "tela"} · {t.iniciada_por} ·{" "}
                  {new Date(t.criada_em).toLocaleString("pt-BR")}
                </span>
                <Button variant="ghost" onClick={() => setAoVivo(t)} aria-label="Acompanhar deploy">
                  <Eye size={16} aria-hidden={true} />
                </Button>
              </motion.li>
            );
          })}
        </ul>
      </Card>
      {novo && (
        <NovoDeploy
          agentes={agentes}
          onClose={() => setNovo(false)}
          onCriado={(t) => {
            setNovo(false);
            setAoVivo(t);
            void carregar();
          }}
        />
      )}
      {aoVivo && (
        <TarefaAoVivo
          tarefa={aoVivo}
          onClose={() => {
            setAoVivo(null);
            void carregar();
          }}
        />
      )}
    </div>
  );
}

function NovoDeploy({ agentes, onClose, onCriado }: { agentes: Agente[]; onClose: () => void; onCriado: (t: Tarefa) => void }) {
  const toast = useToast();
  const [f, setF] = useState({ agente: agentes[0]?.id ?? "", aplicacao: "", versao: "", ambiente: "" });
  const [busy, setBusy] = useState(false);
  const enviar = async () => {
    setBusy(true);
    try {
      onCriado(await criarDeploy({ ...f, ambiente: f.ambiente || undefined }));
    } catch (e) {
      toast.error(msg(e));
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      open
      onClose={onClose}
      title="Novo deploy"
      footer={
        <div className="row">
          <Button onClick={onClose}>Cancelar</Button>
          <Button variant="primary" onClick={enviar} disabled={busy || !f.aplicacao || !f.versao}>
            Implantar
          </Button>
        </div>
      }
    >
      <div className="mcp__form">
        <FormField label="Servidor">
          <select className="field" value={f.agente} onChange={(e) => setF({ ...f, agente: e.target.value })}>
            {agentes.map((a) => (
              <option key={a.id} value={a.id}>
                {a.hostname || a.id}
              </option>
            ))}
          </select>
        </FormField>
        <FormField label="Aplicação (como está em canal.deploy no agent.yaml)">
          <input className="field" value={f.aplicacao} onChange={(e) => setF({ ...f, aplicacao: e.target.value })} placeholder="loja" />
        </FormField>
        <FormField label="Versão">
          <input className="field" value={f.versao} onChange={(e) => setF({ ...f, versao: e.target.value })} placeholder="v1.4.2" />
        </FormField>
        <FormField label="Ambiente (opcional)">
          <input className="field" value={f.ambiente} onChange={(e) => setF({ ...f, ambiente: e.target.value })} placeholder="producao" />
        </FormField>
      </div>
    </Modal>
  );
}
