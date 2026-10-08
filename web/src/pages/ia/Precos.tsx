// Modelos e preços (#/ia/precos): a tabela que transforma tokens em dólar. Todos
// veem; só administradores cadastram e apagam (o servidor também barra).
import { useCallback, useRef, useState } from "react";
import { isAdmin } from "../../api";
import { apagarIaPreco, listIaPrecos, type IaModeloSemPreco, type IaPreco, type IaReferenciaPrecos } from "../../api.ia";
import { ActionIcons, Badge, Button, Card, ConfirmDialog, DataTable, IconButton, Skeleton, useToast, type Column } from "../../components";
import { mensagemDeErro } from "../../format";
import { Aviso, FalhaCarga, useCarga } from "./comum";
import { fmtDataPreco, fmtInteiro, fmtUsd } from "./formato";
import { PrecoForm } from "./PrecoForm";

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
  { key: "entrada", label: "Entrada / 1 mi", align: "right", render: (p) => fmtUsd(p.entrada_por_1m, "não cadastrado") },
  { key: "saida", label: "Saída / 1 mi", align: "right", render: (p) => fmtUsd(p.saida_por_1m, "não cadastrado") },
  { key: "cache_l", label: "Cache leitura", align: "right", hideOnMobile: true, render: (p) => fmtUsd(p.cache_leitura_por_1m, "não cadastrado") },
  { key: "cache_e", label: "Cache escrita", align: "right", hideOnMobile: true, render: (p) => fmtUsd(p.cache_escrita_por_1m, "não cadastrado") },
  { key: "vigente_desde", label: "Vale desde", sortable: true, render: (p) => fmtDataPreco(p.vigente_desde) },
  { key: "origem", label: "Origem", hideOnMobile: true, render: (p) => <span className="ia-sub">{p.origem || "manual"}</span> },
];

function AvisoReferencia({ r }: { r: IaReferenciaPrecos }) {
  const idade = r.dias_desde_atualizacao == null ? "idade desconhecida" : `atualizada há ${fmtInteiro(r.dias_desde_atualizacao)} dias`;
  if (!r.desatualizada) {
    return <p className="ia-sub">Tabela de referência {r.referencia || "não informada"}, {idade}.</p>;
  }
  return (
    <Aviso tom="warn" titulo="Tabela de referência desatualizada">
      <p>
        Os preços de referência que vêm com o Revoada são de {r.referencia || "data não informada"} ({idade}). Provedores mudam
        preços: confira os modelos que você usa e cadastre o valor atual. O custo do passado não muda.
      </p>
    </Aviso>
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
      <ul className="stack" style={{ gap: "var(--sp-2)", listStyle: "none", padding: 0, margin: "var(--sp-3) 0 0" }}>
        {modelos.map((m) => (
          <li key={`${m.provedor}/${m.modelo}`} className="row">
            <Badge state="warn">sem preço</Badge>
            <span className="ia-mono">{m.modelo}</span>
            <span className="ia-sub tabular">
              {m.provedor || "provedor não informado"} · {fmtInteiro(m.chamadas)} chamadas
            </span>
            {admin && <Button onClick={() => onCadastrar(m)}>Cadastrar preço</Button>}
          </li>
        ))}
      </ul>
    </Card>
  );
}

function CartaoCadastro({
  admin,
  prefill,
  onSalvo,
}: {
  admin: boolean;
  prefill?: { provedor: string; modelo: string; n: number };
  onSalvo: () => void;
}) {
  return (
    <Card title="Cadastrar preço">
      {admin ? (
        <>
          <p className="ia-texto">
            Para trocar um preço, cadastre uma linha nova com a data em que ele passou a valer: o custo do passado continua
            calculado com o preço antigo. Valores em dólar por 1 milhão de tokens.
          </p>
          {/* key: um novo "Cadastrar preço" da lista remonta o formulário já preenchido */}
          <PrecoForm key={prefill?.n ?? 0} prefill={prefill} onSalvo={onSalvo} />
        </>
      ) : (
        <p className="ia-texto">Só administradores cadastram e apagam preços. Você pode consultar a tabela abaixo.</p>
      )}
    </Card>
  );
}

function TabelaPrecos({ precos, admin, onApagar }: { precos: IaPreco[]; admin: boolean; onApagar: (p: IaPreco) => void }) {
  return (
    <Card title="Tabela de preços">
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
        empty={<p className="ia-texto">Nenhum preço cadastrado. Sem preço, o custo aparece como “sem preço”, nunca como zero.</p>}
      />
    </Card>
  );
}

export function Precos({ versao }: { versao: number }) {
  const admin = isAdmin();
  const toast = useToast();
  // eslint-disable-next-line react-hooks/exhaustive-deps -- `versao`: o botão Atualizar refaz a busca
  const buscar = useCallback(() => listIaPrecos(), [versao]);
  const { dados, erro, recarregar } = useCarga(buscar, "Não foi possível carregar a tabela de preços.");
  const [prefill, setPrefill] = useState<{ provedor: string; modelo: string; n: number }>();
  const [apagando, setApagando] = useState<IaPreco | null>(null);
  const formRef = useRef<HTMLDivElement>(null);

  const cadastrar = (m: IaModeloSemPreco) => {
    setPrefill({ provedor: m.provedor, modelo: m.modelo, n: Date.now() });
    formRef.current?.scrollIntoView?.({ behavior: "smooth", block: "start" }); // ausente em alguns ambientes (jsdom)
  };
  const confirmarApagar = async () => {
    const alvo = apagando;
    setApagando(null);
    if (!alvo) return;
    try {
      await apagarIaPreco(alvo.id);
      toast.success(`Preço de ${alvo.modelo} apagado.`);
      recarregar();
    } catch (e) {
      toast.error(mensagemDeErro(e, "Não foi possível apagar o preço."));
    }
  };

  if (!dados) return erro ? <FalhaCarga erro={erro} onTentar={recarregar} /> : <Skeleton height={200} />;
  return (
    <>
      {erro && <FalhaCarga erro={erro} onTentar={recarregar} />}
      <AvisoReferencia r={dados.referencia} />
      <SemPreco modelos={dados.modelos_sem_preco} admin={admin} onCadastrar={cadastrar} />
      <div ref={formRef}>
        <CartaoCadastro admin={admin} prefill={prefill} onSalvo={recarregar} />
      </div>
      <TabelaPrecos precos={dados.precos} admin={admin} onApagar={setApagando} />
      <ConfirmDialog
        open={apagando !== null}
        onCancel={() => setApagando(null)}
        onConfirm={confirmarApagar}
        verb="Apagar"
        target={`o preço de ${apagando?.modelo ?? ""}`}
        consequences="As chamadas que usavam este preço passam a ser calculadas com outra linha que case com o modelo ou aparecem como “sem preço”. Não dá para desfazer; você pode cadastrar o preço de novo."
        danger
      />
    </>
  );
}
