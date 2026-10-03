// Editor visual do mapeamento (ARQUITETURA §9.2): tabelas da origem à esquerda, do destino à
// direita, ligadas por curvas; ao escolher um par, o mapeamento coluna a coluna com
// transformações. Toda edição vira uma VERSÃO nova validada pelo back-end.
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { AlertTriangle, CheckCircle2, Sparkles, XCircle } from "lucide-react";
import { Badge, Button } from "../../components";
import { useToast } from "../../components/Toast";
import {
  aprovarMapeamento,
  obterProjeto,
  podeOperar,
  salvarMapeamento,
  sugerirMapeamento,
  ultimoEsquema,
  type Esquema,
  type MapColuna,
  type MapTabela,
  type Mapeamento,
  type Problema,
  type ProjetoMigracao,
  type TabelaEsquema,
  type Transformacao,
  type VersaoMapeamento,
} from "../../api";
import { NOME_ACAO, NOME_TRANSFORMACAO, mensagemErro } from "./util";
import { Parametros } from "./Parametros";
import { Execucao } from "./Execucao";
import { Upgrade } from "./Upgrade";

type Curva = { origem: string; d: string; confianca: number };

export function Editor({ projetoId }: { projetoId: string }) {
  const toast = useToast();
  const reduzir = useReducedMotion();
  const [projeto, setProjeto] = useState<ProjetoMigracao | null>(null);
  const [versao, setVersao] = useState<VersaoMapeamento | null>(null);
  const [origem, setOrigem] = useState<Esquema | null>(null);
  const [destino, setDestino] = useState<Esquema | null>(null);
  const [mapa, setMapa] = useState<Mapeamento | null>(null);
  const [sujo, setSujo] = useState(false);
  const [selecionada, setSelecionada] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [erro, setErro] = useState<string | null>(null);

  const carregar = useCallback(async () => {
    try {
      const r = await obterProjeto(projetoId);
      setProjeto(r.projeto);
      const eo = await ultimoEsquema(r.projeto.origem_id).catch(() => null);
      setOrigem(eo?.conteudo ?? null);
      const ed = r.projeto.destino_id ? await ultimoEsquema(r.projeto.destino_id).catch(() => null) : eo;
      setDestino(ed?.conteudo ?? null);
      if (r.mapeamento) {
        setVersao(r.mapeamento);
        setMapa(r.mapeamento.conteudo);
        setSujo(false);
      }
    } catch (e) {
      setErro(mensagemErro(e));
    }
  }, [projetoId]);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  const aplicarVersao = (v: VersaoMapeamento) => {
    setVersao(v);
    setMapa(v.conteudo);
    setSujo(false);
  };

  const executar = async (fn: () => Promise<VersaoMapeamento>, ok: string) => {
    setBusy(true);
    try {
      const v = await fn();
      aplicarVersao(v);
      toast.success(ok);
    } catch (e) {
      toast.error(mensagemErro(e));
    } finally {
      setBusy(false);
    }
  };

  const alterarTabela = (nome: string, f: (t: MapTabela) => MapTabela) => {
    setMapa((m) => (m ? { tabelas: m.tabelas.map((t) => (t.tabela_origem === nome ? f(t) : t)) } : m));
    setSujo(true);
  };

  const problemas = versao?.problemas ?? [];
  const erros = problemas.filter((p) => p.nivel === "erro").length;
  const atual = mapa?.tabelas.find((t) => t.tabela_origem === selecionada) ?? null;

  if (erro) return <p className="mm-erro">{erro}</p>;
  if (!projeto) return <p className="mm-dica">Carregando…</p>;
  if (projeto.tipo === "upgrade_versao") return <Upgrade projeto={projeto} />;

  return (
    <section className="mm-editor">
      <header className="mm-editor__cabeca">
        <div>
          <a href="#/migracao" className="mm-dica">
            ← Projetos
          </a>
          <h1>{projeto.nome}</h1>
          <p className="mm-dica">
            {origem ? `${origem.motor} ${origem.versao} · ${origem.charset}` : "origem ainda não lida"} →{" "}
            {destino ? `${destino.motor} ${destino.versao}` : "destino ainda não lido"}
          </p>
        </div>
        <div className="mm-editor__estado">
          {versao && (
            <Badge state={versao.estado === "aprovado" ? "ok" : versao.estado === "valido" ? "info" : "warn"}>
              v{versao.versao} · {versao.estado}
              {sujo ? " · editado" : ""}
            </Badge>
          )}
          {podeOperar() && (
            <div className="mm-editor__botoes">
              <Button onClick={() => executar(() => sugerirMapeamento(projetoId), "Sugestão gerada como versão nova.")} disabled={busy || !origem}>
                <Sparkles size={16} aria-hidden={true} /> Sugerir automaticamente
              </Button>
              <Button onClick={() => mapa && executar(() => salvarMapeamento(projetoId, mapa), "Versão salva e validada.")} disabled={busy || !sujo || !mapa}>
                Salvar versão
              </Button>
              {/* versão já aprovada (e sem edição pendente): o selo ao lado já diz;
                  um "Aprovar" apagado ali parecia pedir uma ação que não existe */}
              {!(versao?.estado === "aprovado" && !sujo) && (
                <Button
                  variant="primary"
                  onClick={() => versao && executar(() => aprovarMapeamento(projetoId, versao.versao), "Mapeamento aprovado.")}
                  disabled={busy || sujo || versao?.estado !== "valido"}
                  title={
                    sujo
                      ? "Salve a versão editada antes de aprovar"
                      : versao?.estado !== "valido"
                        ? "Corrija os erros e salve para poder aprovar"
                        : "Aprovar (pede confirmação de identidade)"
                  }
                >
                  Aprovar v{versao?.versao ?? ""}
                </Button>
              )}
            </div>
          )}
        </div>
      </header>

      {!origem && (
        <div className="mm-vazio">
          <p className="mm-vazio__titulo">Leia a estrutura dos bancos primeiro</p>
          <p>
            Em <a href="#/migracao/conexoes">Conexões</a>, use “Ler estrutura” na origem{projeto.destino_id ? " e no destino" : ""}.
          </p>
        </div>
      )}

      {origem && !mapa && (
        <div className="mm-vazio">
          <p className="mm-vazio__titulo">Nenhum mapeamento ainda</p>
          <p>“Sugerir automaticamente” compara nomes, sinônimos (pt/en), tipos e chaves e monta uma primeira versão para você revisar.</p>
        </div>
      )}

      {origem && destino && mapa && (
        <div className="mm-editor__corpo">
          <Diagrama
            mapa={mapa}
            origem={origem}
            destino={destino}
            problemas={problemas}
            selecionada={selecionada}
            onSelecionar={setSelecionada}
            reduzir={!!reduzir}
          />
          <aside className="mm-problemas" aria-label="Problemas da validação">
            <h2>
              {erros > 0 ? <XCircle size={18} aria-hidden={true} /> : <CheckCircle2 size={18} aria-hidden={true} />}
              {erros > 0 ? `${erros} erro(s) impedem aprovar` : "Sem erros"}
            </h2>
            {sujo && <p className="mm-dica">Há edições não salvas — salve para validar de novo.</p>}
            <ul>
              {problemas.map((p, i) => (
                <li key={i} className={`mm-problema mm-problema--${p.nivel}`}>
                  <button type="button" onClick={() => p.tabela && setSelecionada(p.tabela)}>
                    {p.nivel === "erro" ? <XCircle size={14} aria-hidden={true} /> : <AlertTriangle size={14} aria-hidden={true} />}
                    <span>
                      <strong>
                        {p.tabela}
                        {p.coluna ? `.${p.coluna}` : ""}
                      </strong>{" "}
                      {p.mensagem}
                    </span>
                  </button>
                </li>
              ))}
            </ul>
          </aside>
        </div>
      )}

      {versao && projeto.destino_id && <Execucao projetoId={projetoId} versao={versao} sujo={sujo} />}

      <AnimatePresence>
        {atual && origem && destino && (
          <motion.div
            key={atual.tabela_origem}
            initial={reduzir ? false : { opacity: 0, y: 16 }}
            animate={{ opacity: 1, y: 0 }}
            exit={reduzir ? undefined : { opacity: 0, y: 8 }}
          >
            <PainelTabela
              mt={atual}
              origem={origem}
              destino={destino}
              problemas={problemas.filter((p) => p.tabela === atual.tabela_origem)}
              editavel={podeOperar()}
              onAlterar={(f) => alterarTabela(atual.tabela_origem, f)}
              onFechar={() => setSelecionada(null)}
            />
          </motion.div>
        )}
      </AnimatePresence>
    </section>
  );
}

