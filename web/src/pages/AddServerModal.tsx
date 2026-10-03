// Onboarding por SSH: o painel conecta na máquina, gera a chave de ingestão,
// instala o agente e sobe o serviço, mostrando cada passo ao vivo (SSE). É um dos
// caminhos do fluxo "Adicionar servidor" (ver pages/instalar/) — o que dispensa
// tocar no servidor, ao custo de exigir Linux com systemd e credencial SSH. A
// credencial é usada para instalar e fica guardada cifrada num cofre, para
// reprovisionar/atualizar depois sem redigitar.
import { useEffect, useRef, useState } from "react";
import { Lock } from "lucide-react";
import { Button, FormField, Modal } from "../components";
import { help } from "../help";
import {
  startProvision,
  updateProvisionTarget,
  type ProvisionAuthType,
  type ProvisionStartBody,
  type ProvisionStep,
  type ProvisionStepName,
  type ProvisionWireStepName,
  type ProvisionTarget,
} from "../api";

function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

// Passos conhecidos, na ordem em que o servidor os executa. A UI já os desenha
// como "aguardando" e vai marcando ok/erro conforme os eventos chegam.
const STEPS: { key: ProvisionStepName; label: string; running: string }[] = [
  { key: "ssh", label: "Conectar via SSH", running: "Conectando ao servidor…" },
  { key: "serverkey", label: "Gerar a chave do servidor", running: "Gerando a chave de ingestão…" },
  { key: "install", label: "Instalar o agente", running: "Instalando o agente…" },
  { key: "service", label: "Ativar o serviço", running: "Subindo o serviço do agente…" },
];

// Inclui os passos auxiliares que o servidor pode emitir além dos 4 desenhados
// ("persist" = guardar credencial no cofre; "done" = evento de fechamento), para
// nunca renderizar "undefined:" num erro vindo deles.
const STEP_LABEL: Record<string, string> = {
  ssh: "Conectar via SSH",
  serverkey: "Gerar a chave do servidor",
  install: "Instalar o agente",
  service: "Ativar o serviço",
  persist: "Guardar credencial no cofre",
  done: "Conclusão",
};

const MUTED_12 = { fontSize: "var(--fs-12)", color: "var(--text-3)" } as const;

type Phase = "running" | "done" | "failed";

// Ícone de estado de um passo — cor semântica só para estado (ok/erro), pendente
// e "em andamento" ficam neutros. Não depende só da cor: usa também o glifo.
function StepIcon({ state }: { state: "waiting" | "running" | "ok" | "erro" }) {
  const map = {
    waiting: { ch: "○", color: "var(--text-3)" },
    running: { ch: "◔", color: "var(--text-2)" },
    ok: { ch: "✓", color: "var(--ok)" },
    erro: { ch: "✕", color: "var(--crit)" },
  } as const;
  const { ch, color } = map[state];
  return (
    <span
      aria-hidden
      style={{ color, fontWeight: 700, width: 18, textAlign: "center", flexShrink: 0 }}
    >
      {ch}
    </span>
  );
}

