// Formulário de preço de modelo (admin). Uma troca de preço é uma LINHA NOVA com
// vigência posterior: o passado continua calculado com o preço antigo.
import { useState, type FormEvent } from "react";
import { criarIaPreco, type IaPrecoNovo } from "../../api.ia";
import { Button, FormField, useToast } from "../../components";
import { mensagemDeErro } from "../../format";
import { fmtDataPreco } from "./formato";

export interface CamposPreco {
  provedor: string;
  modelo: string;
  entrada: string;
  saida: string;
  cacheLeitura: string;
  cacheEscrita: string;
  vigenteDesde: string;
}

type ErrosPreco = Partial<Record<keyof CamposPreco, string>>;

/** Hoje (AAAA-MM-DD) no calendário de quem está usando a tela. */
function hoje(): string {
  const d = new Date();
  const dois = (n: number) => String(n).padStart(2, "0");
  return `${d.getFullYear()}-${dois(d.getMonth() + 1)}-${dois(d.getDate())}`;
}

export function camposIniciais(prefill?: { provedor: string; modelo: string }): CamposPreco {
  return {
    provedor: prefill?.provedor ?? "",
    modelo: prefill?.modelo ?? "",
    entrada: "",
    saida: "",
    cacheLeitura: "",
    cacheEscrita: "",
    vigenteDesde: hoje(),
  };
}

/** "0,15" ou "0.15" → 0.15; vazio → null; inválido → NaN. */
function numero(texto: string): number | null {
  const t = texto.trim().replace(",", ".");
  if (t === "") return null;
  const n = Number(t);
  return Number.isFinite(n) && n >= 0 ? n : NaN;
}

/** Valida os campos e, se estiverem bons, devolve o corpo do POST. */
export function validarPreco(c: CamposPreco): { erros: ErrosPreco; preco?: IaPrecoNovo } {
  const erros: ErrosPreco = {};
  const modelo = c.modelo.trim();
  if (!c.provedor.trim()) erros.provedor = "Informe o provedor (ex.: openai, anthropic).";
  if (!modelo) erros.modelo = "Informe o modelo.";
  else if (modelo.slice(0, -1).includes("*")) erros.modelo = "O * só vale no fim, como prefixo (ex.: gpt-4o-mini*).";
  const entrada = numero(c.entrada);
  const saida = numero(c.saida);
  const cacheLeitura = numero(c.cacheLeitura);
  const cacheEscrita = numero(c.cacheEscrita);
  if (entrada === null || Number.isNaN(entrada)) erros.entrada = "Preço de entrada em dólar, zero ou mais.";
  if (saida === null || Number.isNaN(saida)) erros.saida = "Preço de saída em dólar, zero ou mais.";
  if (Number.isNaN(cacheLeitura)) erros.cacheLeitura = "Número em dólar, ou deixe vazio.";
  if (Number.isNaN(cacheEscrita)) erros.cacheEscrita = "Número em dólar, ou deixe vazio.";
  if (!/^\d{4}-\d{2}-\d{2}$/.test(c.vigenteDesde)) erros.vigenteDesde = "Escolha a data a partir da qual o preço vale.";
  if (Object.keys(erros).length > 0) return { erros };
  return {
    erros,
    preco: {
      provedor: c.provedor.trim(),
      modelo,
      entrada_por_1m: entrada,
      saida_por_1m: saida,
      cache_leitura_por_1m: cacheLeitura,
      cache_escrita_por_1m: cacheEscrita,
      moeda: "USD",
      vigente_desde: `${c.vigenteDesde}T00:00:00Z`,
    },
  };
}

const AJUDA_MODELO =
  "Nome do modelo como a biblioteca manda. Termine com * para cobrir todas as versões: gpt-4o-mini* vale para gpt-4o-mini-2024-07-18 e qualquer outra que comece igual. Sem *, só o nome exato.";

type PropsCampo = (k: keyof CamposPreco) => {
  className: string;
  value: string;
  onChange: (e: { target: { value: string } }) => void;
};

function CamposDoPreco({ campo, erros }: { campo: PropsCampo; erros: ErrosPreco }) {
  return (
    <div className="ia-form-preco">
      <FormField label="Provedor" required error={erros.provedor}>
        <input {...campo("provedor")} placeholder="openai" />
      </FormField>
      <FormField label="Modelo" required help={AJUDA_MODELO} error={erros.modelo} hint="* no fim = prefixo">
        <input {...campo("modelo")} placeholder="gpt-4o-mini*" />
      </FormField>
      <FormField label="Entrada (US$ por 1 milhão de tokens)" required error={erros.entrada}>
        <input {...campo("entrada")} inputMode="decimal" placeholder="0,15" />
      </FormField>
      <FormField label="Saída (US$ por 1 milhão de tokens)" required error={erros.saida}>
        <input {...campo("saida")} inputMode="decimal" placeholder="0,60" />
      </FormField>
      <FormField label="Cache: leitura (US$ por 1 mi)" error={erros.cacheLeitura} hint="opcional">
        <input {...campo("cacheLeitura")} inputMode="decimal" />
      </FormField>
      <FormField label="Cache: escrita (US$ por 1 mi)" error={erros.cacheEscrita} hint="opcional">
        <input {...campo("cacheEscrita")} inputMode="decimal" />
      </FormField>
      <FormField label="Vale a partir de" required error={erros.vigenteDesde} hint="o passado mantém o preço anterior">
        <input {...campo("vigenteDesde")} type="date" />
      </FormField>
    </div>
  );
}

export function PrecoForm({
  prefill,
  onSalvo,
  onCancelar,
}: {
  prefill?: { provedor: string; modelo: string };
  onSalvo: () => void;
  /** Com ele, o formulário ganha um "Cancelar" (uso dentro do modal). */
  onCancelar?: () => void;
}) {
  const toast = useToast();
  const [c, setC] = useState<CamposPreco>(() => camposIniciais(prefill));
  const [erros, setErros] = useState<ErrosPreco>({});
  const [salvando, setSalvando] = useState(false);
  const campo: PropsCampo = (k) => ({
    className: "field",
    value: c[k],
    onChange: (e) => setC((ant) => ({ ...ant, [k]: e.target.value })),
  });

  const enviar = async (e: FormEvent) => {
    e.preventDefault();
    const r = validarPreco(c);
    setErros(r.erros);
    if (!r.preco) return;
    setSalvando(true);
    try {
      await criarIaPreco(r.preco);
      toast.success(`Preço de ${r.preco.modelo} cadastrado. Chamadas a partir de ${fmtDataPreco(c.vigenteDesde)} usam este valor.`);
      setC(camposIniciais());
      onSalvo();
    } catch (err) {
      toast.error(mensagemDeErro(err, "Não foi possível cadastrar o preço."));
    } finally {
      setSalvando(false);
    }
  };

  return (
    <form onSubmit={enviar} noValidate aria-label="Cadastrar preço">
      <CamposDoPreco campo={campo} erros={erros} />
      <div className="row row--end ia-form-preco__acoes">
        {onCancelar && (
          <Button type="button" variant="ghost" onClick={onCancelar}>
            Cancelar
          </Button>
        )}
        <Button type="submit" variant="primary" disabled={salvando}>
          {salvando ? "Salvando…" : "Salvar preço"}
        </Button>
      </div>
    </form>
  );
}