// ---------------------------------------------------------------- diagrama

function Diagrama({
  mapa,
  origem,
  destino,
  problemas,
  selecionada,
  onSelecionar,
  reduzir,
}: {
  mapa: Mapeamento;
  origem: Esquema;
  destino: Esquema;
  problemas: Problema[];
  selecionada: string | null;
  onSelecionar: (t: string) => void;
  reduzir: boolean;
}) {
  const caixa = useRef<HTMLDivElement>(null);
  const refsO = useRef(new Map<string, HTMLElement>());
  const refsD = useRef(new Map<string, HTMLElement>());
  const [curvas, setCurvas] = useState<Curva[]>([]);

  const destinos = useMemo(() => {
    // tabelas do destino + as que serão criadas
    const nomes = destino.tabelas.map((t) => t.nome);
    for (const t of mapa.tabelas) if (t.acao === "criar_no_destino" && t.tabela_destino && !nomes.includes(t.tabela_destino)) nomes.push(t.tabela_destino);
    return nomes;
  }, [destino, mapa]);

  const recalcular = useCallback(() => {
    const base = caixa.current?.getBoundingClientRect();
    if (!base) return;
    const out: Curva[] = [];
    for (const t of mapa.tabelas) {
      if (t.acao === "ignorar" || !t.tabela_destino) continue;
      const a = refsO.current.get(t.tabela_origem)?.getBoundingClientRect();
      const b = refsD.current.get(t.tabela_destino)?.getBoundingClientRect();
      if (!a || !b) continue;
      const x1 = a.right - base.left, y1 = a.top + a.height / 2 - base.top;
      const x2 = b.left - base.left, y2 = b.top + b.height / 2 - base.top;
      const meio = (x1 + x2) / 2;
      out.push({ origem: t.tabela_origem, d: `M${x1},${y1} C${meio},${y1} ${meio},${y2} ${x2},${y2}`, confianca: t.confianca ?? 1 });
    }
    setCurvas(out);
  }, [mapa]);

  useLayoutEffect(() => {
    recalcular();
    const ro = new ResizeObserver(recalcular);
    if (caixa.current) ro.observe(caixa.current);
    window.addEventListener("resize", recalcular);
    return () => {
      ro.disconnect();
      window.removeEventListener("resize", recalcular);
    };
  }, [recalcular]);

  const comErro = new Set(problemas.filter((p) => p.nivel === "erro").map((p) => p.tabela));
  const origemDe = (n: string) => origem.tabelas.find((t) => t.nome === n);

  return (
    <div className="mm-diagrama" ref={caixa}>
      <svg className="mm-diagrama__linhas" aria-hidden="true">
        {curvas.map((c, i) => (
          <motion.path
            key={c.origem}
            d={c.d}
            className={`mm-linha${selecionada === c.origem ? " is-sel" : ""}${comErro.has(c.origem) ? " is-erro" : ""}${c.confianca < 0.8 ? " is-incerta" : ""}`}
            initial={reduzir ? false : { pathLength: 0, opacity: 0 }}
            animate={{ pathLength: 1, opacity: 1 }}
            transition={{ duration: reduzir ? 0 : 0.6, delay: reduzir ? 0 : i * 0.05, ease: "easeOut" }}
          />
        ))}
      </svg>
      <div className="mm-diagrama__coluna">
        <h2>Origem</h2>
        {mapa.tabelas.map((t) => {
          const to = origemDe(t.tabela_origem);
          return (
            <button
              key={t.tabela_origem}
              type="button"
              ref={(el) => {
                if (el) refsO.current.set(t.tabela_origem, el);
              }}
              className={`mm-tab${selecionada === t.tabela_origem ? " is-sel" : ""}${comErro.has(t.tabela_origem) ? " is-erro" : ""}${t.acao === "ignorar" ? " is-ignorada" : ""}`}
              onClick={() => onSelecionar(t.tabela_origem)}
            >
              <span className="mm-tab__nome">{t.tabela_origem}</span>
              <span className="mm-tab__meta">
                {to?.colunas.length ?? 0} col · {NOME_ACAO[t.acao]}
              </span>
            </button>
          );
        })}
      </div>
      <div className="mm-diagrama__coluna">
        <h2>Destino</h2>
        {destinos.map((n) => {
          const nova = !destino.tabelas.some((t) => t.nome === n);
          return (
            <div
              key={n}
              ref={(el) => {
                if (el) refsD.current.set(n, el);
              }}
              className={`mm-tab mm-tab--destino${nova ? " is-nova" : ""}`}
            >
              <span className="mm-tab__nome">{n}</span>
              <span className="mm-tab__meta">{nova ? "será criada" : `${destino.tabelas.find((t) => t.nome === n)?.colunas.length ?? 0} col`}</span>
            </div>
          );
        })}
      </div>
    </div>
  );
}

