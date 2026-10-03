// Verificação (ARQUITETURA §9.4): a prova de que a migração está certa. Mostra, em uma
// linha grande, o veredito do conteúdo e, por tabela, contagem ✓ · chaves ✓ ·
// conteúdo ✓ (n colunas) — ou o que ficou de fora (com o motivo) e onde diverge
// (pela chave da linha e o nome das colunas; o valor nunca sai do agente).
// "Verificar de novo" pede ao agente a comparação origem × destino linha a linha.
import { useState } from "react";
import { CheckCircle2, CircleAlert, CircleHelp, Eye, OctagonAlert, ShieldCheck } from "lucide-react";
import { Badge, Button } from "../../components";
import type { ExecucaoMigracao, ResumoExecucao, ResumoVerificacao, Tarefa } from "../../api";
import { type Estado, type LinhaVerificacao, type VereditoConteudo, vereditoDaExecucao, vereditoDaVerificacao } from "./verificacao";

const ABERTOS = ["na_fila", "enviada", "executando", "pausada"];

const ICONE = { ok: CheckCircle2, warn: CircleAlert, crit: OctagonAlert } as const;

interface Props {
  execucao: ExecucaoMigracao; // a execução (concluída, ou a última que falhou na conferência)
  verificacao?: ExecucaoMigracao; // a última migracao.verificar desta execução
  podeVerificar: boolean; // execução concluída e nada rodando
  agenteVerifica: boolean; // o agente escolhido liberou migracao.verificar
  opera: boolean;
  busy: boolean;
  onVerificar: () => void;
  onAcompanhar: (t: Tarefa) => void;
}

export function Verificacao({ execucao, verificacao, podeVerificar, agenteVerifica, opera, busy, onVerificar, onAcompanhar }: Props) {
  const resumo = execucao.tarefa.resumo as ResumoExecucao | undefined;
  const sob = verificacao?.tarefa.resumo as ResumoVerificacao | undefined;
  const rodando = !!verificacao && ABERTOS.includes(verificacao.tarefa.estado);
  const motivoBotao = !podeVerificar
    ? "Só para execução concluída, com nada rodando no projeto"
    : !agenteVerifica
      ? "O agente escolhido não liberou migracao.verificar em canal.tarefas_permitidas"
      : "Relê origem e destino e compara linha a linha, coluna a coluna (só leitura)";

  return (
    <section className="mm-verif" aria-labelledby="mm-verif-titulo">
      <div className="mm-verif__cabeca">
        <div>
          <h3 id="mm-verif-titulo">
            <ShieldCheck size={18} aria-hidden={true} /> Verificação
          </h3>
          <p className="mm-dica">
            Duas conferências <strong>independentes</strong>, porque cada uma vê o que a outra não vê. Nenhum valor sai do servidor
            do agente: as divergências apontam a chave da linha e o nome da coluna.
          </p>
          <ul className="mm-dica mm-verif__duas">
            <li>
              <strong>Gravado × destino</strong> — contagem, soma das chaves e soma do conteúdo de cada coluna numa forma canônica: um
              centavo, um acento ou uma hora a mais <em>depois da leitura</em> já não batem. Não vê erro na leitura da origem (o
              driver entregando a data errada), porque os dois lados herdam o mesmo erro.
            </li>
            <li>
              <strong>Pelos próprios bancos</strong> (em “Verificar de novo”) — a origem e o destino imprimem cada valor como texto
              no próprio servidor e os textos são comparados: pega erro de leitura, de tipo e de fuso. Só vale para colunas
              copiadas sem transformação; textos longos e binários entram em parte (tamanho, começo) e o resto fica com a primeira.
            </li>
          </ul>
        </div>
        {opera && (
          <Button onClick={onVerificar} disabled={busy || rodando || !podeVerificar || !agenteVerifica} title={motivoBotao}>
            <ShieldCheck size={16} aria-hidden={true} /> {rodando ? "Verificando…" : "Verificar de novo"}
          </Button>
        )}
      </div>

      {verificacao && (
        <div className="mm-verif__bloco">
          <p className="mm-verif__quando">
            <Badge state={verificacao.tarefa.estado === "sucesso" ? "ok" : rodando ? "info" : "crit"}>
              {verificacao.tarefa.estado.replace("_", " ")}
            </Badge>
            <span>
              Verificação sob demanda (origem × destino) · {new Date(sob?.verificado_em ?? verificacao.criada_em).toLocaleString("pt-BR")}
              {verificacao.tarefa.iniciada_por ? ` · ${verificacao.tarefa.iniciada_por}` : ""}
            </span>
            <Button variant="ghost" onClick={() => onAcompanhar(verificacao.tarefa)} aria-label="Acompanhar a verificação">
              <Eye size={16} aria-hidden={true} />
            </Button>
          </p>
          {verificacao.tarefa.erro && <p className="mm-historico__erro">{verificacao.tarefa.erro}</p>}
          {sob && <Painel v={vereditoDaVerificacao(sob)} sobDemanda />}
        </div>
      )}

      {resumo && (
        <div className="mm-verif__bloco">
          <p className="mm-verif__quando">
            <span>Conferência da execução (gravado × relido do destino, antes da troca) · {new Date(execucao.criada_em).toLocaleString("pt-BR")}</span>
          </p>
          <Painel v={vereditoDaExecucao(resumo)} />
        </div>
      )}
    </section>
  );
}

