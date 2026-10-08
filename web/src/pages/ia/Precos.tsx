// Modelos e preços (#/ia/precos): a tabela que transforma tokens em dólar. Todos
// veem; só administradores cadastram e apagam (o servidor também barra).
import { useCallback, useState } from "react";
import { Plus, Tags } from "lucide-react";
import { isAdmin } from "../../api";
import { apagarIaPreco, listIaPrecos, type IaModeloSemPreco, type IaPreco, type IaReferenciaPrecos } from "../../api.ia";
import {
  ActionIcons,
  Badge,
  Button,
  Card,
  ConfirmDialog,
  DataTable,
  EmptyState,
  IconButton,
  InfoTip,
  Modal,
  Skeleton,
  useToast,
  type Column,
} from "../../components";
import { mensagemDeErro } from "../../format";
import { Aviso, FalhaCarga, useCarga, useSinalizarCarga } from "./comum";
import { DadosPrivacidade } from "./DadosPrivacidade";
import { fmtDataPreco, fmtOrigemPreco, fmtReferencia, fmtUsd } from "./formato";
import { PrecoForm } from "./PrecoForm";
import { plural } from "./veredito";

const NAO_CADASTRADO = "não cadastrado";

const COLUNAS: Column<IaPreco>[] = [
  { key: "provedor", label: "Provedor", sortable: true },
  {
    key: "modelo",
    label: "Modelo",
    sortable: true,
    render: (p) => (
      <span>
        <span className="ia-mono">{p.modelo}</span> {p.modelo.endsWith("*") && <span className="ia-chip">prefixo</span>}
      </span>
    ),
  },
  { key: "entrada", label: "Entrada / 1 mi", align: "right", render: (p) => fmtUsd(p.entrada_por_1m, NAO_CADASTRADO) },
  { key: "saida", label: "Saída / 1 mi", align: "right", render: (p) => fmtUsd(p.saida_por_1m, NAO_CADASTRADO) },
  { key: "cache_l", label: "Cache leitura", align: "right", hideOnMobile: true, render: (p) => fmtUsd(p.cache_leitura_por_1m, NAO_CADASTRADO) },
  { key: "cache_e", label: "Cache escrita", align: "right", hideOnMobile: true, render: (p) => fmtUsd(p.cache_escrita_por_1m, NAO_CADASTRADO) },
  { key: "vigente_desde", label: "Vale desde", sortable: true, render: (p) => fmtDataPreco(p.vigente_desde) },
  { key: "origem", label: "Origem", hideOnMobile: true, render: (p) => <span className="ia-sub">{fmtOrigemPreco(p.origem)}</span> },
];

const EXPLICA_TABELA =
  "Valores em dólar por 1 milhão de tokens. Modelo terminado em * é prefixo: gpt-4o-mini* vale para qualquer versão que comece igual. Cada linha vale a partir da data em “Vale desde”: uma troca de preço é uma linha nova, e o custo do passado continua calculado com o preço da época.";

/** O formulário aberto: vazio (botão do topo) ou já com o modelo (lista "sem preço"). */
interface FormAberto {
  prefill?: { provedor: string; modelo: string };
  /** Remonta o formulário a cada abertura. */
  n: number;
}

function idadeDaReferencia(r: IaReferenciaPrecos): string {
  return r.dias_desde_atualizacao == null
    ? "idade desconhecida"
    : `atualizada há ${plural(r.dias_desde_atualizacao, "dia", "dias")}`;
}

function AvisoReferencia({ r }: { r: IaReferenciaPrecos }) {
  if (!r.desatualizada) return null;
  return (
    <Aviso tom="warn" titulo="Tabela de referência desatualizada">
      <p>
        Os preços de referência que vêm com o Revoada são de {fmtReferencia(r.referencia) || "data não informada"} (
        {idadeDaReferencia(r)}). Provedores mudam preços: confira os modelos que você usa e cadastre o valor atual. O custo
        do passado não muda.
      </p>
    </Aviso>
  );
}

function Topo({ r, admin, onCadastrar }: { r: IaReferenciaPrecos; admin: boolean; onCadastrar: () => void }) {
  return (
    <div className="toolbar">
      <div className="ia-precos__resumo">
        {!r.desatualizada && (
          <p className="ia-sub">
            Tabela de referência {fmtReferencia(r.referencia) || "não informada"}, {idadeDaReferencia(r)}.
          </p>
        )}
        {!admin && <p className="ia-sub">Só administradores cadastram e apagam preços. Você pode consultar a tabela abaixo.</p>}
      </div>
      {admin && (
        <Button variant="primary" onClick={onCadastrar}>
          <Plus size={16} aria-hidden /> Cadastrar preço
        </Button>
      )}
    </div>
  );
}

function SemPreco({
  modelos,
  admin,
  onCadastrar,
}: {
  modelos: IaModeloSemPreco[];
  admin: boolean;
  onCadastrar: (m: IaModeloSemPreco) => void;
}) {
  if (modelos.length === 0) return null;
  return (
    <Card title="Modelos vistos sem preço">
      <p className="ia-texto">
        Estes modelos foram chamados mas não casam com nenhuma linha da tabela, então o custo deles fica fora da soma (e o
        total aparece como parcial).{admin ? "" : " Peça a um administrador para cadastrar o preço."}
      </p>
      <ul className="ia-lista-sem-preco">
        {modelos.map((m) => (
          <li key={`${m.provedor}/${m.modelo}`} className="row">
            <Badge state="warn">sem preço</Badge>
            <span className="ia-mono">{m.modelo}</span>
            <span className="ia-sub tabular">
              {m.provedor || "provedor não informado"} · {plural(m.chamadas, "chamada", "chamadas")}
            </span>
            {admin && (
              <Button onClick={() => onCadastrar(m)} aria-label={`Cadastrar preço de ${m.modelo}`}>
                Cadastrar preço
              </Button>
            )}
          </li>
        ))}
      </ul>
    </Card>
  );
}

