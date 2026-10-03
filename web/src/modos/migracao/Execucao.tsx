// Execução da migração (ARQUITETURA §9.4): simular (dry-run obrigatório) → executar em lotes
// → reverter. A trilha no topo mostra onde o projeto está; o histórico guarda cada
// tarefa e abre a tela ao vivo (progresso, pausar/retomar/cancelar).
import { useCallback, useEffect, useMemo, useState } from "react";
import { AnimatePresence, motion, useReducedMotion } from "motion/react";
import { AlertTriangle, CheckCircle2, Eye, FlaskConical, Play, RotateCcw, Undo2 } from "lucide-react";
import { Badge, Button } from "../../components";
import { useToast } from "../../components/Toast";
import { TarefaAoVivo } from "../../pages/Agentes";
import {
  executarMigracao,
  execucoesProjeto,
  listarAgentes,
  podeOperar,
  reverterMigracao,
  simularMigracao,
  verificarMigracao,
  type Agente,
  type ExecucaoMigracao,
  type RelatorioSimulacao,
  type ResumoExecucao,
  type ResumoVerificacao,
  type SituacaoExecucoes,
  type Tarefa,
  type VersaoMapeamento,
} from "../../api";
import { mensagemErro } from "./util";
import { Verificacao } from "./Verificacao";

const ESTRATEGIA: Record<string, string> = {
  apagar_tabela: "cria a tabela · reverter apaga",
  esvaziar: "estava vazia · staging e troca · reverter esvazia",
  staging: "tem dados · cópia prévia, staging e troca · reverter restaura",
};

const VIOLACAO: Record<string, string> = {
  nulo: "obrigatória sem valor",
  tamanho: "texto grande demais",
  numero: "número não cabe",
  tipo: "tipo incompatível",
  texto_invalido: "texto inválido",
  unica: "chave repetida",
  estrangeira: "FK sem registro pai",
  transformacao: "transformação falhou",
};

const PERDA: Record<string, string> = {
  arredonda_casas: "arredonda casas",
  descarta_hora: "descarta a hora",
  suspeita_charset: "acentos suspeitos — ligue “reparar”",
};

const TIPO: Record<ExecucaoMigracao["tipo"], string> = {
  simular: "Simulação",
  executar: "Execução",
  reverter: "Reversão",
  verificar: "Verificação",
};

const ABERTOS = ["na_fila", "enviada", "executando", "pausada"];

function corEstado(e: string): "ok" | "crit" | "warn" | "info" | "neutral" {
  if (e === "sucesso") return "ok";
  if (e === "falha" || e === "recusada") return "crit";
  if (e === "pausada" || e === "cancelada") return "warn";
  return "info";
}

