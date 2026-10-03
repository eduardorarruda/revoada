// Parâmetros de uma transformação (a lista é FECHADA no back-end; aqui só os campos
// que cada uma usa). Nada de SQL: são valores que o agente aplica linha a linha.
import { useId } from "react";
import type { MapColuna } from "../../api";

const CHARSETS = ["WIN1252", "ISO8859_1", "ISO8859_15", "WIN1250", "DOS850", "UTF8"];

// mapa_valores é editado como "S=true" por linha.
export function mapaParaTexto(p: Record<string, string> = {}): string {
  return Object.entries(p)
    .map(([k, v]) => `${k}=${v}`)
    .join("\n");
}

export function textoParaMapa(t: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const linha of t.split("\n")) {
    const i = linha.indexOf("=");
    if (i > 0) out[linha.slice(0, i).trim()] = linha.slice(i + 1).trim();
  }
  return out;
}

export function Parametros({
  coluna,
  editavel,
  onMudar,
}: {
  coluna: MapColuna;
  editavel: boolean;
  onMudar: (p: Record<string, string>) => void;
}) {
  const id = useId();
  const p = coluna.parametros ?? {};
  const set = (k: string, v: string) => onMudar({ ...p, [k]: v });
  const campo = (k: string, rotulo: string, dica?: string) => (
    <label className="mm-param" htmlFor={`${id}-${k}`}>
      <span>{rotulo}</span>
      <input id={`${id}-${k}`} className="field" value={p[k] ?? ""} placeholder={dica} disabled={!editavel} onChange={(e) => set(k, e.target.value)} />
    </label>
  );

  switch (coluna.transformacao) {
    case "charset":
      return (
        <div className="mm-params">
          <label className="mm-param" htmlFor={`${id}-de`}>
            <span>de</span>
            <select id={`${id}-de`} className="field" value={p.de ?? "WIN1252"} disabled={!editavel} onChange={(e) => set("de", e.target.value)}>
              {CHARSETS.map((c) => (
                <option key={c}>{c}</option>
              ))}
            </select>
          </label>
          <label className="mm-param mm-param--check" title="O programa antigo gravou UTF-8 numa coluna WIN1252 e o banco devolve “JosÃ©”">
            <input type="checkbox" checked={p.reparar === "sim"} disabled={!editavel} onChange={(e) => set("reparar", e.target.checked ? "sim" : "")} />
            <span>reparar acentos (“JosÃ©” → “José”)</span>
          </label>
        </div>
      );
    case "constante":
    case "valor_padrao":
      return <div className="mm-params">{campo("valor", coluna.transformacao === "constante" ? "valor fixo" : "se vier nulo")}</div>;
    case "concatenar":
      return (
        <div className="mm-params">
          {campo("colunas", "colunas", "NOME,SOBRENOME")}
          {campo("separador", "separador", "espaço")}
        </div>
      );
    case "dividir":
      return (
        <div className="mm-params">
          {campo("separador", "separador", "-")}
          {campo("parte", "parte nº", "1")}
        </div>
      );
    case "data_formato":
      return <div className="mm-params">{campo("formato", "formato", "DD/MM/AAAA")}</div>;
    case "mapa_valores":
      return (
        <div className="mm-params">
          <label className="mm-param" htmlFor={`${id}-mapa`}>
            <span>de=para (um por linha; * = os outros)</span>
            <textarea
              id={`${id}-mapa`}
              className="field"
              rows={3}
              defaultValue={mapaParaTexto(p)}
              placeholder={"S=true\nN=false"}
              disabled={!editavel}
              onBlur={(e) => onMudar(textoParaMapa(e.target.value))}
            />
          </label>
        </div>
      );
  }
  return null;
}