function Painel({ v, sobDemanda }: { v: VereditoConteudo; sobDemanda?: boolean }) {
  const Icone = ICONE[v.tom];
  return (
    <>
      <div className={`mm-verif__veredito mm-verif__veredito--${v.tom}`} role="status">
        <Icone size={22} aria-hidden={true} />
        <div>
          <strong>{v.titulo}</strong>
          <span>{v.detalhe}</span>
          {v.texto && <span className={`mm-verif__texto is-${v.texto.tom}`}>{v.texto.frase}</span>}
        </div>
      </div>
      <table className="mm-colunas mm-verif__tabela">
        <thead>
          <tr>
            <th>Tabela</th>
            <th>Linhas</th>
            <th>Contagem</th>
            <th>Chaves</th>
            <th>Conteúdo</th>
            {sobDemanda && <th>Pelos bancos</th>}
          </tr>
        </thead>
        <tbody>
          {v.linhas.map((l) => (
            <Linha key={l.destino} l={l} sobDemanda={!!sobDemanda} />
          ))}
        </tbody>
      </table>
    </>
  );
}

function Selo({ estado, ok, nao, nulo }: { estado: Estado; ok: string; nao: string; nulo: string }) {
  if (estado === true)
    return (
      <span className="mm-verif__selo is-ok">
        <CheckCircle2 size={14} aria-hidden={true} /> {ok}
      </span>
    );
  if (estado === false)
    return (
      <span className="mm-verif__selo is-erro">
        <OctagonAlert size={14} aria-hidden={true} /> {nao}
      </span>
    );
  return (
    <span className="mm-verif__selo is-nulo">
      <CircleHelp size={14} aria-hidden={true} /> {nulo}
    </span>
  );
}

