// MCP (ARQUITETURA §12): tokens com escopo para agentes de IA (Claude Desktop/Code, IDEs).
// O token aparece UMA vez; depois só o nome, os escopos e o último uso.
import { useCallback, useEffect, useMemo, useState } from "react";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { Bot, Copy, KeyRound, ShieldCheck } from "lucide-react";
import { Badge, Button, Card, FormField, PageHeader } from "../components";
import { Modal } from "../components/Modal";
import { useToast } from "../components/Toast";
import {
  criarTokenMCP,
  listarTokensMCP,
  revogarTokenMCP,
  type EscopoMCP,
  type TokenMCP,
} from "../api";
import "./mcp.css";

const ESCOPOS: { id: EscopoMCP; titulo: string; texto: string }[] = [
  {
    id: "leitura",
    titulo: "Leitura",
    texto:
      "servidores, alertas, agentes, conexões (sem senha), schemas, mapeamentos e status",
  },
  {
    id: "mapeamento",
    titulo: "Mapeamento",
    texto: "ler schema de novo e gravar RASCUNHO de mapeamento (nunca aprova)",
  },
  {
    id: "simulacao",
    titulo: "Simulação",
    texto: "pedir dry-run (lê e confere tudo, não grava nada)",
  },
];

function erro(e: unknown) {
  return e instanceof Error ? e.message.replace(/^\d{3}:\s*/, "") : "Não deu.";
}

export function Mcp() {
  const toast = useToast();
  const reduzir = useReducedMotion();
  const [tokens, setTokens] = useState<TokenMCP[] | null>(null);
  const [novo, setNovo] = useState(false);
  const [criado, setCriado] = useState<{ token: string; nome: string } | null>(
    null,
  );

  const carregar = useCallback(async () => {
    try {
      setTokens(await listarTokensMCP());
    } catch (e) {
      toast.error(erro(e));
    }
  }, [toast]);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  const revogar = async (t: TokenMCP) => {
    try {
      await revogarTokenMCP(t.id);
      toast.success(
        `Token “${t.nome}” revogado: a próxima chamada dele já é recusada.`,
      );
      await carregar();
    } catch (e) {
      toast.error(erro(e));
    }
  };

  return (
    <div className="page mcp">
      <PageHeader
        title="MCP — agentes de IA"
        subtitle="Dê a um agente de IA acesso ao Revoada com escopo: ele lê estrutura e estado, ajuda no mapeamento e pede dry-run. Executar, aprovar, reverter e ver credencial continuam só com pessoas."
        actions={
          <Button variant="primary" onClick={() => setNovo(true)}>
            <KeyRound size={16} aria-hidden={true} /> Novo token
          </Button>
        }
      />
      <div className="mcp__garantias">
        {[
          "Nunca recebe linha de dado nem senha",
          "Cada chamada fica na auditoria (origem MCP)",
          "120 chamadas por minuto por token",
        ].map((g) => (
          <span key={g}>
            <ShieldCheck size={14} aria-hidden={true} /> {g}
          </span>
        ))}
      </div>

      <Card title="Tokens">
        {tokens?.length === 0 && (
          <p className="mcp__vazio">
            Nenhum token ainda. Crie um para conectar o Claude Desktop, o Claude
            Code ou sua IDE.
          </p>
        )}
        <ul className="mcp__lista">
          <AnimatePresence initial={false}>
            {tokens?.map((t) => {
              const vencido = new Date(t.expira_em) < new Date();
              return (
                <motion.li
                  key={t.id}
                  layout={!reduzir}
                  initial={reduzir ? false : { opacity: 0, y: -6 }}
                  animate={{ opacity: 1, y: 0 }}
                >
                  <Bot size={18} aria-hidden={true} />
                  <strong>{t.nome}</strong>
                  <span className="mcp__escopos">
                    {t.escopos.map((e) => (
                      <code key={e}>{e}</code>
                    ))}
                  </span>
                  <span className="mcp__meta">
                    {t.ultimo_uso
                      ? `usado ${new Date(t.ultimo_uso).toLocaleString("pt-BR")}`
                      : "nunca usado"}{" "}
                    · vence {new Date(t.expira_em).toLocaleDateString("pt-BR")}{" "}
                    · por {t.criado_por || "—"}
                  </span>
                  {t.revogado ? (
                    <Badge state="neutral">revogado</Badge>
                  ) : vencido ? (
                    <Badge state="warn">vencido</Badge>
                  ) : (
                    <Badge state="ok">ativo</Badge>
                  )}
                  {!t.revogado && !vencido && (
                    <Button variant="ghost" onClick={() => revogar(t)}>
                      Revogar
                    </Button>
                  )}
                </motion.li>
              );
            })}
          </AnimatePresence>
        </ul>
      </Card>

      {novo && (
        <ModalNovo
          onClose={() => setNovo(false)}
          onCriado={(c) => {
            setNovo(false);
            setCriado(c);
            void carregar();
          }}
        />
      )}
      {criado && <ModalCriado {...criado} onClose={() => setCriado(null)} />}
    </div>
  );
}