// Tela de progresso passo a passo, reaproveitada pelo fluxo de adicionar e pelo de
// atualizar agente. Recebe a função que dispara o SSE (`run`) e chama onStep a cada
// evento; ao fim decide sucesso (serviço ativo) ou erro (passo que falhou).
function ProvisionProgress({
  run,
  successTitle,
  onFinish,
}: {
  run: (onStep: (s: ProvisionStep) => void, signal: AbortSignal) => Promise<void>;
  successTitle: string;
  onFinish: (ok: boolean) => void;
}) {
  const [byStep, setByStep] = useState<Map<ProvisionWireStepName, ProvisionStep>>(new Map());
  const [phase, setPhase] = useState<Phase>("running");
  const [failMsg, setFailMsg] = useState<string | null>(null);
  const [warnMsg, setWarnMsg] = useState<string | null>(null);
  const seen = useRef<Map<ProvisionWireStepName, ProvisionStep>>(new Map());
  const started = useRef(false);
  // `run`/`onFinish` são recriados a cada render (arrow inline no pai). Guardamos as
  // versões mais recentes em refs para disparar o SSE UMA vez só, sem que o efeito
  // reinicie (e cancele o stream) a cada re-render nem duplique o provisionamento no
  // StrictMode. Por isso o efeito não os lista como dependências.
  const runRef = useRef(run);
  const finishRef = useRef(onFinish);
  runRef.current = run;
  finishRef.current = onFinish;

  // Cancela o stream (e, via cancel do contexto no servidor, o passo em andamento)
  // se o componente desmontar — ex.: admin fecha o modal no meio. Sem isto o
  // provisionamento seguiria invisível e um reabrir dispararia um segundo concorrente.
  const abort = useRef<AbortController | null>(null);
  useEffect(() => () => abort.current?.abort(), []);

  useEffect(() => {
    if (started.current) return; // roda uma única vez (StrictMode invoca o efeito 2x em dev)
    started.current = true;
    const ac = new AbortController();
    abort.current = ac;

    runRef.current((s) => {
      seen.current.set(s.step, s);
      setByStep(new Map(seen.current));
    }, ac.signal)
      .then(() => {
        if (ac.signal.aborted) return; // desmontado no meio: nada a mostrar
        const steps = seen.current;
        // "done" é só o evento de fechamento (espelha o resultado); não é um passo.
        const errored = [...steps.values()].find((s) => s.status === "erro" && s.step !== "done");
        // Falha SÓ no cofre com o serviço ativo = o servidor FOI adicionado; avisa
        // que a credencial não ficou guardada (o "Atualizar agente" vai pedir de novo).
        if (errored?.step === "persist" && steps.get("service")?.status === "ok") {
          setPhase("done");
          setWarnMsg(
            "O agente foi instalado e está ativo, mas a credencial não pôde ser guardada no cofre, para usar o “Atualizar agente” depois, será preciso informá-la de novo.",
          );
          finishRef.current(true);
        } else if (errored) {
          setPhase("failed");
          setFailMsg(`${STEP_LABEL[errored.step] ?? errored.step}: ${errored.detail}`);
          finishRef.current(false);
        } else if (steps.get("service")?.status === "ok") {
          setPhase("done");
          finishRef.current(true);
        } else {
          setPhase("failed");
          setFailMsg("A instalação terminou sem confirmar o serviço ativo. Verifique o servidor e tente de novo.");
          finishRef.current(false);
        }
      })
      .catch((e) => {
        if (ac.signal.aborted) return; // abortado pelo unmount: não é falha real
        setPhase("failed");
        setFailMsg(errMsg(e));
        finishRef.current(false);
      });
  }, []);

  // Estado visual de cada passo: o primeiro sem resultado, enquanto roda, é "em
  // andamento"; os seguintes ficam "aguardando".
  const firstPendingIdx = STEPS.findIndex((st) => !byStep.has(st.key));

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-3)" }}>
      <ol style={{ listStyle: "none", padding: 0, margin: 0, display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
        {STEPS.map((st, i) => {
          const ev = byStep.get(st.key);
          const state: "waiting" | "running" | "ok" | "erro" = ev
            ? ev.status === "ok"
              ? "ok"
              : "erro"
            : phase === "running" && i === firstPendingIdx
              ? "running"
              : "waiting";
          const detail = ev?.detail ?? (state === "running" ? st.running : "");
          return (
            <li key={st.key} style={{ display: "flex", gap: "var(--sp-2)", alignItems: "flex-start" }}>
              <StepIcon state={state} />
              <div style={{ minWidth: 0 }}>
                <div
                  style={{
                    fontSize: "var(--fs-14)",
                    color: state === "waiting" ? "var(--text-3)" : "var(--text-1)",
                    fontWeight: state === "erro" || state === "ok" ? 600 : 400,
                  }}
                >
                  {st.label}
                </div>
                {detail && (
                  <div
                    style={{
                      fontSize: "var(--fs-12)",
                      color: state === "erro" ? "var(--crit)" : "var(--text-3)",
                      wordBreak: "break-word",
                    }}
                  >
                    {detail}
                  </div>
                )}
              </div>
            </li>
          );
        })}
      </ol>

      {phase === "done" && (
        <div
          role="status"
          style={{
            border: "1px solid var(--ok)",
            borderRadius: "var(--radius-sm)",
            padding: "var(--sp-3)",
            color: "var(--text-1)",
            fontSize: "var(--fs-14)",
          }}
        >
          <strong style={{ color: "var(--ok)" }}>✓ {successTitle}</strong>
          <div style={{ ...MUTED_12, marginTop: "var(--sp-1)" }}>
            O host passa a reportar métricas em instantes e aparece na lista de Infraestrutura.
          </div>
          {warnMsg && (
            <div style={{ marginTop: "var(--sp-2)", fontSize: "var(--fs-12)", color: "var(--warn)" }}>
              ▲ {warnMsg}
            </div>
          )}
        </div>
      )}

      {phase === "failed" && (
        <div
          role="alert"
          style={{
            border: "1px solid var(--crit)",
            borderRadius: "var(--radius-sm)",
            padding: "var(--sp-3)",
            color: "var(--text-1)",
            fontSize: "var(--fs-14)",
          }}
        >
          <strong style={{ color: "var(--crit)" }}>Não foi possível concluir</strong>
          <div style={{ marginTop: "var(--sp-1)", wordBreak: "break-word" }}>{failMsg}</div>
        </div>
      )}
    </div>
  );
}