export function Execucao({ projetoId, versao, sujo }: { projetoId: string; versao: VersaoMapeamento; sujo: boolean }) {
  const toast = useToast();
  const reduzir = useReducedMotion();
  const [sit, setSit] = useState<SituacaoExecucoes | null>(null);
  const [agentes, setAgentes] = useState<Agente[]>([]);
  const [agente, setAgente] = useState("");
  const [aoVivo, setAoVivo] = useState<Tarefa | null>(null);
  const [busy, setBusy] = useState(false);

  const carregar = useCallback(async () => {
    try {
      const [s, ags] = await Promise.all([execucoesProjeto(projetoId), listarAgentes()]);
      setSit(s);
      const aptos = ags.filter((a) => !a.revogado && a.capacidades?.includes("migracao.simular"));
      setAgentes(aptos);
      setAgente((x) => x || aptos.find((a) => a.estado === "online")?.id || aptos[0]?.id || "");
    } catch (e) {
      toast.error(mensagemErro(e));
    }
  }, [projetoId, toast]);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  // Enquanto algo roda, a lista se atualiza sozinha (a tela ao vivo é o SSE).
  useEffect(() => {
    if (!sit?.execucoes.some((e) => ABERTOS.includes(e.tarefa.estado))) return;
    const t = window.setInterval(() => void carregar(), 3000);
    return () => window.clearInterval(t);
  }, [sit, carregar]);

  const disparar = async (fn: () => Promise<ExecucaoMigracao>, ok: string) => {
    setBusy(true);
    try {
      const e = await fn();
      toast.success(ok);
      setAoVivo(e.tarefa);
      await carregar();
    } catch (e) {
      toast.error(mensagemErro(e));
    } finally {
      setBusy(false);
    }
  };

  // Relatório: o da simulação desta versão (o mais recente com sucesso).
  const relatorio = useMemo<RelatorioSimulacao | null>(() => {
    const s = sit?.execucoes.find((e) => e.tipo === "simular" && e.versao === versao.versao && e.tarefa.estado === "sucesso");
    return (s?.tarefa.resumo as RelatorioSimulacao | undefined) ?? null;
  }, [sit, versao.versao]);

  const incompletas = Object.values(sit?.incompletas ?? {});
  const concluida = sit?.execucoes.find((e) => e.tipo === "executar" && sit.concluidas[e.execucao] && e.tarefa.estado === "sucesso");
  // Verificação: a execução concluída — ou, se nenhuma concluiu, a última que deixou
  // resumo (inclusive a que falhou na conferência, para mostrar onde divergiu).
  const alvoVerif =
    concluida ?? sit?.execucoes.find((e) => e.tipo === "executar" && !!e.tarefa.resumo && !sit.revertidas[e.execucao]);
  const ultimaVerif = alvoVerif && sit?.execucoes.find((e) => e.tipo === "verificar" && e.execucao === alvoVerif.execucao);
  const agenteVerifica = !!agentes.find((a) => a.id === agente)?.capacidades?.includes("migracao.verificar");
  const opera = podeOperar();
  const simulavel = (versao.estado === "valido" || versao.estado === "aprovado") && !sujo;

  const passos = [
    { nome: "Mapear", feito: versao.estado !== "rascunho" },
    { nome: "Simular", feito: !!relatorio && relatorio.bloqueantes === 0 },
    { nome: "Aprovar", feito: versao.estado === "aprovado" },
    { nome: "Executar", feito: !!concluida },
  ];
  const ativo = passos.findIndex((p) => !p.feito);

  return (
    <section className="mm-exec" aria-labelledby="mm-exec-titulo">
      <div className="mm-exec__cabeca">
        <div>
          <h2 id="mm-exec-titulo">Execução</h2>
          <p className="mm-dica">
            Simular lê tudo e confere tudo sem gravar nada. Executar só libera depois de uma simulação sem bloqueantes da versão
            aprovada. Cada lote é uma transação; reverter desfaz pelo manifesto, numa transação só.
          </p>
        </div>
        <ol className="mm-trilha" aria-label="Etapas">
          {passos.map((p, i) => (
            <li key={p.nome} className={`mm-trilha__passo${p.feito ? " is-feito" : ""}${i === ativo ? " is-ativo" : ""}`}>
              <motion.span
                className="mm-trilha__bola"
                initial={false}
                animate={{ scale: i === ativo && !reduzir ? [1, 1.15, 1] : 1 }}
                transition={i === ativo && !reduzir ? { repeat: Infinity, duration: 1.8 } : { duration: 0 }}
              >
                {p.feito ? <CheckCircle2 size={14} aria-hidden={true} /> : i + 1}
              </motion.span>
              {p.nome}
            </li>
          ))}
        </ol>
      </div>

      {opera && (
        <div className="mm-exec__acoes">
          {agentes.length === 0 ? (
            <p className="mm-alerta mm-alerta--forte">
              <AlertTriangle size={14} aria-hidden={true} /> Nenhum agente liberou <code>migracao.simular</code> em{" "}
              <code>canal.tarefas_permitidas</code>.
            </p>
          ) : (
            <select className="field" value={agente} onChange={(e) => setAgente(e.target.value)} aria-label="Agente que roda a migração">
              {agentes.map((a) => (
                <option key={a.id} value={a.id} disabled={a.estado === "offline"}>
                  {a.hostname || a.id} {a.estado !== "online" ? `(${a.estado})` : ""}
                </option>
              ))}
            </select>
          )}
          <Button
            onClick={() => disparar(() => simularMigracao(projetoId, agente, versao.versao), "Simulação enviada ao agente.")}
            disabled={busy || !agente || !simulavel}
            title={sujo ? "Salve a versão antes de simular" : simulavel ? "Dry-run: nada é gravado" : "A versão tem erros"}
          >
            <FlaskConical size={16} aria-hidden={true} /> Simular v{versao.versao}
          </Button>
          <Button
            variant="primary"
            onClick={() => disparar(() => executarMigracao(projetoId, agente), "Execução enviada ao agente.")}
            disabled={busy || !agente || !sit?.pode_executar}
            title={sit?.pode_executar ? "Pede confirmação de identidade" : "Aprove a versão e simule sem bloqueantes antes"}
          >
            <Play size={16} aria-hidden={true} /> Executar v{sit?.versao_aprovada ?? "—"}
          </Button>
          {incompletas.map((tid) => (
            <span key={tid} className="mm-exec__incompleta">
              <Button onClick={() => disparar(() => executarMigracao(projetoId, agente, tid), "Retomando do último lote confirmado.")} disabled={busy || sit?.ocupado}>
                <RotateCcw size={16} aria-hidden={true} /> Retomar
              </Button>
              <Button variant="ghost" onClick={() => disparar(() => reverterMigracao(projetoId, tid), "Reversão enviada.")} disabled={busy || sit?.ocupado}>
                <Undo2 size={16} aria-hidden={true} /> Reverter
              </Button>
            </span>
          ))}
          {concluida && (
            <Button variant="ghost" onClick={() => disparar(() => reverterMigracao(projetoId, concluida.tarefa_id), "Reversão enviada.")} disabled={busy || sit?.ocupado}>
              <Undo2 size={16} aria-hidden={true} /> Reverter execução concluída
            </Button>
          )}
        </div>
      )}

      {alvoVerif && (
        <Verificacao
          execucao={alvoVerif}
          verificacao={ultimaVerif}
          podeVerificar={!!concluida && concluida.tarefa_id === alvoVerif.tarefa_id && !sit?.ocupado}
          agenteVerifica={agenteVerifica}
          opera={opera}
          busy={busy}
          onVerificar={() =>
            disparar(() => verificarMigracao(projetoId, alvoVerif.tarefa_id, agente), "Verificação enviada ao agente (só leitura).")
          }
          onAcompanhar={setAoVivo}
        />
      )}

      {relatorio && <Relatorio rel={relatorio} reduzir={!!reduzir} />}

      {sit && sit.execucoes.length > 0 && (
        <ul className="mm-historico" aria-label="Histórico">
          <AnimatePresence initial={false}>
            {sit.execucoes.map((e) => (
              <motion.li key={e.tarefa_id} layout={!reduzir} initial={reduzir ? false : { opacity: 0, y: -6 }} animate={{ opacity: 1, y: 0 }}>
                <Badge state={corEstado(e.tarefa.estado)}>{e.tarefa.estado.replace("_", " ")}</Badge>
                <strong>{TIPO[e.tipo]}</strong>
                <span className="mm-dica">
                  v{e.versao} · {new Date(e.criada_em).toLocaleString("pt-BR")} · {e.tarefa.iniciada_por}
                  {sit.revertidas[e.execucao] && e.tipo === "executar" ? " · revertida" : ""}
                </span>
                <ResumoCurto e={e} />
                <Button variant="ghost" onClick={() => setAoVivo(e.tarefa)} aria-label={`Acompanhar ${TIPO[e.tipo]}`}>
                  <Eye size={16} aria-hidden={true} />
                </Button>
              </motion.li>
            ))}
          </AnimatePresence>
        </ul>
      )}

      {aoVivo && (
        <TarefaAoVivo
          tarefa={aoVivo}
          onClose={() => {
            setAoVivo(null);
            void carregar();
          }}
        />
      )}
    </section>
  );
}