function ModalNovo({
  onClose,
  onCriado,
}: {
  onClose: () => void;
  onCriado: (c: { token: string; nome: string }) => void;
}) {
  const toast = useToast();
  const [nome, setNome] = useState("");
  const [escopos, setEscopos] = useState<EscopoMCP[]>(["leitura"]);
  const [dias, setDias] = useState(90);
  const [busy, setBusy] = useState(false);
  const alternar = (e: EscopoMCP) =>
    setEscopos((xs) =>
      e === "leitura"
        ? xs
        : xs.includes(e)
          ? xs.filter((x) => x !== e)
          : [...xs, e],
    );

  const criar = async () => {
    setBusy(true);
    try {
      const r = await criarTokenMCP(nome, escopos, dias);
      onCriado({ token: r.token, nome: r.nome });
    } catch (e) {
      toast.error(erro(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title="Novo token MCP"
      footer={
        <div className="row">
          <Button onClick={onClose}>Cancelar</Button>
          <Button
            variant="primary"
            onClick={criar}
            disabled={busy || !nome.trim()}
          >
            Criar token
          </Button>
        </div>
      }
    >
      <div className="mcp__form">
        <FormField label="Nome (quem vai usar)">
          <input
            className="field"
            value={nome}
            onChange={(e) => setNome(e.target.value)}
            placeholder="Claude Desktop da Ana"
          />
        </FormField>
        <fieldset className="mcp__escolhas">
          <legend>Escopos</legend>
          {ESCOPOS.map((e) => (
            <label key={e.id} className={escopos.includes(e.id) ? "is-on" : ""}>
              <input
                type="checkbox"
                checked={escopos.includes(e.id)}
                disabled={e.id === "leitura"}
                onChange={() => alternar(e.id)}
              />
              <span>
                <strong>{e.titulo}</strong> — {e.texto}
              </span>
            </label>
          ))}
        </fieldset>
        <FormField label="Validade (dias, até 365)">
          <input
            className="field"
            type="number"
            min={1}
            max={365}
            value={dias}
            onChange={(e) => setDias(Number(e.target.value))}
          />
        </FormField>
      </div>
    </Modal>
  );
}

function ModalCriado({
  token,
  nome,
  onClose,
}: {
  token: string;
  nome: string;
  onClose: () => void;
}) {
  const toast = useToast();
  const url = `${window.location.origin}/mcp`;
  const configs = useMemo(
    () => ({
      "Claude Code / HTTP": JSON.stringify(
        {
          mcpServers: {
            revoada: {
              type: "http",
              url,
              headers: { Authorization: `Bearer ${token}` },
            },
          },
        },
        null,
        2,
      ),
      "stdio (Claude Desktop)": JSON.stringify(
        {
          mcpServers: {
            revoada: {
              command: "revoada-painel",
              args: ["mcp"],
              env: {
                REVOADA_URL: window.location.origin,
                REVOADA_MCP_TOKEN: token,
              },
            },
          },
        },
        null,
        2,
      ),
    }),
    [token, url],
  );
  const copiar = async (t: string) => {
    try {
      await navigator.clipboard.writeText(t);
      toast.success("Copiado.");
    } catch {
      toast.error("Não deu para copiar; selecione e copie à mão.");
    }
  };
  return (
    <Modal open onClose={onClose} title={`Token “${nome}” criado`} wide>
      <div className="mcp__criado">
        <p className="mcp__aviso">
          Copie agora: o token não aparece de novo (o painel guarda só o hash).
        </p>
        <div className="mcp__token">
          <code>{token}</code>
          <Button onClick={() => copiar(token)}>
            <Copy size={16} aria-hidden={true} /> Copiar
          </Button>
        </div>
        {Object.entries(configs).map(([titulo, cfg]) => (
          <div key={titulo} className="mcp__config">
            <div className="mcp__config-topo">
              <strong>{titulo}</strong>
              <Button variant="ghost" onClick={() => copiar(cfg)}>
                <Copy size={14} aria-hidden={true} /> Copiar
              </Button>
            </div>
            <pre>{cfg}</pre>
          </div>
        ))}
      </div>
    </Modal>
  );
}
