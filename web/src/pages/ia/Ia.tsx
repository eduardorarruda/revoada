// Agentes de IA (#/ia/*): custo, tokens, latência, erros e o passo a passo das
// aplicações de IA do usuário, a partir dos spans OpenTelemetry que elas já mandam.
// O Revoada observa IA, não usa IA (ADR 008).
//
// Uma casca só para as cinco vistas: o cabeçalho, as abas e a janela de tempo são
// os mesmos, e a janela fica na URL para atravessar as abas. A casca NÃO remonta ao
// trocar de aba (App.tsx usa chaveDaTela): só a vista entra, com a sua cascata.
import { useEffect, useRef, useState, type ReactNode } from "react";
import { m, useReducedMotion } from "motion/react";
import { ActionIcons, HelpPanel, IconButton, LastSeen, PageHeader } from "../../components";
import { acharJanela, JanelaTempo, type OpcaoJanela } from "../../components/JanelaTempo";
import { usePolling } from "../../hooks/usePolling";
import { help } from "../../help";
import { ProvedorCargaDaVista, SINAL_VAZIO, type SinalCarga } from "./comum";
import { ABAS_IA, abaDaVista, hrefIa, vistaDaRota, type AbaIa, type VistaIa } from "./rotas";
import { VisaoGeral } from "./VisaoGeral";
import { Execucoes } from "./Execucoes";
import { Replay } from "./Replay";
import { Ferramentas } from "./Ferramentas";
import { Precos } from "./Precos";
import "./ia.css";

/** De quanto em quanto tempo as vistas com janela se atualizam sozinhas. */
export const ATUALIZAR_A_CADA_MS = 60_000;
/** Sem atualização há mais que isto (aba escondida, rede fora), o selo avisa. */
const IDADE_VELHA_S = 180;
/** O "há X" do selo é recalculado neste ritmo. */
const RELOGIO_SELO_MS = 15_000;
/** Mesma mola da pílula do menu lateral. */
const MOLA_INDICADOR = { type: "spring", stiffness: 520, damping: 42 } as const;

/** Vistas que dependem de uma janela de tempo (replay e preços, não). */
function usaJanela(v: VistaIa): boolean {
  return v.vista === "visao" || v.vista === "execucoes" || v.vista === "ferramentas";
}

function chaveFiltros(p: URLSearchParams): string {
  return ["agente", "modelo", "status", "custo_min"].map((k) => p.get(k) ?? "").join("|");
}

/** Chave da vista: trocar de vista (ou de execução no replay) refaz a entrada dela. */
function chaveDaVista(v: VistaIa): string {
  return v.vista === "replay" ? `replay:${v.traceId}` : v.vista;
}

/**
 * Atualização automática a cada minuto nas vistas com janela. A primeira carga é da
 * própria vista; o tique só refaz a busca se o dado na tela já tem meio minuto (o
 * usePolling também dispara ao voltar para a aba do navegador).
 */
function useAtualizacaoAutomatica(ativa: boolean, atualizadoEm: number | null, atualizar: () => void) {
  const montadaEm = useRef(Date.now());
  usePolling(() => {
    if (!ativa) return;
    const base = atualizadoEm ?? montadaEm.current;
    if (Date.now() - base < ATUALIZAR_A_CADA_MS / 2) return;
    atualizar();
  }, ATUALIZAR_A_CADA_MS);
}

export function PaginaIa({ rota, query }: { rota: string; query: string }) {
  const params = new URLSearchParams(query);
  const vista = vistaDaRota(rota);
  const janela = acharJanela(params.get("janela"));
  // "Atualizar" refaz a consulta da vista com a janela recalculada até agora.
  const [versao, setVersao] = useState(0);
  const [ajuda, setAjuda] = useState(false);
  const [sinal, setSinal] = useState<SinalCarga>(SINAL_VAZIO);
  const atualizar = () => setVersao((n) => n + 1);
  useAtualizacaoAutomatica(usaJanela(vista), sinal.atualizadoEm, atualizar);

  const mudarJanela = (id: string) => {
    const novos = Object.fromEntries(params.entries());
    window.location.hash = hrefIa(rota, { ...novos, janela: id }).slice(1);
  };

  return (
    <div className="page">
      <PageHeader
        title="Agentes de IA"
        subtitle="Custo, tokens, latência e erros das suas aplicações de IA, e o passo a passo de cada execução. O Revoada só observa: não chama nenhum modelo."
        onHelp={() => setAjuda(true)}
        actions={
          <AcoesDoTopo
            janela={usaJanela(vista) ? janela : null}
            mudarJanela={mudarJanela}
            sinal={sinal}
            atualizar={atualizar}
          />
        }
      />
      <AbasIa aba={abaDaVista(vista)} janelaId={janela.id} />
      <ProvedorCargaDaVista value={setSinal}>
        {/* aria-busy só na RECARGA: o dado velho fica na tela, esmaecido, até o novo chegar. */}
        <div key={chaveDaVista(vista)} className="ia-vista stack" aria-busy={sinal.carregando && sinal.temDados}>
          <ConteudoDaVista vista={vista} params={params} janela={janela} versao={versao} mudarJanela={mudarJanela} />
        </div>
      </ProvedorCargaDaVista>
      <AjudaIa aberta={ajuda} onFechar={() => setAjuda(false)} />
    </div>
  );
}

