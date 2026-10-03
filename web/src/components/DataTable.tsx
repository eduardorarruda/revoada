// DataTable — tabela responsiva e o componente único de listagem do painel.
// Desktop: <table> com cabeçalho fixo dentro de container com overflow-x. Mobile
// (<768px): thead some e cada linha vira um card empilhado (rótulo via data-label).
//
// Recursos opcionais (todos opt-in, para não quebrar os usos existentes):
//  - ordenação por coluna  (Column.sortable + Column.sortValue)
//  - busca                 (searchable + searchText)
//  - paginação             (pageSize — LIGADA POR PADRÃO, ver PAGINA_PADRAO)
//  - estados carregando/erro (loading / error)
// Sem esses props, comporta-se como a versão simples (render + ações + vazio).
import { Fragment, useMemo, useState, type CSSProperties, type ReactNode } from "react";
import { useMobile } from "../hooks/useMobile";

/**
 * Linhas por página quando a tela não pede outra coisa.
 *
 * Antes o default era "sem paginação", e só duas das dezoito tabelas do painel
 * pediam `pageSize` explicitamente. As outras dezesseis despejavam a lista
 * inteira: no celular, /alerts passava de 30 telas de rolagem e /audit de 33 —
 * e a trilha de auditoria só cresce, então isso pioraria para sempre. Paginar
 * por padrão inverte o incentivo: a tela que realmente precisa mostrar tudo
 * pede `pageSize={0}` de propósito, e assume o custo por escrito.
 *
 * 25 é o número que as duas telas já paginadas usavam — mantido para não haver
 * duas noções de "uma página" no mesmo produto. O paginador só aparece quando há
 * mais linhas do que isto, então nenhuma lista curta muda de aparência.
 */
export const PAGINA_PADRAO = 25;

/**
 * No celular a mesma tabela vira CARDS empilhados: cada linha deixa de ocupar
 * uma faixa de 40px e passa a ocupar um bloco com uma linha por coluna. Vinte e
 * cinco linhas que cabiam em uma tela e meia no desktop viram nove telas de
 * rolagem. A página menor é o mesmo conteúdo na mesma proporção de esforço.
 */
export const PAGINA_PADRAO_MOBILE = 10;

export type Column<T> = {
  key: string;
  label: string;
  render?: (r: T) => ReactNode;
  hideOnMobile?: boolean;
  /** "right" alinha a coluna à direita e aplica tabular-nums (números comparáveis). */
  align?: "left" | "right";
  /** Habilita ordenar por esta coluna (clique no cabeçalho). */
  sortable?: boolean;
  /** Valor de ordenação (obrigatório p/ colunas com render custom). Default: campo cru. */
  sortValue?: (r: T) => string | number;
  /** Largura da coluna no modo `fit` (ex.: "96px", "12%"). Colunas sem largura
   *  dividem o espaço restante. Ignorada quando `fit` é falso. */
  width?: string;
  /** Conteúdo extra no CABEÇALHO (ex.: um InfoTip que explica a coluna inteira).
   *  Fica fora do `data-label` do mobile, que precisa continuar sendo texto. */
  headerExtra?: ReactNode;
};

// Célula "vazia" (null/undefined/"") vira travessão — nunca some em silêncio.
function isEmptyCell(v: ReactNode): boolean {
  return v == null || v === "";
}

type SortState = { key: string; dir: "asc" | "desc" } | null;