function ResumoCurto({ e }: { e: ExecucaoMigracao }) {
  if (e.tarefa.erro) return <span className="mm-historico__erro">{e.tarefa.erro}</span>;
  if (!e.tarefa.resumo) return null;
  if (e.tipo === "simular") {
    const r = e.tarefa.resumo as RelatorioSimulacao;
    return (
      <span className="mm-dica">
        {r.total_linhas.toLocaleString("pt-BR")} linhas · {r.bloqueantes.toLocaleString("pt-BR")} bloqueantes ·{" "}
        {r.perdas.toLocaleString("pt-BR")} avisos
      </span>
    );
  }
  if (e.tipo === "executar") {
    const r = e.tarefa.resumo as ResumoExecucao;
    const chaves = r.tabelas?.every((t) => t.checksum_ok);
    const conteudo = r.tabelas?.every((t) => t.conteudo_ok === true);
    return (
      <span className="mm-dica">
        {r.linhas.toLocaleString("pt-BR")} linhas gravadas ·{" "}
        {conteudo ? "contagem, chaves e conteúdo conferidos" : chaves ? "contagem e chaves conferidas; conteúdo não conferido" : "conferência pendente"}
      </span>
    );
  }
  if (e.tipo === "verificar") {
    const r = e.tarefa.resumo as ResumoVerificacao;
    return (
      <span className="mm-dica">
        {r.linhas.toLocaleString("pt-BR")} linhas comparadas ·{" "}
        {r.conteudo_ok ? "origem e destino idênticos" : `${r.divergentes.toLocaleString("pt-BR")} divergentes · ${r.ausentes.toLocaleString("pt-BR")} ausentes`}
        {r.texto_ok === true && ` · ${(r.colunas_texto ?? 0).toLocaleString("pt-BR")} colunas conferidas pelos próprios bancos`}
        {r.tabelas?.some((t) => (t.texto?.divergentes ?? 0) + (t.texto?.ausentes ?? 0) > 0) &&
          " · os próprios bancos discordam no texto (veja a Verificação)"}
      </span>
    );
  }
  return null;
}

