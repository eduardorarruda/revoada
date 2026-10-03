// Peças compartilhadas do fluxo "Adicionar servidor": o que mais de um passo usa.
// A tela em si está em InstalarServidorModal.tsx.
import { Radio } from "../../components";
import { AGENT_OS_OPTIONS, type AgentOS, type AgentResourceLimits } from "../../api";

export function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

// A máscara da chave é feita pelo BACKEND (agents.maskKey), que devolve "dev-…46"
// na listagem — a chave em claro não chega mais ao navegador para ser mascarada
// aqui. Mascarar de novo no front seria mascarar uma máscara.

// resourceFlags espelha o AgentResourceFlags do backend: monta as flags do install.sh
// a partir dos limites configurados. Vazio => usa os defaults embutidos no install.sh.
function resourceFlags(l: AgentResourceLimits | null): string {
  if (!l) return "";
  return ` --mem-max ${l.memory_max_mb}M --mem-high ${l.memory_high_mb}M --cpu-quota ${l.cpu_quota_pct}% --nice ${l.nice} --tasks-max ${l.tasks_max} --mem-soft ${l.mem_soft_mb} --max-procs ${l.max_procs}`;
}

// Comando de instalação pronto, com o domínio atual, a chave e os limites preenchidos.
// É a saída para quem prefere colar uma linha no terminal a levar um arquivo.
export function installCommand(key: string, limits: AgentResourceLimits | null): string {
  const o = window.location.origin;
  return `curl -fsSL ${o}/install.sh | sh -s -- --key ${key} --gateway ${o} --agent-url ${o}/revoada-agent${resourceFlags(limits)}`;
}

// comoRodar diz, em uma frase, o que a pessoa faz com o arquivo que acabou de
// baixar. É a única instrução que ela precisa: o instalador já leva a chave, o
// endereço do painel e os limites de recurso dentro dele.
export function comoRodar(os: AgentOS, arquivo: string): string {
  switch (os) {
    case "windows":
      return `Leve ${arquivo} para o servidor, clique nele com o botão direito e escolha “Executar como administrador”.`;
    case "macos":
      return `Leve ${arquivo} para o Mac, abra o Terminal na pasta do arquivo e rode: sudo sh ${arquivo}`;
    default:
      return `Leve ${arquivo} para o servidor e rode: sudo sh ${arquivo}`;
  }
}

// AlvoInstalador é a credencial de onde o instalador será gerado: a chave de um
// servidor ou o token de um instalador universal. `nome` só rotula a tela.
//
// `id` para tipo "chave" é o id PÚBLICO do agente (não a serverkey): o instalador é
// gerado sem que a chave em claro precise existir no navegador.
export type AlvoInstalador = { tipo: "chave" | "token"; id: string; nome: string };

// SeletorSO é a escolha do sistema operacional. Cada opção diz o que sai dela —
// a extensão do arquivo e como se roda —, para a escolha não depender de o
// usuário lembrar qual formato serve para qual máquina.
export function SeletorSO({
  value,
  onChange,
  name,
}: {
  value: AgentOS;
  onChange: (v: AgentOS) => void;
  name: string;
}) {
  return (
    <fieldset style={{ border: 0, margin: 0, padding: 0, minWidth: 0 }}>
      <legend style={{ fontSize: "var(--fs-12)", color: "var(--text-2)", padding: 0, marginBottom: "var(--sp-1)" }}>
        Sistema operacional do servidor
      </legend>
      <div style={{ display: "flex", gap: "var(--sp-3)", flexWrap: "wrap" }}>
        {AGENT_OS_OPTIONS.map((o) => (
          <Radio
            key={o.id}
            name={name}
            checked={value === o.id}
            onChange={() => onChange(o.id)}
            title={o.hint}
            label={
              <>
                {o.label} <span style={{ color: "var(--text-3)", fontFamily: "var(--font-mono)" }}>{o.ext}</span>
              </>
            }
          />
        ))}
      </div>
      <p style={{ fontSize: "var(--fs-12)", color: "var(--text-3)", margin: "var(--sp-1) 0 0" }}>
        {AGENT_OS_OPTIONS.find((o) => o.id === value)?.hint}
      </p>
    </fieldset>
  );
}

// normId normaliza um identificador (identificação da chave, hostname técnico ou nome
// de exibição) para comparação tolerante: sem espaços nas pontas e sem diferença de
// maiúsculas — assim "Servidor Principal" casa com "servidor principal".
function normId(s: string): string {
  return s.trim().toLowerCase();
}

// keyMatch classifica a correspondência entre a identificação da chave e o inventário.
// Casa por hostname técnico OU nome de exibição (display name) — antes só o hostname
// técnico contava, então chaves batizadas com nome amigável caíam sempre em "divergente".
//  - "confere": a identificação bate com um servidor do inventário (hostname técnico
//    ou nome de exibição), tolerando maiúsculas/espaços.
//  - "divergente": a chave tem identificação, mas não corresponde a nenhum servidor
//    (erro de digitação, chave órfã, ou host que nunca reportou).
//  - "sem-id": a chave foi criada sem identificação.
export type KeyMatch = "confere" | "divergente" | "sem-id";
export function keyMatch(hostname: string, knownIds: Set<string>): KeyMatch {
  if (!hostname.trim()) return "sem-id";
  return knownIds.has(normId(hostname)) ? "confere" : "divergente";
}
export const MATCH_META: Record<KeyMatch, { label: string; state: "ok" | "warn" | "neutral"; help: string }> = {
  confere: {
    label: "confere",
    state: "ok",
    help: "A identificação da chave corresponde a um servidor do inventário (pelo hostname técnico ou pelo nome de exibição).",
  },
  divergente: {
    label: "divergente",
    state: "warn",
    help: "A identificação da chave não corresponde a nenhum servidor do inventário, nem por hostname técnico, nem por nome de exibição. Pode ser erro de digitação ou chave órfã.",
  },
  "sem-id": {
    label: "sem identificação",
    state: "neutral",
    help: "Chave criada sem identificação, instale o agente para vincular a um servidor.",
  },
};

// Caixa neutra de conteúdo (formulário de um passo, aviso, resultado). Existe para
// os passos não repetirem seis linhas de estilo cada um.
export const CAIXA: React.CSSProperties = {
  border: "1px solid var(--border)",
  borderRadius: "var(--radius-sm)",
  padding: "var(--sp-3)",
};
