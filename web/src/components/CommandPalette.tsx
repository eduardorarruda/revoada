// CommandPalette — "ir para qualquer lugar" com Ctrl+K (⌘K no Mac).
//
// O painel tem 15 telas e uma frota de servidores; chegar ao servidor certo pelo
// menu são três cliques e uma rolagem. Aqui são duas teclas e três letras.
//
// Carga no servidor: a lista de servidores só é buscada quando a paleta ABRE, uma
// vez por abertura. Nada de polling — ninguém precisa do inventário atualizado
// enquanto a paleta está fechada.
import { useEffect, useMemo, useRef, useState, type ComponentType } from "react";
import { AnimatePresence, m } from "motion/react";
import { CornerDownLeft, LogOut, Moon, Search, Server, Sun, X } from "lucide-react";
import { listHosts, logout, type HostDetail } from "../api";
import { DUR, EASE, MOLA } from "../motion";

type Icone = ComponentType<{ size?: number | string; "aria-hidden"?: boolean }>;

export interface ItemPaleta {
  id: string;
  rotulo: string;
  grupo: string;
  icone: Icone;
  /** Texto extra que também casa com a busca (hostname técnico, IPs…). */
  chaves?: string;
  detalhe?: string;
  acao: () => void;
}

/** Busca por palavras: todas precisam aparecer, em qualquer ordem, sem acento. */
export function casa(item: Pick<ItemPaleta, "rotulo" | "chaves" | "grupo">, busca: string): boolean {
  const normal = (t: string) => t.normalize("NFD").replace(/\p{Diacritic}/gu, "").toLowerCase();
  const alvo = normal(`${item.rotulo} ${item.chaves ?? ""} ${item.grupo}`);
  return normal(busca)
    .split(/\s+/)
    .filter(Boolean)
    .every((palavra) => alvo.includes(palavra));
}

