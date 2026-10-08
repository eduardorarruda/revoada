// Modelos de regra para agentes de IA. Não há motor de alerta próprio para IA: o painel
// grava as séries llm.* em `metrics` a cada minuto (server/internal/ia/runner.go) e
// estas regras são regras comuns sobre elas — com prévia retroativa, plantão e
// silêncio. O modelo só preenche o assistente; a pessoa ajusta o limite e salva.
import { Button, Modal } from "../../components";

/** Os campos do assistente que um modelo preenche (subconjunto de WizardState). */
export type PresetRegraIA = {
  name: string;
  metric: string;
  condition_op: string;
  threshold: number;
  agg: string;
  window_seconds: number;
  for_seconds: number;
  severity: string;
  runbook: string;
};

export type ModeloIA = { id: string; titulo: string; explicacao: string; preset: PresetRegraIA };

export const MODELOS_IA: ModeloIA[] = [
  {
    id: "gasto-hora",
    titulo: "Gasto com IA na última hora",
    explicacao: "Soma do custo das chamadas numa janela de 1 h, por serviço/agente/modelo. Ajuste o limite ao seu orçamento.",
    preset: {
      name: "Gasto com IA acima do orçamento (1 h)", metric: "llm.custo_usd", condition_op: ">", threshold: 5,
      agg: "sum", window_seconds: 3600, for_seconds: 0, severity: "warning",
      runbook: "Abra Agentes de IA → Visão geral na última hora: qual modelo e qual agente subiram? Um agente em loop costuma aparecer junto em “Chamadas numa execução”.",
    },
  },
  {
    id: "erros",
    titulo: "Chamadas de IA com erro",
    explicacao: "Mais de N erros do provedor em 10 min (limite de taxa, timeout, 5xx).",
    preset: {
      name: "Chamadas de IA falhando", metric: "llm.erros", condition_op: ">", threshold: 5,
      agg: "sum", window_seconds: 600, for_seconds: 0, severity: "critical",
      runbook: "Agentes de IA → Execuções com status erro mostra o tipo do erro em cada passo (RateLimitError, Timeout…).",
    },
  },
  {
    id: "latencia",
    titulo: "IA lenta (p95)",
    explicacao: "p95 das chamadas acima de N segundos de forma sustentada. É o p95 de cada minuto: use Máximo, não média.",
    preset: {
      name: "Latência p95 da IA alta", metric: "llm.latencia_p95_ms", condition_op: ">", threshold: 10000,
      agg: "max", window_seconds: 600, for_seconds: 300, severity: "warning",
      runbook: "Compare com o host em Agentes de IA → Replay → “Ver host nesta hora”: lentidão do provedor ou da sua máquina?",
    },
  },
  {
    id: "loop",
    titulo: "Agente em loop",
    explicacao: "Uma execução fez mais de N chamadas de modelo — o agente provavelmente está repetindo a mesma ferramenta.",
    preset: {
      name: "Agente de IA em loop", metric: "llm.execucao.passos_max", condition_op: ">", threshold: 20,
      agg: "max", window_seconds: 300, for_seconds: 0, severity: "warning",
      runbook: "Abra a execução em Agentes de IA → Execuções: o replay aponta a ferramenta chamada várias vezes seguidas.",
    },
  },
  {
    id: "sem-preco",
    titulo: "Modelo sem preço",
    explicacao: "Chamadas num modelo que não está na tabela de preços: o gasto mostrado fica menor que o real.",
    preset: {
      name: "Modelo de IA sem preço cadastrado", metric: "llm.sem_preco", condition_op: ">", threshold: 0,
      agg: "sum", window_seconds: 3600, for_seconds: 0, severity: "info",
      runbook: "Cadastre o preço em Agentes de IA → Modelos e preços (a lista “sem preço” já traz o nome do modelo).",
    },
  },
];

export function ModelosIA({
  aberto,
  onFechar,
  onEscolher,
}: {
  aberto: boolean;
  onFechar: () => void;
  onEscolher: (p: PresetRegraIA) => void;
}) {
  return (
    <Modal open={aberto} onClose={onFechar} title="Alertas para agentes de IA">
      <p className="muted" style={{ marginTop: 0 }}>
        Regras comuns sobre as séries que o painel grava a cada minuto (com 5 min de atraso, para esperar os spans de
        execuções longas). Escolha um modelo, ajuste o limite e salve.
      </p>
      <ul className="modelos-ia" style={{ listStyle: "none", padding: 0, margin: 0, display: "grid", gap: "var(--sp-2)" }}>
        {MODELOS_IA.map((m) => (
          <li key={m.id}>
            <Button onClick={() => onEscolher(m.preset)} style={{ width: "100%", justifyContent: "flex-start", textAlign: "left" }}>
              <span style={{ display: "grid", gap: 2 }}>
                <strong>{m.titulo}</strong>
                <span className="muted" style={{ fontWeight: 400 }}>{m.explicacao}</span>
              </span>
            </Button>
          </li>
        ))}
      </ul>
    </Modal>
  );
}
