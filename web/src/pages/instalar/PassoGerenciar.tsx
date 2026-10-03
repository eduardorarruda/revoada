// Gestão do que já foi criado: chaves de ingestão, lotes do instalador universal e
// os limites de recurso do agente. É a parte de manutenção do fluxo — fica atrás de
// uma escolha explícita para não competir com o caminho principal, que é instalar.
import { useMemo, useState } from "react";
import { Badge, Button, ConfirmDialog } from "../../components";
import type { AgentKey, AgentResourceLimits, EnrollToken } from "../../api";
import { fmtRelAbs } from "../../format";
import { useHostNames } from "../../hooks/useHostNames";
import { useHostContainers } from "../../hooks/useHostContainers";
import { CAIXA, MATCH_META, keyMatch, type AlvoInstalador } from "./comum";
import type { useCredenciais } from "./useCredenciais";

type Credenciais = ReturnType<typeof useCredenciais>;

export function PassoGerenciar({
  cred,
  onBaixar,
  onCopiar,
}: {
  cred: Credenciais;
  onBaixar: (alvo: AlvoInstalador) => void;
  onCopiar: (texto: string, oQue: string) => void;
}) {
  const { hostLabel, names } = useHostNames();
  const { hosts: hostsConhecidos } = useHostContainers();
  const [apagarChave, setApagarChave] = useState<AgentKey | null>(null);
  const [apagarToken, setApagarToken] = useState<EnrollToken | null>(null);
  const [verLimites, setVerLimites] = useState(false);
  // Chaves reveladas nesta sessão da tela (id → texto). Fica só em memória e some
  // ao fechar o modal: a chave em claro não deve sobreviver à tarefa que a pediu.
  const [reveladas, setReveladas] = useState<Record<string, string>>({});
  const [revelando, setRevelando] = useState<string | null>(null);

  const revelar = async (a: AgentKey) => {
    setRevelando(a.id);
    const chave = await cred.revelarChave(a);
    setRevelando(null);
    if (chave) setReveladas((r) => ({ ...r, [a.id]: chave }));
  };
  const ocultar = (id: string) =>
    setReveladas((r) => Object.fromEntries(Object.entries(r).filter(([k]) => k !== id)));

  // Conjunto de identificadores conhecidos (normalizados) contra o qual a chave é
  // conferida: hostnames que reportam + hostnames e nomes de exibição do inventário.
  const idsConhecidos = useMemo(() => {
    const s = new Set<string>();
    for (const h of hostsConhecidos) s.add(h.trim().toLowerCase());
    for (const [tec, exib] of Object.entries(names)) {
      s.add(tec.trim().toLowerCase());
      if (exib) s.add(exib.trim().toLowerCase());
    }
    return s;
  }, [hostsConhecidos, names]);

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-4)" }}>
      {/* Chaves de ingestão */}
      <section>
        <h3 style={{ fontSize: "var(--fs-14)", margin: "0 0 var(--sp-1)" }}>Chaves de ingestão</h3>
        <p style={{ color: "var(--text-2)", fontSize: "var(--fs-12)", margin: "0 0 var(--sp-2)" }}>
          Uma por servidor: é o que autentica o agente ao enviar dados.{" "}
          <strong>Revogar</strong> derruba o envio daquele servidor na hora. A chave aparece{" "}
          <strong>abreviada</strong>, o suficiente para você reconhecer a linha, não para usá-la. Para
          reinstalar, use o botão <strong>Instalador</strong>: ele leva a chave dentro do arquivo, sem
          passar por aqui. Só use <strong>Revelar</strong> quando precisar mesmo colar o texto da chave em
          algum lugar; cada revelação fica registrada na Auditoria.
        </p>
        {cred.carregando ? (
          <p style={{ color: "var(--text-2)" }}>Carregando…</p>
        ) : cred.chaves.length === 0 ? (
          <p style={{ color: "var(--text-2)", fontSize: "var(--fs-13)" }}>
            Nenhuma chave ainda, ela é criada junto com o primeiro instalador.
          </p>
        ) : (
          <div style={{ overflowX: "auto" }}>
            <table className="dtable" style={{ width: "100%" }}>
              <thead>
                <tr>
                  <th>Servidor</th>
                  <th>Correspondência</th>
                  <th>Chave</th>
                  <th>Estado</th>
                  <th>Última atividade</th>
                  <th style={{ textAlign: "right" }}>Ações</th>
                </tr>
              </thead>
              <tbody>
                {cred.chaves.map((a) => {
                  const m = MATCH_META[keyMatch(a.hostname, idsConhecidos)];
                  const revelada = reveladas[a.id];
                  return (
                    <tr key={a.id}>
                      <td title={a.hostname || undefined}>{a.hostname ? hostLabel(a.hostname) : "—"}</td>
                      <td>
                        <span title={m.help}>
                          <Badge state={m.state}>{m.label}</Badge>
                        </span>
                      </td>
                      <td>
                        {revelada ? (
                          // Segredo À MOSTRA: o aviso e o "ocultar" ficam colados no
                          // texto, para que ninguém deixe a chave aberta na tela sem
                          // perceber que ela está ali.
                          <div style={{ display: "flex", flexDirection: "column", gap: 2, minWidth: 0 }}>
                            <code
                              style={{
                                fontFamily: "var(--font-mono)",
                                fontSize: "var(--fs-12)",
                                wordBreak: "break-all",
                                color: "var(--warn)",
                              }}
                            >
                              {revelada}
                            </code>
                            <span style={{ display: "flex", gap: "var(--sp-1)", flexWrap: "wrap" }}>
                              <Button variant="ghost" onClick={() => onCopiar(revelada, "Chave")}>
                                Copiar
                              </Button>
                              <Button variant="ghost" onClick={() => ocultar(a.id)}>
                                Ocultar
                              </Button>
                            </span>
                            <span style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}>
                              Chave à mostra, trate como senha.
                            </span>
                          </div>
                        ) : (
                          <div style={{ display: "flex", alignItems: "center", gap: "var(--sp-1)", flexWrap: "wrap" }}>
                            <span
                              style={{ fontFamily: "var(--font-mono)", fontSize: "var(--fs-12)", color: "var(--text-2)" }}
                              title="Chave abreviada: identifica a linha, mas não autentica nada."
                            >
                              {a.serverkey}
                            </span>
                            <Button
                              variant="ghost"
                              disabled={revelando === a.id}
                              onClick={() => void revelar(a)}
                              title="Mostra o texto completo da chave. A revelação fica registrada na Auditoria."
                            >
                              {revelando === a.id ? "Revelando…" : "Revelar"}
                            </Button>
                          </div>
                        )}
                      </td>
                      <td>
                        <Badge state={a.revoked ? "crit" : "ok"}>{a.revoked ? "Revogada" : "Ativa"}</Badge>
                      </td>
                      <td>
                        {a.last_seen ? (
                          <span title={fmtRelAbs(a.last_seen).abs}>{fmtRelAbs(a.last_seen).rel}</span>
                        ) : (
                          "nunca"
                        )}
                      </td>
                      <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                        <Button
                          variant="ghost"
                          disabled={a.revoked}
                          onClick={() => onBaixar({ tipo: "chave", id: a.id, nome: a.hostname })}
                          title={
                            a.revoked
                              ? "Chave revogada: reative antes de gerar o instalador."
                              : "Baixar o instalador desta chave (reinstalar sem criar chave nova)"
                          }
                        >
                          Instalador
                        </Button>
                        <Button variant="ghost" onClick={() => void cred.revogarChave(a)}>
                          {a.revoked ? "Reativar" : "Revogar"}
                        </Button>
                        <Button variant="ghost" onClick={() => setApagarChave(a)}>
                          Excluir
                        </Button>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {/* Lotes do instalador universal */}
      <section>
        <h3 style={{ fontSize: "var(--fs-14)", margin: "0 0 var(--sp-1)" }}>Instaladores universais</h3>
        <p style={{ color: "var(--text-2)", fontSize: "var(--fs-12)", margin: "0 0 var(--sp-2)" }}>
          Cada lote é um arquivo que instala em quantos servidores você quiser.{" "}
          <strong>Revogar</strong> impede entradas novas e <strong>não</strong> derruba quem já entrou,
          cada servidor ficou com a chave dele.
        </p>
        {cred.tokens.length === 0 ? (
          <p style={{ color: "var(--text-2)", fontSize: "var(--fs-13)" }}>Nenhum instalador universal criado.</p>
        ) : (
          <div style={{ overflowX: "auto" }}>
            <table className="dtable" style={{ width: "100%" }}>
              <thead>
                <tr>
                  <th>Lote</th>
                  <th>Servidores que entraram</th>
                  <th>Estado</th>
                  <th>Último uso</th>
                  <th style={{ textAlign: "right" }}>Ações</th>
                </tr>
              </thead>
              <tbody>
                {cred.tokens.map((t) => (
                  <tr key={t.token}>
                    <td>{t.label || "—"}</td>
                    <td>{t.uses}</td>
                    <td>
                      <Badge state={t.revoked ? "crit" : "ok"}>{t.revoked ? "Revogado" : "Ativo"}</Badge>
                    </td>
                    <td>
                      {t.last_used_at ? (
                        <span title={fmtRelAbs(t.last_used_at).abs}>{fmtRelAbs(t.last_used_at).rel}</span>
                      ) : (
                        "nunca"
                      )}
                    </td>
                    <td style={{ textAlign: "right", whiteSpace: "nowrap" }}>
                      <Button
                        variant="ghost"
                        disabled={t.revoked}
                        onClick={() => onBaixar({ tipo: "token", id: t.token, nome: t.label })}
                        title={
                          t.revoked
                            ? "Instalador revogado: reative antes de baixar."
                            : "Baixar este mesmo instalador (inclusive para outro sistema operacional)"
                        }
                      >
                        Baixar
                      </Button>
                      <Button
                        variant="ghost"
                        onClick={() => void cred.revogarToken(t)}
                        title={
                          t.revoked
                            ? "Voltar a aceitar servidores novos por este arquivo."
                            : "Impede que NOVOS servidores entrem por este arquivo."
                        }
                      >
                        {t.revoked ? "Reativar" : "Revogar"}
                      </Button>
                      <Button variant="ghost" onClick={() => setApagarToken(t)}>
                        Excluir
                      </Button>
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {/* Limites de recurso do agente (a "cerca dura") */}
      <section>
        <button
          type="button"
          className="btn btn--ghost"
          aria-expanded={verLimites}
          onClick={() => setVerLimites((v) => !v)}
          style={{ fontSize: "var(--fs-13)" }}
        >
          {verLimites ? "▾" : "▸"} Limites de recurso do agente
        </button>
        {verLimites && <BlocoLimites cred={cred} />}
      </section>

      <ConfirmDialog
        open={apagarChave !== null}
        onCancel={() => setApagarChave(null)}
        onConfirm={() => {
          if (apagarChave) void cred.excluirChave(apagarChave);
          setApagarChave(null);
        }}
        verb="Excluir"
        target="chave"
        consequences={`O servidor "${apagarChave?.hostname ?? ""}" deixará de enviar dados até receber uma nova chave. Não pode ser desfeito.`}
        danger
      />

      <ConfirmDialog
        open={apagarToken !== null}
        onCancel={() => setApagarToken(null)}
        onConfirm={() => {
          if (apagarToken) void cred.excluirToken(apagarToken);
          setApagarToken(null);
        }}
        verb="Excluir"
        target="instalador universal"
        consequences={`O arquivo do lote "${apagarToken?.label ?? ""}" deixa de instalar em máquinas novas. Os ${apagarToken?.uses ?? 0} servidor(es) que já entraram por ele continuam reportando. Não pode ser desfeito.`}
        danger
      />
    </div>
  );
}

const CAMPOS_LIMITE: [keyof AgentResourceLimits, string, string][] = [
  ["memory_max_mb", "Memória máx. (MB)", "MemoryMax, OOM se estourar"],
  ["memory_high_mb", "Memória alta (MB)", "MemoryHigh, pressão antes do teto"],
  ["cpu_quota_pct", "CPU (% de 1 núcleo)", "CPUQuota"],
  ["nice", "Nice", "prioridade de CPU (maior = mais educado)"],
  ["tasks_max", "Tasks máx.", "TasksMax, teto de threads"],
  ["mem_soft_mb", "GOMEMLIMIT (MB)", "soft, GC agressivo perto do teto"],
  ["max_procs", "GOMAXPROCS", "núcleos em paralelo (0 = auto)"],
];

function BlocoLimites({ cred }: { cred: Credenciais }) {
  const [salvando, setSalvando] = useState(false);
  const l = cred.limites;
  return (
    <div style={{ ...CAIXA, marginTop: "var(--sp-2)" }}>
      <p style={{ color: "var(--text-2)", fontSize: "var(--fs-12)", marginTop: 0 }}>
        Tetos aplicados ao agente no servidor: a <strong>cerca dura</strong> do serviço
        (memória/CPU/threads) e os <strong>limites suaves</strong> que o próprio agente aplica. Valem
        para <strong>novas instalações</strong> e já entram nos instaladores gerados daqui em diante. Os
        padrões são generosos frente ao consumo real (pico ~14&nbsp;MB / &lt;1% de CPU), mexa só se
        precisar.
      </p>
      {l === null ? (
        <p style={{ color: "var(--text-2)", fontSize: "var(--fs-12)" }}>Carregando limites…</p>
      ) : (
        <>
          <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fit, minmax(150px, 1fr))", gap: "var(--sp-2)" }}>
            {CAMPOS_LIMITE.map(([campo, rotulo, dica]) => (
              <label key={campo} style={{ display: "flex", flexDirection: "column", gap: "var(--sp-1)" }}>
                <span style={{ fontSize: "var(--fs-12)", color: "var(--text-2)" }} title={dica}>
                  {rotulo}
                </span>
                <input
                  className="field"
                  type="number"
                  min={0}
                  value={l[campo]}
                  onChange={(e) => cred.setLimites({ ...l, [campo]: Number(e.target.value) })}
                />
              </label>
            ))}
          </div>
          <div style={{ marginTop: "var(--sp-2)" }}>
            <Button
              variant="primary"
              disabled={salvando}
              onClick={async () => {
                setSalvando(true);
                await cred.salvarLimites(l);
                setSalvando(false);
              }}
            >
              {salvando ? "Salvando…" : "Salvar limites"}
            </Button>
          </div>
        </>
      )}
    </div>
  );
}