function Linha({ l, sobDemanda }: { l: LinhaVerificacao; sobDemanda: boolean }) {
  const [aberta, setAberta] = useState(false);
  const detalhes = l.naoConferidas.length > 0 || l.divergencias.length > 0 || !!l.motivo;
  return (
    <tr>
      <td>
        <code>{l.origem}</code> → <code>{l.destino}</code>
        {l.onde === "staging" && <span className="mm-dica"> (staging)</span>}
      </td>
      <td>{l.linhas.toLocaleString("pt-BR")}</td>
      <td>
        <Selo
          estado={l.contagem}
          ok="confere"
          nao={l.ausentes ? `${l.ausentes.toLocaleString("pt-BR")} ausentes` : "não confere"}
          nulo="—"
        />
      </td>
      <td>
        <Selo estado={l.chaves} ok="conferem" nao="faltam chaves" nulo={l.semChave ? "sem chave" : "não conferidas"} />
      </td>
      <td>
        <Selo
          estado={l.conteudo}
          ok={`idêntico (${l.colunas} ${l.colunas === 1 ? "coluna" : "colunas"})`}
          nao={l.divergentes ? `${l.divergentes.toLocaleString("pt-BR")} linhas divergentes` : "divergente"}
          nulo="não conferido"
        />
        {l.naoConferidas.length > 0 && (
          <span className="mm-chip mm-chip--aviso">
            {l.naoConferidas.length} {l.naoConferidas.length === 1 ? "coluna de fora" : "colunas de fora"}
          </span>
        )}
        {l.porColuna &&
          Object.entries(l.porColuna).map(([c, n]) => (
            <span key={c} className="mm-chip mm-chip--erro">
              {c}: {n.toLocaleString("pt-BR")}
            </span>
          ))}
        {detalhes && (
          <button type="button" className="mm-link" onClick={() => setAberta(!aberta)} aria-expanded={aberta}>
            {aberta ? "esconder detalhes" : "ver detalhes"}
          </button>
        )}
        {aberta && (
          <ul className="mm-amostras">
            {l.motivo && <li>{l.motivo}</li>}
            {l.naoConferidas.map((c) => (
              <li key={c.coluna}>
                <code>{c.coluna}</code> não conferida: {c.motivo}
              </li>
            ))}
            {l.divergencias.map((d, i) => (
              <li key={i}>
                {d.chave ? (
                  <code>chave {d.chave}</code>
                ) : l.semChave ? (
                  <span>sem chave, pela soma do conteúdo:</span>
                ) : (
                  <span>linha (a chave só aparece para quem pode executar migração)</span>
                )}{" "}
                {d.ausente ? "existe na origem e não está no destino" : `diverge em ${d.colunas?.join(", ") || "—"}`}
              </li>
            ))}
            {l.divergencias.length > 0 && !sobDemanda && <li className="mm-dica">Primeiras linhas divergentes encontradas (até 20).</li>}
          </ul>
        )}
      </td>
      {sobDemanda && (
        <td>
          <PelosBancos l={l} />
        </td>
      )}
    </tr>
  );
}

// A segunda conferência de uma tabela: o texto que a origem e o destino imprimem.
function PelosBancos({ l }: { l: LinhaVerificacao }) {
  const [aberta, setAberta] = useState(false);
  const t = l.texto;
  if (!t) return <span className="mm-dica">—</span>;
  const detalhes = t.fora.length > 0 || t.parciais.length > 0 || t.divergencias.length > 0 || !!t.motivo;
  return (
    <>
      <Selo
        estado={t.estado}
        ok={`mesmo texto (${t.colunas} ${t.colunas === 1 ? "coluna" : "colunas"})`}
        nao={`${t.divergentes.toLocaleString("pt-BR")} ${t.divergentes === 1 ? "linha diverge" : "linhas divergem"}`}
        nulo="não conferido"
      />
      {t.parciais.length > 0 && <span className="mm-chip">{t.parciais.length} em parte</span>}
      {t.fora.length > 0 && (
        <span className="mm-chip mm-chip--aviso">
          {t.fora.length} {t.fora.length === 1 ? "coluna de fora" : "colunas de fora"}
        </span>
      )}
      {t.porColuna &&
        Object.entries(t.porColuna).map(([c, n]) => (
          <span key={c} className="mm-chip mm-chip--erro">
            {c}: {n.toLocaleString("pt-BR")}
          </span>
        ))}
      {detalhes && (
        <button type="button" className="mm-link" onClick={() => setAberta(!aberta)} aria-expanded={aberta}>
          {aberta ? "esconder detalhes" : "ver detalhes"}
        </button>
      )}
      {aberta && (
        <ul className="mm-amostras">
          {t.motivo && <li>{t.motivo}</li>}
          {t.divergencias.map((d, i) => (
            <li key={i}>
              {d.chave ? <code>chave {d.chave}</code> : <span>linha (a chave só aparece para quem pode executar migração)</span>}{" "}
              {d.ausente ? "não está no destino" : `os bancos imprimem diferente em ${d.colunas?.join(", ") || "—"}`}
            </li>
          ))}
          {t.parciais.map((c) => (
            <li key={c.coluna}>
              <code>{c.coluna}</code> conferida em parte: {c.motivo}
            </li>
          ))}
          {t.fora.map((c) => (
            <li key={c.coluna}>
              <code>{c.coluna}</code> fora: {c.motivo}
            </li>
          ))}
        </ul>
      )}
    </>
  );
}
