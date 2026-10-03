// Upgrade Firebird 2.x/3/4 → 5 (ARQUITETURA §9.5): diagnóstico ("o que vai quebrar?") com
// ensaio de metadados no FB5, upgrade por backup/restore com validação e descarte do
// banco novo. O banco original nunca é alterado.
import { useCallback, useEffect, useMemo, useState } from "react";
import { motion, useReducedMotion } from "motion/react";
import { AlertOctagon, AlertTriangle, ArrowUpCircle, Download, Eye, Info, Stethoscope, Trash2 } from "lucide-react";
import { Badge, Button } from "../../components";
import { useToast } from "../../components/Toast";
import { TarefaAoVivo } from "../../pages/Agentes";
import {
  atualizarFirebird,
  descartarUpgrade,
  diagnosticarUpgrade,
  listarAgentes,
  podeOperar,
  situacaoUpgrade,
  type AchadoUpgrade,
  type Agente,
  type DiagnosticoUpgrade,
  type ExecucaoUpgrade,
  type ProjetoMigracao,
  type ResumoUpgrade,
  type SituacaoUpgrade,
  type Tarefa,
} from "../../api";
import { mensagemErro } from "./util";

const NIVEL: Record<AchadoUpgrade["nivel"], { rotulo: string; Icone: typeof Info }> = {
  bloqueio: { rotulo: "Bloqueia o upgrade", Icone: AlertOctagon },
  risco: { rotulo: "Muda o comportamento", Icone: AlertTriangle },
  info: { rotulo: "Vale saber", Icone: Info },
};

const CATEGORIA: Record<string, string> = {
  versao: "versão",
  palavra_reservada: "palavra reservada",
  udf: "UDF",
  data_hora: "data e hora",
  not_null: "nulo em NOT NULL",
  chave_estrangeira: "registro órfão",
  unicidade: "chave repetida",
  charset: "charset",
  usuarios: "usuários",
  corrupcao: "páginas",
  ensaio: "ensaio no FB5",
};

const TIPO: Record<ExecucaoUpgrade["tipo"], string> = { diagnosticar: "Diagnóstico", atualizar: "Upgrade", descartar: "Descarte" };
const CHARSETS = ["", "WIN1252", "WIN1250", "ISO8859_1", "ISO8859_15", "DOS850", "DOS437", "UTF8"];

function tamanho(b: number) {
  if (b >= 1 << 30) return `${(b / (1 << 30)).toFixed(1)} GiB`;
  if (b >= 1 << 20) return `${(b / (1 << 20)).toFixed(1)} MiB`;
  return `${Math.max(1, Math.round(b / 1024))} KiB`;
}

function duracao(s: number) {
  if (s < 120) return `${s} s`;
  if (s < 7200) return `${Math.round(s / 60)} min`;
  return `${(s / 3600).toFixed(1)} h`;
}

function baixar(nome: string, texto: string) {
  const url = URL.createObjectURL(new Blob([texto], { type: "text/plain;charset=utf-8" }));
  const a = document.createElement("a");
  a.href = url;
  a.download = nome;
  a.click();
  URL.revokeObjectURL(url);
}