export function DataTable<T>({
  columns,
  rows,
  keyFn,
  rowActions,
  empty,
  loading,
  error,
  searchable,
  searchText,
  searchPlaceholder = "Buscar…",
  pageSize,
  initialSort,
  expandable,
  fit,
  actionsWidth,
  minWidth,
}: {
  columns: Column<T>[];
  rows: T[];
  keyFn: (r: T) => string | number;
  rowActions?: (r: T) => ReactNode;
  empty: ReactNode;
  /** Conteúdo de detalhe expansível por linha (colapsado). null = linha sem expandir. */
  expandable?: (r: T) => ReactNode;
  /** Mostra estado de carregando (linhas esqueleto) em vez das linhas. */
  loading?: boolean;
  /** Mostra estado de erro (mensagem) em vez das linhas. */
  error?: ReactNode;
  /** Habilita o campo de busca (requer searchText). */
  searchable?: boolean;
  /** Texto pesquisável de uma linha (concatene os campos relevantes). */
  searchText?: (r: T) => string;
  searchPlaceholder?: string;
  /**
   * Linhas por página. Ausente = `PAGINA_PADRAO` no desktop e
   * `PAGINA_PADRAO_MOBILE` no celular; `0` desliga a paginação (use só onde a
   * lista é comprovadamente curta e ver tudo de uma vez importa).
   */
  pageSize?: number;
  initialSort?: { key: string; dir: "asc" | "desc" };
  /** Ajusta a tabela à largura do container (table-layout: fixed) — sem scroll
   *  lateral. Conteúdo longo quebra em vez de estourar. Use com Column.width. */
  fit?: boolean;
  /** Largura da coluna de Ações no modo `fit` (ex.: "150px"). */
  actionsWidth?: string;
  /** PISO de largura da tabela no modo `fit` (ex.: "1080px"). Sem ele, quando a
   *  soma das larguras fixas quase preenche o container, as colunas SEM largura
   *  ficam com alguns pixels e o texto quebra letra por letra — foi o que
   *  aconteceu em /websites (9 colunas fixas = 1044px num container de 1108px
   *  deixavam 8px de texto para "Site"). Com o piso, o container rola de lado em
   *  vez de espremer. Some no mobile, onde a tabela vira cards empilhados. */
  minWidth?: string;
}) {
  const [query, setQuery] = useState("");
  const [expanded, setExpanded] = useState<Set<string | number>>(new Set());
  const [sort, setSort] = useState<SortState>(initialSort ?? null);
  const [page, setPage] = useState(0);
  const estreita = useMobile();
  // `pageSize` explícito manda sempre (inclusive o 0 que desliga a paginação);
  // sem ele, o tamanho vem do formato em que a tabela está sendo desenhada.
  const porPagina = pageSize ?? (estreita ? PAGINA_PADRAO_MOBILE : PAGINA_PADRAO);

  const colByKey = useMemo(() => new Map(columns.map((c) => [c.key, c])), [columns]);

  // 1) filtro por busca
  const filtered = useMemo(() => {
    if (!searchable || !searchText || query.trim() === "") return rows;
    const q = query.trim().toLowerCase();
    return rows.filter((r) => searchText(r).toLowerCase().includes(q));
  }, [rows, searchable, searchText, query]);

  // 2) ordenação
  const sorted = useMemo(() => {
    if (!sort) return filtered;
    const col = colByKey.get(sort.key);
    if (!col) return filtered;
    const val = (r: T): string | number =>
      col.sortValue ? col.sortValue(r) : ((r as Record<string, unknown>)[col.key] as string | number) ?? "";
    const dir = sort.dir === "asc" ? 1 : -1;
    return [...filtered].sort((a, b) => {
      const av = val(a);
      const bv = val(b);
      if (typeof av === "number" && typeof bv === "number") return (av - bv) * dir;
      return String(av).localeCompare(String(bv), "pt-BR", { numeric: true }) * dir;
    });
  }, [filtered, sort, colByKey]);

  // 3) paginação
  const pages = porPagina > 0 ? Math.max(1, Math.ceil(sorted.length / porPagina)) : 1;
  const safePage = Math.min(page, pages - 1);
  const visible = porPagina > 0 ? sorted.slice(safePage * porPagina, safePage * porPagina + porPagina) : sorted;

  function toggleSort(key: string) {
    setPage(0);
    setSort((s) => (s?.key === key ? (s.dir === "asc" ? { key, dir: "desc" } : null) : { key, dir: "asc" }));
  }

  const toolbar = searchable ? (
    <div className="dt-toolbar">
      <input
        type="search"
        className="dt-search"
        value={query}
        placeholder={searchPlaceholder}
        onChange={(e) => {
          setQuery(e.target.value);
          setPage(0);
        }}
        aria-label="Buscar na tabela"
      />
      <span className="dt-count">
        {sorted.length} {sorted.length === 1 ? "item" : "itens"}
      </span>
    </div>
  ) : null;

  // Estados explícitos: erro e carregando têm prioridade sobre linhas/vazio.
  let body: ReactNode;
  if (error != null) {
    body = <div className="dt-state dt-state--error">{error}</div>;
  } else if (loading) {
    body = <LoadingRows cols={columns.length + (rowActions ? 1 : 0)} />;
  } else if (rows.length === 0) {
    body = <>{empty}</>;
  } else if (visible.length === 0) {
    body = <div className="dt-state">Nenhum resultado para “{query}”.</div>;
  } else {
    body = (
      <div className="table-scroll">
        <table
          className={fit ? "dtable dtable--fit" : "dtable"}
          style={fit && minWidth ? ({ "--dt-min": minWidth } as CSSProperties) : undefined}
        >
          {fit && (
            <colgroup>
              {expandable && <col style={{ width: 40 }} />}
              {columns.map((c) => (
                <col key={c.key} style={c.width ? { width: c.width } : undefined} />
              ))}
              {rowActions && <col style={actionsWidth ? { width: actionsWidth } : undefined} />}
            </colgroup>
          )}
          <thead>
            <tr>
              {expandable && <th className="dt-expand-col" aria-hidden="true" />}
              {columns.map((c) => {
                const active = sort?.key === c.key;
                const cls =
                  [c.hideOnMobile ? "dt-hide-mobile" : "", c.align === "right" ? "num" : "", c.sortable ? "dt-sortable" : ""]
                    .filter(Boolean)
                    .join(" ") || undefined;
                return (
                  <th
                    key={c.key}
                    className={cls}
                    onClick={c.sortable ? () => toggleSort(c.key) : undefined}
                    aria-sort={active ? (sort!.dir === "asc" ? "ascending" : "descending") : undefined}
                  >
                    {c.label}
                    {c.sortable && <span className="dt-sort-ind">{active ? (sort!.dir === "asc" ? "▲" : "▼") : "↕"}</span>}
                    {/* o extra do cabeçalho não pode disparar a ordenação ao ser clicado */}
                    {c.headerExtra && (
                      <span className="dt-head-extra" onClick={(e) => e.stopPropagation()}>
                        {c.headerExtra}
                      </span>
                    )}
                  </th>
                );
              })}
              {rowActions && <th className="dt-actions">Ações</th>}
            </tr>
          </thead>
          <tbody>
            {visible.map((r) => {
              const k = keyFn(r);
              const detail = expandable ? expandable(r) : null;
              const isOpen = expanded.has(k);
              const span = columns.length + (rowActions ? 1 : 0) + (expandable ? 1 : 0);
              return (
                <Fragment key={k}>
                  <tr>
                    {expandable && (
                      <td className="dt-expand-col">
                        {detail != null && (
                          <button
                            type="button"
                            className="dt-expand-btn"
                            aria-expanded={isOpen}
                            aria-label={isOpen ? "Recolher" : "Expandir"}
                            onClick={() =>
                              setExpanded((prev) => {
                                const next = new Set(prev);
                                if (next.has(k)) next.delete(k);
                                else next.add(k);
                                return next;
                              })
                            }
                          >
                            {isOpen ? "▾" : "▸"}
                          </button>
                        )}
                      </td>
                    )}
                    {columns.map((c) => {
                      const value: ReactNode = c.render
                        ? c.render(r)
                        : ((r as Record<string, unknown>)[c.key] as ReactNode);
                      const cls =
                        [c.hideOnMobile ? "dt-hide-mobile" : "", c.align === "right" ? "num" : ""].filter(Boolean).join(" ") ||
                        undefined;
                      return (
                        <td key={c.key} data-label={c.label} className={cls}>
                          {isEmptyCell(value) ? "—" : value}
                        </td>
                      );
                    })}
                    {rowActions && (
                      <td data-label="Ações" className="dt-actions">
                        {rowActions(r)}
                      </td>
                    )}
                  </tr>
                  {detail != null && isOpen && (
                    <tr className="dt-detail-row">
                      <td colSpan={span}>{detail}</td>
                    </tr>
                  )}
                </Fragment>
              );
            })}
          </tbody>
        </table>
      </div>
    );
  }

  const pager =
    porPagina > 0 && !loading && error == null && sorted.length > porPagina ? (
      <div className="dt-pager">
        <button type="button" className="dt-page-btn" disabled={safePage === 0} onClick={() => setPage(safePage - 1)}>
          ← Anterior
        </button>
        <span className="dt-count">
          Página {safePage + 1} de {pages}
        </span>
        <button type="button" className="dt-page-btn" disabled={safePage >= pages - 1} onClick={() => setPage(safePage + 1)}>
          Próxima →
        </button>
      </div>
    ) : null;

  return (
    <div className="dt-wrap">
      {toolbar}
      {body}
      {pager}
    </div>
  );
}

function LoadingRows({ cols }: { cols: number }) {
  return (
    <div className="dt-state" aria-busy="true">
      {[0, 1, 2].map((i) => (
        <div className="dt-skel-row" key={i}>
          {Array.from({ length: Math.max(1, cols) }).map((_, j) => (
            <span className="dt-skel-cell" key={j} />
          ))}
        </div>
      ))}
    </div>
  );
}