// Aviso de segurança fixo do formulário — a app recebe credencial sensível.
function SecurityNote() {
  return (
    <div
      style={{
        border: "1px solid var(--border)",
        background: "var(--bg-1)",
        borderRadius: "var(--radius-sm)",
        padding: "var(--sp-3)",
        fontSize: "var(--fs-12)",
        color: "var(--text-2)",
        display: "flex",
        flexDirection: "column",
        gap: "var(--sp-1)",
      }}
    >
      <strong style={{ color: "var(--text-1)", display: "inline-flex", alignItems: "center", gap: 6 }}><Lock size={14} /> Sobre a credencial</strong>
      <span>
        A senha ou chave SSH é usada para <strong>conectar ao servidor e instalar o agente</strong>.
        Ela fica guardada <strong>cifrada num cofre</strong>, só para reprovisionar e atualizar depois
, nunca é exibida em claro nem devolvida pela API.
      </span>
      <span>
        Como é uma credencial sensível, <strong>só administradores</strong> usam este recurso e a
        conexão com a central deve ser confiável (em produção a app é servida por HTTPS).
      </span>
    </div>
  );
}

// Instalação por SSH: formulário → progresso ao vivo. Devolve só o CONTEÚDO (sem
// Modal) porque é um dos caminhos do fluxo "Adicionar servidor" — quem embrulha,
// dá título e decide quando fechar é o InstalarServidorModal.
//
// Vale saber por que este caminho não serve para tudo: o comando remoto é o
// install.sh, que usa useradd, /etc/systemd/system e espera o serviço ficar
// ativo. Ou seja, **Linux com systemd**. Windows não tem SSH nem sudo por padrão,
// e no macOS o serviço é launchd, não systemd. Para esses, o caminho é o
// instalador que se baixa e roda na máquina.
export function SshProvisionForm({
  onProvisioned,
  onCancel,
}: {
  onProvisioned: () => void;
  onCancel: () => void;
}) {
  const [name, setName] = useState("");
  const [host, setHost] = useState("");
  const [port, setPort] = useState(22);
  const [user, setUser] = useState("root");
  const [authType, setAuthType] = useState<ProvisionAuthType>("password");
  const [password, setPassword] = useState("");
  const [keyPem, setKeyPem] = useState("");
  const [running, setRunning] = useState<ProvisionStartBody | null>(null);
  const [ok, setOk] = useState(false);

  // A credencial vive só enquanto este formulário existe: ao desmontar (fechar o
  // modal ou voltar ao menu de caminhos), o estado some junto — nada de senha
  // sobrando de uma abertura para a seguinte.
  useEffect(() => {
    return () => {
      setPassword("");
      setKeyPem("");
    };
  }, []);

  const secret = authType === "password" ? password : keyPem;
  const valid =
    name.trim() !== "" &&
    host.trim() !== "" &&
    user.trim() !== "" &&
    secret.trim() !== "" &&
    port >= 1 &&
    port <= 65535;

  function begin() {
    if (!valid) return;
    setRunning({
      name: name.trim(),
      host: host.trim(),
      port,
      user: user.trim(),
      auth_type: authType,
      secret,
    });
  }

  if (running) {
    return (
      <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-3)" }}>
        <ProvisionProgress
          run={(onStep, signal) => startProvision(running, onStep, signal)}
          successTitle="Servidor adicionado"
          onFinish={setOk}
        />
        <div className="row" style={{ justifyContent: "flex-end" }}>
          <Button
            variant="primary"
            onClick={() => {
              if (ok) onProvisioned();
              else onCancel();
            }}
          >
            {ok ? "Concluir" : "Voltar"}
          </Button>
        </div>
      </div>
    );
  }

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
      {/* O limite tem de aparecer ANTES de a pessoa digitar uma senha e descobrir
          no erro que a máquina dela não era compatível. */}
      <div
        style={{
          border: "1px solid var(--warn)",
          borderRadius: "var(--radius-sm)",
          padding: "var(--sp-2) var(--sp-3)",
          fontSize: "var(--fs-13)",
          color: "var(--text-2)",
        }}
      >
        Este caminho funciona em <strong>Linux com systemd</strong>, a instalação cria um serviço do
        sistema. Para <strong>Windows</strong>, <strong>macOS</strong> ou Linux sem systemd (hospedagem
        compartilhada), volte e escolha <strong>“Baixar o instalador”</strong>.
      </div>
      <p style={{ color: "var(--text-2)", fontSize: "var(--fs-13)", margin: 0 }}>
        O painel conecta no servidor por SSH, gera a chave de ingestão, instala o agente e sobe o
        serviço, sem você colar comandos. Cada passo aparece ao vivo em seguida.
      </p>

        <FormField label="Nome" help={help.fields["provision.name"]} required>
          <input
            className="field"
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder="ex.: Banco de Produção"
            autoFocus
          />
        </FormField>

        <div className="grid-2">
          <FormField label="Host / IP" help={help.fields["provision.host"]} required>
            <input
              className="field"
              value={host}
              onChange={(e) => setHost(e.target.value)}
              placeholder="ex.: 10.0.0.12 ou app-prod-01"
            />
          </FormField>
          <FormField label="Porta" help={help.fields["provision.port"]}>
            <input
              className="field"
              type="number"
              min={1}
              max={65535}
              value={port}
              onChange={(e) => setPort(Number(e.target.value))}
            />
          </FormField>
        </div>

        <FormField label="Usuário" help={help.fields["provision.user"]} required>
          <input
            className="field"
            value={user}
            onChange={(e) => setUser(e.target.value)}
            placeholder="ex.: root"
            autoComplete="off"
          />
        </FormField>

        <FormField label="Autenticação" help={help.fields["provision.auth"]}>
          <div className="row" style={{ gap: "var(--sp-3)", alignItems: "center" }}>
            <label className="row" style={{ gap: "var(--sp-1)", alignItems: "center" }}>
              <input
                type="radio"
                name="prov-auth"
                checked={authType === "password"}
                onChange={() => setAuthType("password")}
              />
              <span>Senha</span>
            </label>
            <label className="row" style={{ gap: "var(--sp-1)", alignItems: "center" }}>
              <input
                type="radio"
                name="prov-auth"
                checked={authType === "key"}
                onChange={() => setAuthType("key")}
              />
              <span>Chave SSH</span>
            </label>
          </div>
        </FormField>

        {authType === "password" ? (
          <FormField label="Senha" required hint="Usada só para instalar; guardada cifrada no cofre.">
            <input
              className="field"
              type="password"
              value={password}
              onChange={(e) => setPassword(e.target.value)}
              autoComplete="new-password"
              placeholder="senha do usuário no servidor"
            />
          </FormField>
        ) : (
          <FormField
            label="Chave SSH (PEM)"
            required
            hint="Cole a chave privada completa (-----BEGIN … END-----). Guardada cifrada no cofre."
          >
            <textarea
              className="field"
              value={keyPem}
              onChange={(e) => setKeyPem(e.target.value)}
              rows={6}
              spellCheck={false}
              autoComplete="off"
              placeholder={"-----BEGIN OPENSSH PRIVATE KEY-----\n…\n-----END OPENSSH PRIVATE KEY-----"}
              style={{ fontFamily: "var(--font-mono)", fontSize: "var(--fs-12)", resize: "vertical" }}
            />
          </FormField>
        )}

    <SecurityNote />

      <div className="row" style={{ justifyContent: "space-between", marginTop: "var(--sp-2)" }}>
        <Button onClick={onCancel}>Cancelar</Button>
        <Button variant="primary" disabled={!valid} onClick={begin}>
          Instalar agora
        </Button>
      </div>
    </div>
  );
}

