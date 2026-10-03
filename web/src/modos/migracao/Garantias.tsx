// Garantias da migração: o caminho inteiro (conexão → verificação) com o que o
// Revoada confere em cada passo. Quem vai migrar o banco de um cliente precisa
// saber ANTES o que está protegido e o que ainda depende de olho humano — não
// descobrir no fim. O texto aqui descreve o que o motor faz de fato
// (agent/internal/migracao/copia); se o motor mudar, este texto muda junto.
import {
  Database,
  FileSearch,
  GitCompareArrows,
  ListChecks,
  ScanSearch,
  ShieldCheck,
} from "lucide-react";
import type { LucideIcon } from "lucide-react";
import { Collapsible } from "../../components/Collapsible";
import "./garantias.css";

type Passo = { icone: LucideIcon; titulo: string; garante: string[] };

export const PASSOS_MIGRACAO: Passo[] = [
  {
    icone: Database,
    titulo: "Conexões",
    garante: [
      "Senha cifrada no cofre do painel; nunca volta para a tela",
      "Selada para um agente específico, que fala direto com o banco",
    ],
  },
  {
    icone: FileSearch,
    titulo: "Estrutura",
    garante: [
      "O agente lê só o schema: tabelas, colunas, tipos, chaves e FKs",
      "Nenhuma linha de dado sai do servidor do cliente",
    ],
  },
  {
    icone: GitCompareArrows,
    titulo: "Mapeamento",
    garante: [
      "Cada edição vira uma versão; só versão sem erro é aprovada",
      "Coluna obrigatória do destino sem origem bloqueia a aprovação",
    ],
  },
  {
    icone: ScanSearch,
    titulo: "Simulação",
    garante: [
      "Lê 100% da origem e aplica as transformações sem gravar nada",
      "Aponta cada linha que o destino recusaria: tamanho, nulo, tipo, charset",
    ],
  },
  {
    icone: ListChecks,
    titulo: "Execução",
    garante: [
      "Lotes com checkpoint na mesma transação: pausa e retoma sem perder linha",
      "Tabela com dados carrega numa staging e só entra numa troca única no fim",
    ],
  },
  {
    icone: ShieldCheck,
    titulo: "Verificação",
    garante: [
      "Antes da troca: contagem, chaves e conteúdo coluna a coluna conferidos",
      "Verificar de novo a qualquer hora; reverter volta tudo numa transação",
    ],
  },
];

export function Garantias() {
  return (
    <div className="mm-garantias-bloco">
      <Collapsible
        titulo="Como o Revoada garante que a migração está certa"
        resumo="6 passos, cada um com o que é conferido"
        defaultOpen
        persistKey="mm-garantias"
      >
        <ol className="mm-garantias">
          {PASSOS_MIGRACAO.map((p, i) => (
            <li key={p.titulo} className="mm-garantias__passo">
              <div className="mm-garantias__topo">
                <span className="mm-garantias__num" aria-hidden={true}>
                  {i + 1}
                </span>
                <p.icone size={18} aria-hidden={true} />
                <strong>{p.titulo}</strong>
              </div>
              <ul>
                {p.garante.map((g) => (
                  <li key={g}>{g}</li>
                ))}
              </ul>
            </li>
          ))}
        </ol>
      </Collapsible>
    </div>
  );
}