export function CommandPalette({
  navegacao,
  tema,
  alternarTema,
}: {
  navegacao: ItemPaleta[];
  tema: "dark" | "light";
  alternarTema: () => void;
}) {
  const [aberta, setAberta] = useState(false);
  const [busca, setBusca] = useState("");
  const [ativo, setAtivo] = useState(0);
  const [hosts, setHosts] = useState<HostDetail[] | null>(null);
  const campoRef = useRef<HTMLInputElement>(null);
  const listaRef = useRef<HTMLUListElement>(null);
  const focoAnterior = useRef<HTMLElement | null>(null);

  // Atalho global. Também escuta o evento do botão da barra do topo.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === "k") {
        // Com outra janela aberta (formulário, confirmação), a paleta não abre por
        // cima: ela trocaria de tela com o formulário ainda na frente.
        if (document.querySelector('[aria-modal="true"]:not(.paleta)')) return;
        e.preventDefault();
        setAberta((a) => !a);
      }
    };
    const abrir = () => setAberta(true);
    const fechar = () => setAberta(false);
    document.addEventListener("keydown", onKey);
    window.addEventListener("revoada:paleta", abrir);
    window.addEventListener("revoada:paleta-fechar", fechar);
    return () => {
      document.removeEventListener("keydown", onKey);
      window.removeEventListener("revoada:paleta", abrir);
      window.removeEventListener("revoada:paleta-fechar", fechar);
    };
  }, []);

  useEffect(() => {
    if (!aberta) return;
    focoAnterior.current = document.activeElement as HTMLElement | null;
    setBusca("");
    setAtivo(0);
    let vivo = true;
    listHosts()
      .then((r) => vivo && setHosts(r.hosts ?? []))
      .catch(() => vivo && setHosts([]));
    // Foco depois do quadro de entrada, senão o navegador rola para o campo.
    const t = requestAnimationFrame(() => campoRef.current?.focus());
    return () => {
      vivo = false;
      cancelAnimationFrame(t);
      focoAnterior.current?.focus?.();
    };
  }, [aberta]);

  const itens = useMemo<ItemPaleta[]>(() => {
    const irPara = (href: string) => () => {
      window.location.hash = href.replace(/^#/, "");
      setAberta(false);
    };
    const servidores: ItemPaleta[] = (hosts ?? []).map((h) => ({
      id: `host:${h.hostname}`,
      rotulo: h.display_name?.trim() || h.hostname,
      grupo: "Servidores",
      icone: Server,
      chaves: `${h.hostname} ${h.ips ?? ""} ${h.os ?? ""}`,
      detalhe: h.display_name?.trim() ? h.hostname : (h.os ?? undefined),
      acao: irPara(`#/hosts/${encodeURIComponent(h.hostname)}`),
    }));
    const acoes: ItemPaleta[] = [
      {
        id: "acao:tema",
        rotulo: tema === "dark" ? "Mudar para o tema claro" : "Mudar para o tema escuro",
        grupo: "Ações",
        icone: tema === "dark" ? Sun : Moon,
        chaves: "tema claro escuro aparencia",
        acao: () => {
          alternarTema();
          setAberta(false);
        },
      },
      {
        id: "acao:sair",
        rotulo: "Sair do painel",
        grupo: "Ações",
        icone: LogOut,
        chaves: "logout sair desconectar",
        acao: () => {
          setAberta(false);
          void logout().then(() => (window.location.hash = "/"));
        },
      },
    ];
    return [...navegacao, ...servidores, ...acoes];
  }, [navegacao, hosts, tema, alternarTema]);

  const visiveis = useMemo(() => itens.filter((it) => casa(it, busca)).slice(0, 40), [itens, busca]);

  // Mantém o item ativo dentro da lista e visível na rolagem.
  useEffect(() => {
    if (ativo >= visiveis.length) setAtivo(Math.max(0, visiveis.length - 1));
  }, [visiveis.length, ativo]);
  useEffect(() => {
    listaRef.current?.querySelector<HTMLElement>(`[data-indice="${ativo}"]`)?.scrollIntoView({ block: "nearest" });
  }, [ativo]);

  function teclar(e: React.KeyboardEvent) {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setAtivo((i) => Math.min(i + 1, visiveis.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setAtivo((i) => Math.max(i - 1, 0));
    } else if (e.key === "Enter") {
      e.preventDefault();
      visiveis[ativo]?.acao();
    } else if (e.key === "Escape") {
      e.preventDefault();
      setAberta(false);
    } else if (e.key === "Tab") {
      // Foco preso: a paleta só tem um campo; Tab não pode fugir para a tela de trás.
      e.preventDefault();
    }
  }

  // Grupos na ordem em que aparecem, cada um com seus itens e o índice global
  // (o índice é o que as setas percorrem).
  const grupos: { nome: string; itens: { it: ItemPaleta; i: number }[] }[] = [];
  visiveis.forEach((it, i) => {
    const ultimo = grupos[grupos.length - 1];
    if (ultimo && ultimo.nome === it.grupo) ultimo.itens.push({ it, i });
    else grupos.push({ nome: it.grupo, itens: [{ it, i }] });
  });
  const aviso =
    visiveis.length === 0
      ? hosts === null
        ? "Carregando servidores…"
        : `Nada encontrado para “${busca}”.`
      : `${visiveis.length} ${visiveis.length === 1 ? "resultado" : "resultados"}`;

  return (
    <AnimatePresence>
      {aberta && (
        <m.div
          className="paleta__fundo"
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
          transition={{ duration: DUR.base }}
          onMouseDown={(e) => {
            if (e.target === e.currentTarget) setAberta(false);
          }}
        >
          <m.div
            className="paleta"
            role="dialog"
            aria-modal="true"
            aria-label="Ir para"
            initial={{ opacity: 0, y: -12, scale: 0.97 }}
            animate={{ opacity: 1, y: 0, scale: 1 }}
            exit={{ opacity: 0, y: -8, scale: 0.98, transition: { duration: DUR.rapido, ease: EASE.vaiVolta } }}
            transition={MOLA}
            onKeyDown={teclar}
          >
            <div className="paleta__busca">
              <Search size={18} aria-hidden={true} />
              <input
                ref={campoRef}
                className="paleta__campo"
                placeholder="Ir para uma tela ou servidor…"
                value={busca}
                onChange={(e) => {
                  setBusca(e.target.value);
                  setAtivo(0);
                }}
                role="combobox"
                aria-label="Ir para uma tela ou servidor"
                aria-expanded={visiveis.length > 0}
                aria-controls="paleta-lista"
                aria-activedescendant={visiveis[ativo] ? `paleta-${visiveis[ativo].id}` : undefined}
                aria-autocomplete="list"
                spellCheck={false}
              />
              <kbd className="paleta__tecla">Esc</kbd>
              <button type="button" className="paleta__fechar" aria-label="Fechar" onClick={() => setAberta(false)}>
                <X size={16} aria-hidden={true} />
              </button>
            </div>
            {/* Quantos resultados há, falado pelo leitor de tela a cada busca. */}
            <div className="so-leitor" role="status" aria-live="polite">
              {aviso}
            </div>
            {visiveis.length === 0 && (
              <p className="paleta__vazio" aria-hidden="true">
                {aviso}
              </p>
            )}
            <ul className="paleta__lista" id="paleta-lista" role="listbox" ref={listaRef} aria-label="Resultados">
              {grupos.map((g) => (
                <li key={g.nome} role="group" aria-labelledby={`paleta-grupo-${g.nome}`} className="paleta__bloco">
                  <div className="paleta__grupo" id={`paleta-grupo-${g.nome}`} role="presentation">
                    {g.nome}
                  </div>
                  <ul role="presentation" className="paleta__sublista">
                    {g.itens.map(({ it, i }) => {
                      const Icone = it.icone;
                      return (
                        <li
                          key={it.id}
                          id={`paleta-${it.id}`}
                          data-indice={i}
                          role="option"
                          aria-selected={i === ativo}
                          className={`paleta__item${i === ativo ? " paleta__item--ativo" : ""}`}
                          onMouseMove={() => setAtivo(i)}
                          onClick={it.acao}
                        >
                          <span className="paleta__icone">
                            <Icone size={16} aria-hidden={true} />
                          </span>
                          <span className="paleta__rotulo">{it.rotulo}</span>
                          {it.detalhe && <span className="paleta__detalhe">{it.detalhe}</span>}
                          {i === ativo && <CornerDownLeft size={14} aria-hidden={true} className="paleta__enter" />}
                        </li>
                      );
                    })}
                  </ul>
                </li>
              ))}
            </ul>
            <div className="paleta__rodape">
              <span>
                <kbd className="paleta__tecla">↑</kbd>
                <kbd className="paleta__tecla">↓</kbd> navegar
              </span>
              <span>
                <kbd className="paleta__tecla">Enter</kbd> abrir
              </span>
              <span className="paleta__dica">Dica: Ctrl+K abre de qualquer tela</span>
            </div>
          </m.div>
        </m.div>
      )}
    </AnimatePresence>
  );
}