function ConteudoDaVista({
  vista,
  params,
  janela,
  versao,
  mudarJanela,
}: {
  vista: VistaIa;
  params: URLSearchParams;
  janela: OpcaoJanela;
  versao: number;
  mudarJanela: (id: string) => void;
}) {
  switch (vista.vista) {
    case "visao":
      return <VisaoGeral janela={janela} versao={versao} mudarJanela={mudarJanela} />;
    case "execucoes":
      // key: filtros vindos da URL só semeiam o estado na montagem; um link que troca
      // o filtro (sem trocar a rota) precisa remontar. A janela fica fora da key
      // para não apagar o que a pessoa digitou ao trocar de janela.
      return <Execucoes key={chaveFiltros(params)} janela={janela} versao={versao} params={params} mudarJanela={mudarJanela} />;
    case "replay":
      return <Replay traceId={vista.traceId} janelaId={janela.id} versao={versao} />;
    case "ferramentas":
      return <Ferramentas janela={janela} versao={versao} mudarJanela={mudarJanela} />;
    case "precos":
      return <Precos versao={versao} />;
  }
}

/** Janela de tempo, frescor do dado e o botão Atualizar, no canto do cabeçalho. */
function AcoesDoTopo({
  janela,
  mudarJanela,
  sinal,
  atualizar,
}: {
  janela: OpcaoJanela | null;
  mudarJanela: (id: string) => void;
  sinal: SinalCarga;
  atualizar: () => void;
}) {
  return (
    <div className="ia-acoes">
      {janela && <JanelaTempo valor={janela.id} onChange={mudarJanela} />}
      {sinal.atualizadoEm != null && <SeloAtualizado em={sinal.atualizadoEm} />}
      <IconButton
        icon={ActionIcons.reload}
        label="Atualizar agora"
        className="ia-atualizar"
        aria-busy={sinal.carregando}
        onClick={atualizar}
      />
    </div>
  );
}

/** "atualizado há 2 min", recalculado a cada 15 s (o dado em si chega a cada minuto). */
function SeloAtualizado({ em }: { em: number }) {
  const [, setRelogio] = useState(0);
  usePolling(() => setRelogio((n) => n + 1), RELOGIO_SELO_MS);
  return <LastSeen ts={em} staleAfterSeconds={IDADE_VELHA_S} />;
}

function AbasIa({ aba, janelaId }: { aba: AbaIa; janelaId: string }) {
  const ativaRef = useRef<HTMLAnchorElement>(null);
  const reduzir = useReducedMotion();
  // No celular as abas rolam de lado: a ativa precisa estar à vista (ex.: quem abre
  // um link direto para Preços não pode ver a aba cortada na borda).
  useEffect(() => {
    ativaRef.current?.scrollIntoView?.({ inline: "nearest", block: "nearest", behavior: reduzir ? "auto" : "smooth" });
  }, [aba, reduzir]);
  return (
    <nav className="tabs ia-abas" aria-label="Seções de Agentes de IA">
      {ABAS_IA.map((a) => {
        const ativa = a.id === aba;
        return (
          <a
            key={a.id}
            ref={ativa ? ativaRef : undefined}
            className={`tab${ativa ? " tab--active" : ""}`}
            aria-current={ativa ? "page" : undefined}
            aria-label={a.curto ? a.rotulo : undefined}
            href={hrefIa(a.caminho, { janela: janelaId })}
          >
            {a.curto ? (
              <>
                <span className="ia-aba__longo">{a.rotulo}</span>
                <span className="ia-aba__curto">{a.curto}</span>
              </>
            ) : (
              a.rotulo
            )}
            {ativa && <m.span className="ia-aba__indicador" layoutId="ia-aba" transition={MOLA_INDICADOR} aria-hidden="true" />}
          </a>
        );
      })}
    </nav>
  );
}

function AjudaIa({ aberta, onFechar }: { aberta: boolean; onFechar: () => void }) {
  const p = help.pages.ia;
  const secoes: { heading: string; body: ReactNode }[] = [
    { heading: "O que é esta tela", body: p.what },
    {
      heading: "Como usar",
      body: (
        <ol className="help-section__how">
          {p.how.map((h, i) => (
            <li key={i}>{h}</li>
          ))}
        </ol>
      ),
    },
  ];
  if (p.faq && p.faq.length > 0) {
    secoes.push({
      heading: "Perguntas frequentes",
      body: (
        <div className="help-faq">
          {p.faq.map((f, i) => (
            <div key={i}>
              <p className="help-faq__q">{f.q}</p>
              <p className="help-faq__a">{f.a}</p>
            </div>
          ))}
        </div>
      ),
    });
  }
  return <HelpPanel open={aberta} onClose={onFechar} title={p.title} sections={secoes} />;
}