function TabelaPrecos({
  precos,
  admin,
  onApagar,
  onCadastrar,
}: {
  precos: IaPreco[];
  admin: boolean;
  onApagar: (p: IaPreco) => void;
  onCadastrar: () => void;
}) {
  return (
    <Card
      title={
        <span className="ia-cartao-titulo">
          Tabela de preços <InfoTip title="tabela de preços" text={EXPLICA_TABELA} />
        </span>
      }
    >
      <DataTable
        columns={COLUNAS}
        rows={precos}
        keyFn={(p) => p.id}
        searchable
        searchText={(p) => `${p.provedor} ${p.modelo}`}
        searchPlaceholder="Buscar modelo…"
        initialSort={{ key: "modelo", dir: "asc" }}
        rowActions={
          admin ? (p) => <IconButton icon={ActionIcons.delete} label={`Apagar preço de ${p.modelo}`} onClick={() => onApagar(p)} /> : undefined
        }
        empty={
          <EmptyState
            icon={<Tags size={32} strokeWidth={1.5} />}
            title="Nenhum preço cadastrado"
            body="Sem preço, o custo das chamadas aparece como “sem preço”, nunca como zero."
            action={admin ? { label: "Cadastrar preço", onClick: onCadastrar } : undefined}
          />
        }
      />
    </Card>
  );
}

function ModalPreco({ aberto, onFechar, onSalvo }: { aberto: FormAberto | null; onFechar: () => void; onSalvo: () => void }) {
  const modelo = aberto?.prefill?.modelo;
  return (
    <Modal open={aberto !== null} onClose={onFechar} title={modelo ? `Cadastrar preço de ${modelo}` : "Cadastrar preço"} wide>
      <div className="stack">
        <p className="ia-texto">
          Para trocar um preço, cadastre uma linha nova com a data em que ele passou a valer: o custo do passado continua
          calculado com o preço antigo. Valores em dólar por 1 milhão de tokens.
        </p>
        {aberto && <PrecoForm key={aberto.n} prefill={aberto.prefill} onSalvo={onSalvo} onCancelar={onFechar} />}
      </div>
    </Modal>
  );
}

function EsqueletoPrecos() {
  return (
    <>
      <p className="so-leitor" role="status">
        Carregando a tabela de preços…
      </p>
      <Skeleton height={36} />
      <Card>
        <Skeleton height={220} />
      </Card>
    </>
  );
}

/** Apagar uma linha de preço, com confirmação nomeada. */
function useApagarPreco(depois: () => void) {
  const toast = useToast();
  const [apagando, setApagando] = useState<IaPreco | null>(null);
  const confirmar = async () => {
    const alvo = apagando;
    setApagando(null);
    if (!alvo) return;
    try {
      await apagarIaPreco(alvo.id);
      toast.success(`Preço de ${alvo.modelo} apagado.`);
      depois();
    } catch (e) {
      toast.error(mensagemDeErro(e, "Não foi possível apagar o preço."));
    }
  };
  return { apagando, setApagando, confirmar };
}

export function Precos({ versao }: { versao: number }) {
  const admin = isAdmin();
  // eslint-disable-next-line react-hooks/exhaustive-deps -- `versao`: o botão Atualizar refaz a busca
  const buscar = useCallback(() => listIaPrecos(), [versao]);
  const carga = useCarga(buscar, "Não foi possível carregar a tabela de preços.");
  useSinalizarCarga(carga);
  const { dados, erro, recarregar } = carga;
  const [form, setForm] = useState<FormAberto | null>(null);
  const { apagando, setApagando, confirmar } = useApagarPreco(recarregar);
  const abrir = (m?: IaModeloSemPreco) =>
    setForm({ prefill: m ? { provedor: m.provedor, modelo: m.modelo } : undefined, n: Date.now() });
  const salvo = () => {
    setForm(null);
    recarregar();
  };

  if (!dados) return erro ? <FalhaCarga erro={erro} onTentar={recarregar} /> : <EsqueletoPrecos />;
  return (
    <>
      {erro && <FalhaCarga erro={erro} onTentar={recarregar} />}
      <Topo r={dados.referencia} admin={admin} onCadastrar={() => abrir()} />
      <AvisoReferencia r={dados.referencia} />
      <SemPreco modelos={dados.modelos_sem_preco} admin={admin} onCadastrar={abrir} />
      <div className="ia-recarregavel">
        <TabelaPrecos precos={dados.precos} admin={admin} onApagar={setApagando} onCadastrar={() => abrir()} />
      </div>
      {admin && <DadosPrivacidade />}
      <ModalPreco aberto={form} onFechar={() => setForm(null)} onSalvo={salvo} />
      <ConfirmDialog
        open={apagando !== null}
        onCancel={() => setApagando(null)}
        onConfirm={confirmar}
        verb="Apagar"
        target={`o preço de ${apagando?.modelo ?? ""}`}
        consequences="As chamadas que usavam este preço passam a ser calculadas com outra linha que case com o modelo ou aparecem como “sem preço”. Não dá para desfazer; você pode cadastrar o preço de novo."
        danger
      />
    </>
  );
}
