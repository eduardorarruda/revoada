// Agentes de IA (#/ia/*): custo, tokens, latência, erros e o passo a passo das
// aplicações de IA do usuário, a partir dos spans OpenTelemetry que elas já mandam.
// O Revoada observa IA, não usa IA (ADR 008).
//
// Uma casca só para as cinco vistas: o cabeçalho, as abas e a janela de tempo são
// os mesmos, e a janela fica na URL para atravessar as abas.
import { useState } from "react";
import { ActionIcons, HelpPanel, IconButton, PageHeader } from "../../components";
import { acharJanela, JanelaTempo } from "../../components/JanelaTempo";
import { help } from "../../help";
import { ABAS_IA, abaDaVista, hrefIa, vistaDaRota, type VistaIa } from "./rotas";
import { VisaoGeral } from "./VisaoGeral";
import { Execucoes } from "./Execucoes";
import { Replay } from "./Replay";
import { Ferramentas } from "./Ferramentas";
import { Precos } from "./Precos";
import "./ia.css";

/** Vistas que dependem de uma janela de tempo (replay e preços, não). */
function usaJanela(v: VistaIa): boolean {
  return v.vista === "visao" || v.vista === "execucoes" || v.vista === "ferramentas";
}

function chaveFiltros(p: URLSearchParams): string {
  return ["agente", "modelo", "status", "custo_min"].map((k) => p.get(k) ?? "").join("|");
}

export function PaginaIa({ rota, query }: { rota: string; query: string }) {
  const params = new URLSearchParams(query);
  const vista = vistaDaRota(rota);
  const aba = abaDaVista(vista);
  const janela = acharJanela(params.get("janela"));
  // "Atualizar" refaz a consulta da vista com a janela recalculada até agora.
  const [versao, setVersao] = useState(0);
  const [ajuda, setAjuda] = useState(false);

  const mudarJanela = (id: string) => {
    const novos = Object.fromEntries(params.entries());
    window.location.hash = hrefIa(rota, { ...novos, janela: id }).slice(1);
  };

  return (
    <div className="page stack">
      <PageHeader
        title="Agentes de IA"
        subtitle="Custo, tokens, latência e erros das suas aplicações de IA, e o passo a passo de cada execução. O Revoada só observa: não chama nenhum modelo."
        onHelp={() => setAjuda(true)}
      />
      <div className="ia-topo">
        <nav className="tabs" aria-label="Seções de Agentes de IA">
          {ABAS_IA.map((a) => (
            <a
              key={a.id}
              className={`tab${a.id === aba ? " tab--active" : ""}`}
              aria-current={a.id === aba ? "page" : undefined}
              href={hrefIa(a.caminho, { janela: janela.id })}
            >
              {a.rotulo}
            </a>
          ))}
        </nav>
        <div className="ia-topo__janela">
          {usaJanela(vista) && <JanelaTempo valor={janela.id} onChange={mudarJanela} />}
          <IconButton icon={ActionIcons.reload} label="Atualizar agora" onClick={() => setVersao((n) => n + 1)} />
        </div>
      </div>

      {vista.vista === "visao" && <VisaoGeral janela={janela} versao={versao} />}
      {vista.vista === "execucoes" && (
        // key: filtros vindos da URL só semeiam o estado na montagem; um link que troca
        // o filtro (sem trocar a rota) precisa remontar. A janela fica fora da key
        // para não apagar o que a pessoa digitou ao trocar de janela.
        <Execucoes key={chaveFiltros(params)} janela={janela} versao={versao} params={params} />
      )}
      {vista.vista === "replay" && <Replay traceId={vista.traceId} janelaId={janela.id} versao={versao} />}
      {vista.vista === "ferramentas" && <Ferramentas janela={janela} versao={versao} />}
      {vista.vista === "precos" && <Precos versao={versao} />}

      <HelpPanel
        open={ajuda}
        onClose={() => setAjuda(false)}
        title={help.pages.ia.title}
        sections={[
          { heading: "O que é esta tela", body: help.pages.ia.what },
          {
            heading: "Como usar",
            body: (
              <ol className="help-section__how">
                {help.pages.ia.how.map((h, i) => (
                  <li key={i}>{h}</li>
                ))}
              </ol>
            ),
          },
          ...(help.pages.ia.faq ?? []).map((f) => ({ heading: f.q, body: f.a })),
        ]}
      />
    </div>
  );
}
