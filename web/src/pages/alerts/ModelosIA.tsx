// Modelos de regra para agentes de IA. Não há motor de alerta próprio para IA: o painel
// grava as séries llm.* em `metrics` a cada minuto (server/internal/ia/runner.go) e
// estas regras são regras comuns sobre elas — com prévia retroativa, plantão e
// silêncio. O modelo só preenche o assistente; a pessoa ajusta o limite e salva.
import type { ComponentType } from "react";
import { CircleAlert, Coins, Repeat, Tag, Timer } from "lucide-react";
import { Modal } from "../../components";

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

type Icone = ComponentType<{ size?: number; className?: string; "aria-hidden"?: boolean }>;

export type ModeloIA = { id: string; titulo: string; explicacao: string; icone: Icone; preset: PresetRegraIA };

export const MODELOS_IA: ModeloIA[] = [
  {
    id: "gasto-hora",
    icone: Coins,
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
    icone: CircleAlert,
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
    icone: Timer,
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
    icone: Repeat,
    titulo: "Agente em loop",
    explicacao: "Uma execução fez mais de N chamadas de modelo: o agente provavelmente está repetindo a mesma ferramenta.",
    preset: {
      name: "Agente de IA em loop", metric: "llm.execucao.passos_max", condition_op: ">", threshold: 20,
      agg: "max", window_seconds: 300, for_seconds: 0, severity: "warning",
      runbook: "Abra a execução em Agentes de IA → Execuções: o replay aponta a ferramenta chamada várias vezes seguidas.",
    },
  },
  {
    id: "sem-preco",
    icone: Tag,
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
      <div className="stack">
        <p className="confirm__body">
          Regras comuns sobre as séries que o painel grava a cada minuto (com 5 min de atraso, para esperar os spans de
          execuções longas). Escolha um modelo, ajuste o limite e salve.
        </p>
        <div className="stack" role="group" aria-label="Modelos de regra">
          {MODELOS_IA.map((m) => {
            const Icone = m.icone;
            // Nome do botão = título; a explicação entra como descrição (mesmo
            // desenho do InstalarServidorModal), e não como parte do nome.
            return (
              <button
                key={m.id}
                type="button"
                className="opcao"
                aria-labelledby={`modelo-ia-${m.id}-titulo`}
                aria-describedby={`modelo-ia-${m.id}-texto`}
                onClick={() => onEscolher(m.preset)}
              >
                <Icone size={18} className="opcao__icone" aria-hidden />
                <span>
                  <span id={`modelo-ia-${m.id}-titulo`} className="opcao__titulo">
                    {m.titulo}
                  </span>
                  <span id={`modelo-ia-${m.id}-texto`} className="opcao__texto">
                    {m.explicacao}
                  </span>
                </span>
              </button>
            );
          })}
        </div>
      </div>
    </Modal>
  );
}