// ---------------------------------------------------------------- uma tabela

const TRANSFORMACOES: Transformacao[] = ["nenhuma", "converter_tipo", "charset", "aparar", "valor_padrao", "constante", "data_formato"];

function PainelTabela({
  mt,
  origem,
  destino,
  problemas,
  editavel,
  onAlterar,
  onFechar,
}: {
  mt: MapTabela;
  origem: Esquema;
  destino: Esquema;
  problemas: Problema[];
  editavel: boolean;
  onAlterar: (f: (t: MapTabela) => MapTabela) => void;
  onFechar: () => void;
}) {
  const to = origem.tabelas.find((t) => t.nome === mt.tabela_origem);
  const td: TabelaEsquema | undefined = destino.tabelas.find((t) => t.nome === mt.tabela_destino);
  const colunasDestino = td?.colunas.map((c) => c.nome) ?? to?.colunas.map((c) => c.nome.toLowerCase()) ?? [];
  const porOrigem = new Map(mt.colunas.filter((c) => c.coluna_origem).map((c) => [c.coluna_origem!, c]));
  const naoMapeadasObrig = td?.colunas.filter((c) => !c.nulavel && !c.padrao && !c.identidade && !mt.colunas.some((m) => m.coluna_destino === c.nome)) ?? [];

  const setColuna = (origemCol: string, novo: Partial<MapColuna> | null) =>
    onAlterar((t) => {
      const resto = t.colunas.filter((c) => c.coluna_origem !== origemCol);
      if (novo === null) return { ...t, colunas: resto };
      const atual = t.colunas.find((c) => c.coluna_origem === origemCol) ?? { coluna_origem: origemCol, coluna_destino: "", transformacao: "nenhuma" as Transformacao };
      return { ...t, colunas: [...resto, { ...atual, ...novo, origem: "usuario", confianca: 1 }] };
    });

  return (
    <div className="mm-painel">
      <div className="mm-painel__cabeca">
        <h2>
          {mt.tabela_origem} <span className="mm-dica">→</span> {mt.tabela_destino ?? "—"}
        </h2>
        <div className="mm-painel__controles">
          <select
            className="field"
            value={mt.acao}
            disabled={!editavel}
            onChange={(e) => onAlterar((t) => ({ ...t, acao: e.target.value as MapTabela["acao"] }))}
            aria-label="Ação"
          >
            {Object.entries(NOME_ACAO).map(([k, v]) => (
              <option key={k} value={k}>
                {v}
              </option>
            ))}
          </select>
          {mt.acao === "copiar" && (
            <select
              className="field"
              value={mt.tabela_destino ?? ""}
              disabled={!editavel}
              onChange={(e) => onAlterar((t) => ({ ...t, tabela_destino: e.target.value }))}
              aria-label="Tabela de destino"
            >
              {destino.tabelas.map((t) => (
                <option key={t.nome} value={t.nome}>
                  {t.nome}
                </option>
              ))}
            </select>
          )}
          <Button variant="ghost" onClick={onFechar}>
            Fechar
          </Button>
        </div>
      </div>
      {problemas.length > 0 && (
        <ul className="mm-painel__probs">
          {problemas.map((p, i) => (
            <li key={i} className={`mm-problema mm-problema--${p.nivel}`}>
              {p.coluna ? <strong>{p.coluna}: </strong> : null}
              {p.mensagem}
            </li>
          ))}
        </ul>
      )}
      {mt.acao !== "ignorar" && (
        <table className="mm-colunas">
          <thead>
            <tr>
              <th>Coluna de origem</th>
              <th>Tipo</th>
              <th>Vai para</th>
              <th>Transformação</th>
              <th>Confiança</th>
            </tr>
          </thead>
          <tbody>
            {to?.colunas.map((c) => {
              const m = porOrigem.get(c.nome);
              return (
                <tr key={c.nome} className={m ? "" : "is-vazia"}>
                  <td>
                    <code>{c.nome}</code>
                    {to.chave_primaria?.includes(c.nome) && <span className="mm-pk">PK</span>}
                  </td>
                  <td className="mm-dica">{c.tipo_nativo}</td>
                  <td>
                    <select
                      className="field"
                      value={m?.coluna_destino ?? ""}
                      disabled={!editavel}
                      onChange={(e) => setColuna(c.nome, e.target.value ? { coluna_destino: e.target.value } : null)}
                      aria-label={`Destino de ${c.nome}`}
                    >
                      <option value="">— não migrar —</option>
                      {colunasDestino.map((n) => (
                        <option key={n} value={n}>
                          {n}
                        </option>
                      ))}
                    </select>
                    {m?.alerta && (
                      <span className="mm-alerta" title={m.alerta}>
                        <AlertTriangle size={13} aria-hidden={true} /> {m.alerta}
                      </span>
                    )}
                  </td>
                  <td>
                    {m && (
                      <select
                        className="field"
                        value={m.transformacao}
                        disabled={!editavel}
                        onChange={(e) => setColuna(c.nome, { transformacao: e.target.value as Transformacao })}
                        aria-label={`Transformação de ${c.nome}`}
                      >
                        {TRANSFORMACOES.map((t) => (
                          <option key={t} value={t}>
                            {NOME_TRANSFORMACAO[t]}
                          </option>
                        ))}
                      </select>
                    )}
                    {m && <Parametros coluna={m} editavel={editavel} onMudar={(parametros) => setColuna(c.nome, { parametros })} />}
                  </td>
                  <td>{m?.confianca !== undefined && <Confianca valor={m.confianca} origem={m.origem} />}</td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
      {naoMapeadasObrig.length > 0 && (
        <p className="mm-alerta mm-alerta--forte">
          <AlertTriangle size={14} aria-hidden={true} /> Obrigatórias no destino e sem origem:{" "}
          {naoMapeadasObrig.map((c) => c.nome).join(", ")} — escolha uma coluna de origem para elas.
        </p>
      )}
    </div>
  );
}

function Confianca({ valor, origem }: { valor: number; origem?: string }) {
  const pct = Math.round(valor * 100);
  return (
    <span className="mm-conf" title={`origem: ${origem ?? "—"}`}>
      <span className="mm-conf__barra" style={{ inlineSize: `${pct}%` }} />
      <span className="mm-conf__num">{pct}%</span>
    </span>
  );
}