export function Upgrade({ projeto }: { projeto: ProjetoMigracao }) {
  const toast = useToast();
  const reduzir = useReducedMotion();
  const [sit, setSit] = useState<SituacaoUpgrade | null>(null);
  const [agentes, setAgentes] = useState<Agente[]>([]);
  const [agente, setAgente] = useState("");
  const [validar, setValidar] = useState(false);
  const [charsetFix, setCharsetFix] = useState("");
  const [aoVivo, setAoVivo] = useState<Tarefa | null>(null);
  const [busy, setBusy] = useState(false);

  const carregar = useCallback(async () => {
    try {
      const [s, ags] = await Promise.all([situacaoUpgrade(projeto.id), listarAgentes()]);
      setSit(s);
      if (s.diagnostico?.charset_fix) setCharsetFix((x) => x || s.diagnostico!.charset_fix!);
      const aptos = ags.filter((a) => !a.revogado && a.capacidades?.includes("firebird.diagnostico"));
      setAgentes(aptos);
      setAgente((x) => x || aptos.find((a) => a.estado === "online")?.id || aptos[0]?.id || "");
    } catch (e) {
      toast.error(mensagemErro(e));
    }
  }, [projeto.id, toast]);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  useEffect(() => {
    if (!sit?.execucoes.some((e) => ["na_fila", "enviada", "executando", "pausada"].includes(e.tarefa.estado))) return;
    const t = window.setInterval(() => void carregar(), 3000);
    return () => window.clearInterval(t);
  }, [sit, carregar]);

  const disparar = async (fn: () => Promise<ExecucaoUpgrade>, ok: string) => {
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

  const ultimoUpgrade = sit?.execucoes.find((e) => e.tipo === "atualizar" && e.tarefa.resumo);
  const resumo = ultimoUpgrade?.tarefa.resumo as ResumoUpgrade | undefined;
  const d = sit?.diagnostico;
  const opera = podeOperar();

  return (
    <section className="mm-up">
      <header className="mm-editor__cabeca">
        <div>
          <a href="#/migracao" className="mm-dica">
            ← Projetos
          </a>
          <h1>{projeto.nome}</h1>
          <p className="mm-dica">Upgrade de versão: Firebird → 5. O banco original nunca é alterado; o novo nasce num arquivo separado.</p>
        </div>
      </header>

      {sit && !sit.tem_destino && (
        <p className="mm-alerta mm-alerta--forte">
          <AlertTriangle size={14} aria-hidden={true} /> Sem destino: o diagnóstico roda, mas o ensaio e o upgrade precisam de uma conexão com o
          servidor Firebird 5 (o “banco” dela é o caminho do arquivo novo, e a opção “diretório compartilhado” é a pasta que os dois servidores
          enxergam).
        </p>
      )}

      {opera && (
        <div className="mm-exec__acoes">
          {agentes.length === 0 ? (
            <p className="mm-alerta mm-alerta--forte">
              <AlertTriangle size={14} aria-hidden={true} /> Nenhum agente liberou <code>firebird.diagnostico</code> em <code>canal.tarefas_permitidas</code>.
            </p>
          ) : (
            <select className="field" value={agente} onChange={(e) => setAgente(e.target.value)} aria-label="Agente que roda o upgrade">
              {agentes.map((a) => (
                <option key={a.id} value={a.id} disabled={a.estado === "offline"}>
                  {a.hostname || a.id} {a.estado !== "online" ? `(${a.estado})` : ""}
                </option>
              ))}
            </select>
          )}
          <label className="mm-param mm-param--check" title="gfix -v -full em modo só leitura; no Firebird 2.x exige o banco sem outras conexões">
            <input type="checkbox" checked={validar} onChange={(e) => setValidar(e.target.checked)} />
            <span>validar páginas</span>
          </label>
          <Button onClick={() => disparar(() => diagnosticarUpgrade(projeto.id, agente, validar), "Diagnóstico enviado ao agente.")} disabled={busy || !agente || sit?.ocupado}>
            <Stethoscope size={16} aria-hidden={true} /> Diagnosticar
          </Button>
          <label className="mm-param" title="Charset em que o texto foi realmente gravado (FIX_FSS_DATA/METADATA no restore)">
            <span>corrigir charset</span>
            <select className="field" value={charsetFix} onChange={(e) => setCharsetFix(e.target.value)}>
              {CHARSETS.map((c) => (
                <option key={c} value={c}>
                  {c || "não corrigir"}
                </option>
              ))}
            </select>
          </label>
          <Button
            variant="primary"
            onClick={() => disparar(() => atualizarFirebird(projeto.id, agente, charsetFix), "Upgrade enviado ao agente.")}
            disabled={busy || !agente || !sit?.pode_atualizar}
            title={sit?.pode_atualizar ? "Backup → restore no FB5 → validação (pede confirmação de identidade)" : sit?.motivo}
          >
            <ArrowUpCircle size={16} aria-hidden={true} /> Atualizar para o Firebird 5
          </Button>
          {sit?.pode_descartar && (
            <Button variant="ghost" onClick={() => disparar(() => descartarUpgrade(projeto.id, sit.pode_descartar!), "Descarte enviado.")} disabled={busy || sit.ocupado}>
              <Trash2 size={16} aria-hidden={true} /> Descartar banco novo
            </Button>
          )}
        </div>
      )}
      {sit?.motivo && !sit.ocupado && <p className="mm-dica">{sit.motivo}</p>}

      {d && <Laudo d={d} reduzir={!!reduzir} />}
      {resumo && <ResultadoUpgrade r={resumo} descartado={!!sit?.descartados[resumo.execucao]} />}

      {sit && sit.execucoes.length > 0 && (
        <ul className="mm-historico" aria-label="Histórico">
          {sit.execucoes.map((e) => (
            <li key={e.tarefa_id}>
              <Badge state={e.tarefa.estado === "sucesso" ? "ok" : e.tarefa.estado === "falha" ? "crit" : "info"}>{e.tarefa.estado.replace("_", " ")}</Badge>
              <strong>{TIPO[e.tipo]}</strong>
              <span className="mm-dica">
                {new Date(e.criada_em).toLocaleString("pt-BR")} · {e.tarefa.iniciada_por}
              </span>
              {e.tarefa.erro && <span className="mm-historico__erro">{e.tarefa.erro}</span>}
              <Button variant="ghost" onClick={() => setAoVivo(e.tarefa)} aria-label={`Acompanhar ${TIPO[e.tipo]}`}>
                <Eye size={16} aria-hidden={true} />
              </Button>
            </li>
          ))}
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

// Medidor: arco de 0 a 100 que se desenha ao abrir; a cor diz o risco.
function Medidor({ nota, risco, reduzir }: { nota: number; risco: string; reduzir: boolean }) {
  const arco = "M 10 60 A 50 50 0 0 1 110 60";
  return (
    <svg className={`mm-medidor mm-medidor--${risco}`} viewBox="0 0 120 70" role="img" aria-label={`Risco ${risco}, nota ${nota} de 100`}>
      <path d={arco} className="mm-medidor__trilho" pathLength={1} />
      <motion.path
        d={arco}
        className="mm-medidor__valor"
        pathLength={1}
        initial={reduzir ? false : { pathLength: 0 }}
        animate={{ pathLength: Math.max(nota, 2) / 100 }}
        transition={{ duration: reduzir ? 0 : 1.1, ease: [0.22, 1, 0.36, 1] }}
      />
      <text x="60" y="56" textAnchor="middle" className="mm-medidor__nota">
        {nota}
      </text>
    </svg>
  );
}

function Laudo({ d, reduzir }: { d: DiagnosticoUpgrade; reduzir: boolean }) {
  const grupos = useMemo(() => {
    const g: Record<string, AchadoUpgrade[]> = { bloqueio: [], risco: [], info: [] };
    for (const a of d.achados ?? []) g[a.nivel]?.push(a);
    return g;
  }, [d.achados]);
  return (
    <div className={`mm-relatorio${d.bloqueios > 0 ? " is-bloqueado" : ""}`}>
      <div className="mm-laudo__topo">
        <Medidor nota={d.nota} risco={d.risco} reduzir={reduzir} />
        <div className="mm-relatorio__placar">
          <div className="mm-numero">
            <strong>Firebird {d.versao}</strong>
            <span>
              ODS {d.ods} · dialeto {d.dialeto} · {d.charset}
            </span>
          </div>
          <div className="mm-numero">
            <strong>{tamanho(d.tamanho_bytes)}</strong>
            <span>
              {d.tabelas} tabelas · {d.procedures} procedures · {d.triggers} triggers
            </span>
          </div>
          <div className={`mm-numero${d.bloqueios > 0 ? " is-destaque" : ""}`}>
            <strong>{d.bloqueios}</strong>
            <span>bloqueio(s)</span>
          </div>
          <div className="mm-numero">
            <strong>~{duracao(d.parada_estimada_s)}</strong>
            <span>parada estimada</span>
          </div>
        </div>
      </div>
      <p className="mm-dica">
        Ensaio no Firebird 5:{" "}
        {d.ensaio?.feito ? (d.ensaio.ok ? "metadados restauraram sem erro — procedures e triggers compilam." : "falhou (veja os bloqueios).") : "não feito (sem destino)."}
      </p>
      {(["bloqueio", "risco", "info"] as const).map((n) =>
        grupos[n].length === 0 ? null : (
          <div key={n} className={`mm-achados mm-achados--${n}`}>
            <h3>
              {(() => {
                const I = NIVEL[n].Icone;
                return <I size={16} aria-hidden={true} />;
              })()}{" "}
              {NIVEL[n].rotulo} ({grupos[n].length})
            </h3>
            <ul>
              {grupos[n].map((a, i) => (
                <li key={i}>
                  <span className="mm-chip">{CATEGORIA[a.categoria] ?? a.categoria}</span>
                  {a.objeto && <code>{a.objeto}</code>} {a.mensagem}
                </li>
              ))}
            </ul>
          </div>
        ),
      )}
      <div className="mm-exec__acoes">
        <Button onClick={() => baixar("fix.sql", d.fix_sql)}>
          <Download size={16} aria-hidden={true} /> Baixar fix.sql
        </Button>
        <span className="mm-dica">O que muda ou apaga dado sai comentado: revise e rode na origem, depois de um backup.</span>
      </div>
    </div>
  );
}

function ResultadoUpgrade({ r, descartado }: { r: ResumoUpgrade; descartado: boolean }) {
  const ruins = [...(r.tabelas ?? []), ...(r.geradores ?? [])].filter((c) => !c.ok);
  return (
    <div className={`mm-relatorio${r.tudo_confere ? "" : " is-bloqueado"}`}>
      <div className="mm-relatorio__placar">
        <div className="mm-numero">
          <strong>{r.versao_nova ? `Firebird ${r.versao_nova}` : "—"}</strong>
          <span>ODS {r.ods_nova || "—"}</span>
        </div>
        <div className={`mm-numero${r.tudo_confere ? "" : " is-destaque"}`}>
          <strong>{r.tudo_confere ? "confere" : "não confere"}</strong>
          <span>
            {r.tabelas?.length ?? 0} tabelas e {r.geradores?.length ?? 0} geradores comparados
          </span>
        </div>
      </div>
      <p className="mm-dica">
        Banco novo: <code>{r.novo_banco}</code>
        {descartado ? " — descartado (shutdown completo)" : ""}. Backup: <code>{r.backup}</code>.
      </p>
      {ruins.length > 0 && (
        <ul className="mm-amostras">
          {ruins.map((c) => (
            <li key={c.tabela}>
              <code>{c.tabela}</code>: {c.antes} antes, {c.depois} depois
            </li>
          ))}
        </ul>
      )}
      {r.avisos?.map((a) => (
        <p key={a} className="mm-dica">
          {a}
        </p>
      ))}
    </div>
  );
}