// Modal de "Atualizar agente" para um alvo já provisionado: reusa a tela de
// progresso, sem redigitar credencial (usa o cofre via updateProvisionTarget).
export function UpdateAgentModal({
  target,
  onClose,
  onUpdated,
}: {
  target: ProvisionTarget;
  onClose: () => void;
  onUpdated: () => void;
}) {
  const [ok, setOk] = useState(false);
  return (
    <Modal
      open
      onClose={onClose}
      wide
      title={`Atualizar agente, ${target.name}`}
      footer={
        <div className="row" style={{ justifyContent: "flex-end", width: "100%" }}>
          <Button
            variant="primary"
            onClick={() => {
              if (ok) onUpdated();
              onClose();
            }}
          >
            {ok ? "Concluir" : "Fechar"}
          </Button>
        </div>
      }
    >
      <p style={{ color: "var(--text-2)", fontSize: "var(--fs-13)", marginTop: 0 }}>
        Reexecuta a instalação em <strong>{target.host}</strong> usando a credencial guardada cifrada
        no cofre, sem redigitar senha ou chave.
      </p>
      <ProvisionProgress
        run={(onStep, signal) => updateProvisionTarget(target.id, onStep, signal)}
        successTitle="Agente atualizado"
        onFinish={setOk}
      />
    </Modal>
  );
}