function Relatorio({ rel, reduzir }: { rel: RelatorioSimulacao; reduzir: boolean }) {
  const [aberta, setAberta] = useState<string | null>(null);
  const min = Math.max(1, Math.round(rel.tempo_estimado_s / 60));
  return (
    <div className={`mm-relatorio${rel.bloqueantes > 0 ? " is-bloqueado" : ""}`}>
      <div className="mm-relatorio__placar">
        <Numero rotulo="linhas conferidas" valor={rel.total_linhas} reduzir={reduzir} />
        <Numero rotulo="seriam recusadas" valor={rel.bloqueantes} destaque={rel.bloqueantes > 0} reduzir={reduzir} />
        <Numero rotulo="avisos de perda" valor={rel.perdas} reduzir={reduzir} />
        <div className="mm-numero">
          <strong>~{rel.tempo_estimado_s < 60 ? `${rel.tempo_estimado_s}s` : `${min} min`}</strong>
          <span>tempo estimado</span>
        </div>
      </div>
      <table className="mm-colunas">
        <thead>
          <tr>
            <th>#</th>
            <th>Tabela</th>
            <th>Estratégia</th>
            <th>Linhas</th>
            <th>Problemas</th>
          </tr>
        </thead>
        <tbody>
          {rel.tabelas.map((t, i) => (
            <tr key={t.origem}>
              <td className="mm-dica">{i + 1}</td>
              <td>
                <code>{t.origem}</code> → <code>{t.destino}</code>
              </td>
              <td className="mm-dica">{ESTRATEGIA[t.estrategia] ?? t.estrategia}</td>
              <td>{t.linhas.toLocaleString("pt-BR")}</td>
              <td>
                {Object.entries(t.violacoes ?? {}).map(([k, n]) => (
                  <span key={k} className="mm-chip mm-chip--erro">
                    {n} {VIOLACAO[k] ?? k}
                  </span>
                ))}
                {Object.entries(t.perdas ?? {}).map(([k, n]) => (
                  <span key={k} className="mm-chip mm-chip--aviso">
                    {n} {PERDA[k] ?? k}
                  </span>
                ))}
                {!t.violacoes && !t.perdas && <span className="mm-chip mm-chip--ok">ok</span>}
                {t.amostras && t.amostras.length > 0 && (
                  <button type="button" className="mm-link" onClick={() => setAberta(aberta === t.origem ? null : t.origem)}>
                    {aberta === t.origem ? "esconder exemplos" : "ver exemplos"}
                  </button>
                )}
                {aberta === t.origem && (
                  <ul className="mm-amostras">
                    {t.amostras?.map((a, j) => (
                      <li key={j}>
                        {a.chave && <code>chave {a.chave}</code>} {a.mensagem}
                      </li>
                    ))}
                  </ul>
                )}
              </td>
            </tr>
          ))}
        </tbody>
      </table>
      <p className="mm-dica">Os exemplos apontam a linha pela chave — o conteúdo dos dados nunca sai do servidor do agente.</p>
    </div>
  );
}

function Numero({ rotulo, valor, destaque, reduzir }: { rotulo: string; valor: number; destaque?: boolean; reduzir: boolean }) {
  return (
    <div className={`mm-numero${destaque ? " is-destaque" : ""}`}>
      <motion.strong key={valor} initial={reduzir ? false : { opacity: 0, y: 6 }} animate={{ opacity: 1, y: 0 }}>
        {valor.toLocaleString("pt-BR")}
      </motion.strong>
      <span>{rotulo}</span>
    </div>
  );
}
