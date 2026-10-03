// Atualização dos agentes (admin): o freio da auto-atualização da frota.
//
// POR QUE ESTA TELA EXISTE. O deploy do painel é automático, e o agente pergunta ao
// painel, de hora em hora, se há versão nova. Somados, isso quer dizer que um
// `git push` trocava o binário de TODOS os servidores monitorados em ~1h — sem
// canário, sem janela e sem botão de parada. O backend ganhou a política que
// faltava; esta tela é onde ela se opera.
//
// Três controles, do mais amplo ao mais específico:
//   1. desligar a auto-atualização da frota inteira;
//   2. fixar (pin) uma versão — a frota para de subir e fica naquela;
//   3. segurar UM servidor (hold), que vence os dois de cima.
//
// Motivo é obrigatório ao desligar ou fixar. Não é burocracia: quem encontra a
// frota parada às 3h da manhã não tem como adivinhar se aquilo é um incidente em
// andamento ou um freio que alguém esqueceu de soltar em março.
//
// # A lista é de SERVIDORES, não de chaves
//
// Até aqui a tabela era a de `/api/agents`, cujo campo `hostname` é o APELIDO que
// alguém digitou ao criar a chave. Medido em produção: as 8 chaves se chamavam
// "Loja Exemplo", "ola", "Teste traces", "Nuvem WHM", "Cliente Interno"; os
// servidores reais são srv-01, vps-demo.exemplo.com,
// whm-demo.exemplo.com, srv-02 e mail.exemplo.com.br. Interseção: nenhuma. Quem
// abria a tela para segurar um servidor não achava servidor nenhum.
//
// A fonte passa a ser a MESMA da tela de Infraestrutura — o inventário de
// `/api/hosts`, alimentado pelo que o agente reporta — e a amarração servidor→chave
// mora em agentServerLink.ts, com a regra de nunca agir sobre um vínculo incerto.
import { useCallback, useEffect, useMemo, useState } from "react";
import {
  Badge,
  Button,
  Card,
  Collapsible,
  DataTable,
  FormField,
  HelpPanel,
  Modal,
  PageHeader,
  useToast,
  type Column,
  type State,
} from "../components";
import {
  getAgentUpdatePolicy,
  isAdmin,
  listAgents,
  listHosts,
  setAgentUpdateHold,
  setAgentUpdatePolicy,
  type AgentKey,
  type AgentUpdatePolicy,
  type HostDetail,
} from "../api";
import { formatDateTime, fmtRelAbs, mensagemDeErro } from "../format";
import { help } from "../help";
import { VINCULO_META, vincularChaves, vinculoDe, type Vinculo, type Vinculos } from "./agentServerLink";

// ESTADO_RELATO traduz o vocabulário do agente (agent/internal/selfupdate) para o
// que o operador precisa decidir. Cada entrada diz o que aconteceu E o que fazer —
// um estado de atualização que não sugere ação é só mais uma palavra na tela.
//
// "sem_promotor" é o caso que mais engana: o host roda em modo cron/launchd (ou
// systemd < 231) e NÃO tem quem promova um binário novo. Ele nunca se atualiza
// sozinho, por mais que se espere. Sem este rótulo, ele aparece apenas como uma
// versão atrasada na lista — e o operador fica aguardando uma atualização que, por
// construção, não vai acontecer.
export const ESTADO_RELATO: Record<string, { label: string; state: State; ajuda: string }> = {
  em_dia: {
    label: "em dia",
    state: "ok",
    ajuda: "O agente perguntou ao painel e não havia nada a fazer: ele já está na versão que deve rodar.",
  },
  sem_promotor: {
    label: "precisa do instalador",
    state: "warn",
    ajuda:
      "Há versão nova, mas ESTE servidor não se atualiza sozinho: ele roda em modo cron/launchd (ou systemd antigo), sem o componente que troca o binário. Nada foi baixado, de propósito. Para atualizá-lo, gere o instalador em Infraestrutura → Adicionar servidor → Chaves de acesso e rode-o na máquina; ele reinstala o agente já na versão nova.",
  },
  estagiado: {
    label: "baixado, aguardando troca",
    state: "info",
    ajuda: "O binário novo já está no servidor e a troca acontece na próxima reinicialização do serviço.",
  },
  estagiado_reiniciando: {
    label: "trocando agora",
    state: "info",
    ajuda: "O binário novo foi baixado e o serviço está reiniciando para assumir a versão nova.",
  },
  promocao_nao_ocorreu: {
    label: "troca não aconteceu",
    state: "crit",
    ajuda:
      "O binário novo foi baixado, mas o serviço continuou na versão antiga, o mecanismo de troca não está funcionando neste servidor. O agente parou de insistir para não transformar atualização em queda. Rode o instalador de novo na máquina para reinstalar o serviço.",
  },
  recusado_downgrade: {
    label: "recusou voltar versão",
    state: "warn",
    ajuda:
      "O painel anunciou uma versão MAIS VELHA que a em execução e o agente recusou: ele nunca faz downgrade sozinho. Costuma indicar artefato antigo republicado no painel. Para voltar de versão de propósito, use a versão fixada aqui nesta tela.",
  },
  erro_consulta: {
    label: "não falou com o painel",
    state: "warn",
    ajuda:
      "O agente não conseguiu perguntar ao painel se há versão nova (painel fora do ar, DNS, proxy). Não afeta a coleta: métricas e logs continuam chegando.",
  },
  erro_download: {
    label: "falhou ao baixar",
    state: "crit",
    ajuda: "O agente encontrou uma versão nova mas não conseguiu baixá-la. Confira rede e espaço em disco do servidor.",
  },
  erro: {
    label: "erro",
    state: "crit",
    ajuda: "A última tentativa de atualização terminou em erro. O detalhe está na coluna ao lado.",
  },
};

export function metaDoEstado(estado: string): { label: string; state: State; ajuda: string } {
  return (
    ESTADO_RELATO[estado] ?? {
      label: estado,
      state: "neutral",
      // Estado novo no agente e ainda sem tradução: melhor mostrar o nome cru do que
      // engolir a linha e fingir que o host não relatou nada.
      ajuda: "Estado relatado pelo agente que esta versão do painel ainda não sabe explicar.",
    }
  );
}

// Os dois estados que NÃO vêm do agente — eles descrevem a ausência de relato, e
// precisam ser tão explícitos quanto os outros. Inventar "em dia" para quem nunca
// falou seria a pior mentira possível nesta tela.
//
// A diferença entre os dois é medível e importa: sem NENHUM registro de consulta, o
// agente nunca perguntou (é a frota 0.7.0 de hoje) e não vai trocar de versão
// sozinho; com registro mas sem estado, ele já perguntou e ainda não teve tentativa
// a relatar (a primeira consulta depois de instalar).
export const SEM_CONSULTA = {
  label: "não pergunta ao painel",
  state: "neutral" as State,
  ajuda:
    "Este servidor nunca perguntou ao painel se existe versão nova, não há nenhum registro de consulta da chave dele. É o esperado em agentes anteriores à auto-atualização (a frota 0.7.0 é assim) e em agentes instalados sem o endereço do painel. Consequência prática: ele NÃO troca de versão sozinho, por mais que se espere, e a política da frota não o alcança. Para atualizá-lo (e ligar o relato), reinstale o agente pelo painel: Infraestrutura → Adicionar servidor. A coleta de métricas e logs não é afetada por nada disso.",
};

export const SEM_TENTATIVA = {
  label: "perguntou, sem tentativa ainda",
  state: "info" as State,
  ajuda:
    "O agente já consultou o painel, mas ainda não houve uma tentativa de atualização para relatar. É o normal logo depois de instalar.",
};

// Regra do formulário global, num lugar só: desligar ou fixar exige motivo. Voltar
// tudo ao normal (ligado, sem pin) não exige — não há nada para explicar depois.
export function motivoObrigatorio(off: boolean, pin: string): boolean {
  return off || pin.trim() !== "";
}

// Linha da tabela: um SERVIDOR do inventário mais a chave que o painel conseguiu (ou
// não) amarrar a ele.
interface LinhaServidor {
  host: HostDetail;
  vinculo: Vinculo;
}

const MUTED_12 = { color: "var(--text-3)", fontSize: "var(--fs-12)" } as const;
const CELULA = { display: "flex", flexDirection: "column", gap: 2, minWidth: 0 } as const;

function rotuloDe(h: HostDetail): string {
  return h.display_name?.trim() || h.hostname;
}

export function AgentUpdates() {
  const admin = isAdmin();
  const toast = useToast();

  const [politica, setPolitica] = useState<AgentUpdatePolicy | null>(null);
  const [agentes, setAgentes] = useState<AgentKey[]>([]);
  const [hosts, setHosts] = useState<HostDetail[]>([]);
  const [carregando, setCarregando] = useState(true);
  const [erro, setErro] = useState<string>();
  const [helpOpen, setHelpOpen] = useState(false);

  // Rascunho do formulário global (separado do que está salvo, para o usuário poder
  // desistir e para o botão saber se há algo a salvar).
  const [off, setOff] = useState(false);
  const [pin, setPin] = useState("");
  const [motivo, setMotivo] = useState("");
  const [salvando, setSalvando] = useState(false);

  // Chave em vias de ser segurada, com o nome do que o operador vê na tela: o motivo
  // é pedido antes, num modal, e o texto precisa nomear o SERVIDOR, não a chave.
  const [segurando, setSegurando] = useState<{ chave: AgentKey; rotulo: string } | null>(null);
  const [motivoHold, setMotivoHold] = useState("");
  const [aplicandoHold, setAplicandoHold] = useState<string | null>(null);

  const carregar = useCallback(() => {
    if (!admin) return;
    setCarregando(true);
    // As três leituras juntas: a política da frota, as chaves (onde mora o freio) e o
    // inventário de servidores (o que o operador quer ver). A tela só faz sentido com
    // as três — mostrar servidores sem as chaves seria mostrar uma lista sem ação.
    Promise.all([getAgentUpdatePolicy(), listAgents(), listHosts()])
      .then(([p, a, h]) => {
        setPolitica(p);
        setOff(p.off);
        setPin(p.pin ?? "");
        setMotivo(p.motivo ?? "");
        setAgentes(a.agents ?? []);
        setHosts(h.hosts ?? []);
        setErro(undefined);
      })
      .catch((e) => setErro(mensagemDeErro(e, "Não foi possível carregar a política de atualização.")))
      .finally(() => setCarregando(false));
  }, [admin]);

  useEffect(() => {
    carregar();
  }, [carregar]);

  const alterado =
    politica !== null && (off !== politica.off || pin.trim() !== (politica.pin ?? "") || motivo !== (politica.motivo ?? ""));
  const faltaMotivo = motivoObrigatorio(off, pin) && motivo.trim() === "";

  const salvar = async () => {
    setSalvando(true);
    try {
      await setAgentUpdatePolicy({ off, pin: pin.trim(), motivo: motivo.trim() });
      toast.success("Política de atualização salva. Ela vale na próxima vez que cada agente perguntar (até 1 hora).");
      carregar();
    } catch (e) {
      // O backend valida o formato do pin e devolve a frase pronta ("use o formato
      // 0.9.1"). Repassamos como está: quem escreveu a regra escreveu a explicação.
      toast.error(mensagemDeErro(e, "Não foi possível salvar a política."));
    } finally {
      setSalvando(false);
    }
  };

  const aplicarHold = async (chave: AgentKey, hold: boolean, porque: string) => {
    setAplicandoHold(chave.id);
    try {
      await setAgentUpdateHold(chave.id, hold, porque);
      toast.success(
        hold
          ? "Servidor segurado: ele não troca de versão até você soltar."
          : "Servidor solto: ele volta a seguir a política da frota.",
      );
      carregar();
    } catch (e) {
      toast.error(mensagemDeErro(e, "Não foi possível mudar o freio deste servidor."));
    } finally {
      setAplicandoHold(null);
    }
  };

  // A amarração servidor→chave é recalculada a cada carga; é O(hosts+chaves) sobre
  // dezenas de linhas, não vale cache entre telas.
  const vinculos: Vinculos = useMemo(() => vincularChaves(hosts, agentes), [hosts, agentes]);
  const linhas: LinhaServidor[] = useMemo(
    () => hosts.map((h) => ({ host: h, vinculo: vinculoDe(vinculos, h.hostname) })),
    [hosts, vinculos],
  );

  // botaoFreio é o MESMO botão nas duas tabelas (servidores e chaves soltas): o freio
  // é um só, gravado na chave. Duplicá-lo em dois componentes seria duas chances de
  // uma delas passar a mentir sobre o que o clique faz.
  const botaoFreio = (chave: AgentKey | undefined, rotulo: string, impedimento?: string) => {
    if (!chave) {
      return (
        <span style={{ ...MUTED_12, cursor: "help" }} title={impedimento}>
          sem chave para segurar
        </span>
      );
    }
    return (
      <Button
        variant="ghost"
        disabled={aplicandoHold === chave.id}
        onClick={() => {
          if (chave.update_hold) {
            void aplicarHold(chave, false, "");
          } else {
            setMotivoHold("");
            setSegurando({ chave, rotulo });
          }
        }}
        title={
          chave.update_hold
            ? "Volta a aplicar a política da frota neste servidor."
            : "Impede que ESTE servidor troque de versão, mesmo que a frota atualize. Use para o canário ao contrário: segurar o servidor crítico enquanto o resto sobe."
        }
      >
        {chave.update_hold ? "Soltar" : "Segurar"}
      </Button>
    );
  };

  const colunas = useMemo<Column<LinhaServidor>[]>(
    () => [
      {
        key: "servidor",
        label: "Servidor",
        sortable: true,
        sortValue: (l) => rotuloDe(l.host).toLowerCase(),
        render: (l) => {
          const rotulo = rotuloDe(l.host);
          return (
            <div style={CELULA}>
              <a
                href={`#/hosts/${encodeURIComponent(l.host.hostname)}`}
                title={`Abrir a visão de ${rotulo}`}
                style={{ color: "var(--accent, var(--text-1))", textDecoration: "none", fontWeight: 600 }}
              >
                {rotulo}
              </a>
              {rotulo !== l.host.hostname && (
                <span style={{ ...MUTED_12, wordBreak: "break-all" }}>{l.host.hostname}</span>
              )}
            </div>
          );
        },
      },
      {
        key: "versao",
        label: "Versão do agente",
        sortable: true,
        // A versão vem do INVENTÁRIO (o que o agente está rodando de fato, a mesma
        // coluna da tela de Infraestrutura), não do relato de atualização: a frota
        // que não relata nada continua tendo versão, e escondê-la deixaria a tela
        // vazia justamente onde ela precisa informar.
        sortValue: (l) => l.host.agent_version || "",
        render: (l) => {
          const r = l.vinculo.chave?.update_report;
          const semPromotor = r?.estado === "sem_promotor";
          return (
            <div style={CELULA}>
              <span className="tabular">{l.host.agent_version || "—"}</span>
              {semPromotor && (
                <span title={ESTADO_RELATO.sem_promotor.ajuda}>
                  <Badge state="warn">só troca reinstalando</Badge>
                </span>
              )}
              {r?.versao_desejada && r.versao_desejada !== l.host.agent_version && (
                <span style={MUTED_12}>disponível: {r.versao_desejada}</span>
              )}
            </div>
          );
        },
      },
      {
        key: "relato",
        label: "Última atualização",
        // Sem hideOnMobile de propósito: este é o conteúdo da tela. Esconder no
        // celular justamente o relato de quem falhou a atualização devolveria o
        // problema que a tela veio resolver, só que para quem está de plantão.
        render: (l) => <CelulaRelato linha={l} />,
      },
      {
        key: "freio",
        label: "Freio",
        sortable: true,
        sortValue: (l) => (l.vinculo.chave?.update_hold ? 1 : 0),
        render: (l) => <CelulaFreio vinculo={l.vinculo} />,
      },
      {
        key: "acao",
        label: "Ação",
        align: "right",
        render: (l) =>
          botaoFreio(l.vinculo.chave, rotuloDe(l.host), VINCULO_META[l.vinculo.tipo].ajuda),
      },
    ],
    // As colunas só chamam aplicarHold de dentro do onClick, que lê a versão corrente
    // por closure. O que muda a APARÊNCIA das células é qual linha está em andamento.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [aplicandoHold],
  );

  const colunasSoltas = useMemo<Column<AgentKey>[]>(
    () => [
      {
        key: "chave",
        label: "Chave de acesso",
        sortable: true,
        sortValue: (c) => c.hostname.toLowerCase(),
        render: (c) => (
          <div style={CELULA}>
            <span style={{ fontWeight: 600 }}>{c.hostname || "sem identificação"}</span>
            <span style={{ ...MUTED_12, fontFamily: "var(--font-mono)" }}>{c.serverkey}</span>
            {c.revoked && <span style={MUTED_12}>chave revogada, não recebe binário nenhum</span>}
          </div>
        ),
      },
      {
        key: "relato",
        label: "Última atualização",
        render: (c) => <CelulaRelatoChave chave={c} />,
      },
      {
        key: "freio",
        label: "Freio",
        sortable: true,
        sortValue: (c) => (c.update_hold ? 1 : 0),
        render: (c) => <SeloFreio chave={c} />,
      },
      {
        key: "acao",
        label: "Ação",
        align: "right",
        render: (c) => botaoFreio(c, c.hostname || "chave sem identificação"),
      },
    ],
    // Mesmo motivo das colunas de servidores acima.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [aplicandoHold],
  );

  // O menu já esconde o item; esta guarda cobre quem chega pela URL direta, com uma
  // explicação em vez de um erro seco de permissão (mesmo padrão da Auditoria).
  if (!admin) {
    return (
      <div className="page stack">
        <PageHeader title="Atualização dos agentes" subtitle="Controle de versão do agente na frota." />
        <Card>
          <p style={{ color: "var(--text-2)", margin: 0 }}>
            Esta tela é exclusiva de administradores, ela decide quando o programa que roda dentro dos seus
            servidores troca de versão. Se você precisa segurar ou liberar uma atualização, peça a um
            administrador.
          </p>
        </Card>
      </div>
    );
  }

  const segurados = agentes.filter((a) => a.update_hold).length;
  const semVinculo = linhas.filter((l) => !l.vinculo.chave).length;

  return (
    <div className="page stack">
      <PageHeader
        title="Atualização dos agentes"
        subtitle="Decide quando o agente instalado nos seus servidores troca de versão, para a frota inteira ou servidor por servidor."
        onHelp={() => setHelpOpen(true)}
      />

      <Card title="Política da frota">
        <p style={{ color: "var(--text-2)", marginTop: 0 }}>
          Por padrão, cada agente pergunta ao painel a cada hora se existe versão nova e se atualiza sozinho.
          Isso é bom no dia a dia e perigoso numa noite ruim: uma versão com defeito chega a{" "}
          <strong>todos os servidores</strong> em cerca de uma hora. Aqui você desliga essa troca automática ou
          prende a frota numa versão conhecida enquanto investiga.
        </p>

        {erro && (
          <p role="alert" style={{ color: "var(--crit)", marginTop: 0 }}>
            {erro}
          </p>
        )}

        {carregando ? (
          <p style={{ color: "var(--text-2)", margin: 0 }}>Carregando…</p>
        ) : (
          <div className="stack" style={{ gap: "var(--sp-3)" }}>
            <EstadoAtual politica={politica} segurados={segurados} />

            <label style={{ display: "flex", alignItems: "flex-start", gap: "var(--sp-2)", fontSize: "var(--fs-14)" }}>
              <input type="checkbox" checked={off} onChange={(e) => setOff(e.target.checked)} />
              <span>
                Desligar a auto-atualização de <strong>toda a frota</strong>
                <div style={MUTED_12}>
                  Os agentes continuam coletando e enviando normalmente; só param de trocar de versão sozinhos.
                  Para atualizar depois, religue aqui ou rode o instalador na máquina.
                </div>
              </span>
            </label>

            <FormField
              label="Fixar uma versão (opcional)"
              hint="Formato 0.9.1. Com uma versão fixada, a frota fica exatamente nela: quem estiver atrás sobe até ela e ninguém passa disso. Deixe vazio para seguir sempre a mais nova publicada."
            >
              <input
                className="field tabular"
                value={pin}
                placeholder="ex.: 0.9.1"
                onChange={(e) => setPin(e.target.value)}
                style={{ maxWidth: 200 }}
              />
            </FormField>

            <FormField
              label="Motivo"
              required={motivoObrigatorio(off, pin)}
              hint="Escreva para o colega que vai encontrar a frota parada de madrugada sem saber por quê. Ex.: “0.9.2 derrubou a coleta de disco no Ubuntu 20.04, chamado 4127”."
            >
              <textarea
                className="field"
                rows={2}
                value={motivo}
                onChange={(e) => setMotivo(e.target.value)}
                placeholder="Por que a frota está parada ou presa nesta versão?"
              />
            </FormField>
            {faltaMotivo && (
              <p style={{ color: "var(--warn)", fontSize: "var(--fs-12)", margin: 0 }}>
                Escreva o motivo antes de salvar: desligar ou fixar sem explicação vira um freio esquecido, que
                ninguém depois tem coragem de soltar.
              </p>
            )}

            <div className="row" style={{ gap: "var(--sp-2)", flexWrap: "wrap" }}>
              <Button variant="primary" disabled={!alterado || faltaMotivo || salvando} onClick={() => void salvar()}>
                {salvando ? "Salvando…" : "Salvar política"}
              </Button>
              {alterado && (
                <Button
                  variant="ghost"
                  disabled={salvando}
                  onClick={() => {
                    setOff(politica?.off ?? false);
                    setPin(politica?.pin ?? "");
                    setMotivo(politica?.motivo ?? "");
                  }}
                >
                  Desfazer
                </Button>
              )}
            </div>
            <p style={{ ...MUTED_12, margin: 0 }}>
              A mudança vale na próxima vez que cada agente perguntar ao painel, até 1 hora. Ela não reverte
              quem já se atualizou.
            </p>
          </div>
        )}
      </Card>

      {/* Recolhíveis: os dois blocos abaixo são parágrafos longos de explicação
          seguidos de uma tabela. Fechados, a tela cabe numa olhada; abertos,
          continuam explicando tudo. O contador diz quantos servidores/chaves
          estão lá dentro, para recolher não virar esconder. */}
      <Collapsible
        titulo="Servidor por servidor"
        contador={linhas.length}
        resumo="quem está em qual versão, e o freio por servidor"
        persistKey="agent-updates-servidores"
        defaultOpen
      >
        <p style={{ color: "var(--text-2)", marginTop: 0 }}>
          Um servidor por linha, os mesmos da tela de <strong>Infraestrutura</strong>, com a versão do agente que
          cada um está rodando. <strong>Segurar</strong> um servidor impede só ele de trocar de versão, e vence a
          política da frota, é como você deixa o banco de produção parado enquanto o resto sobe.
        </p>
        <p style={{ color: "var(--text-2)", marginTop: 0 }}>
          O freio é gravado na <strong>chave de acesso</strong> do servidor, não no nome dele: é a chave que o
          agente apresenta quando pergunta se há versão nova, e é o único identificador que o painel reconhece
          nessa hora. Esta lista mostra servidores e o painel resolve servidor → chave sozinho. Quando ele não
          consegue resolver com certeza, o botão fica <strong>desligado</strong> e a coluna “Freio” diz por quê,
          um freio aplicado na chave errada pararia a atualização de outro servidor.
        </p>
        {!carregando && semVinculo > 0 && (
          <p style={{ color: "var(--warn)", fontSize: "var(--fs-13)", marginTop: 0 }}>
            {semVinculo === 1
              ? "1 servidor está sem chave identificada"
              : `${semVinculo} servidores estão sem chave identificada`}
            : o painel casa a chave pelo nome que o agente informa ao consultar atualização, e os agentes 0.7.0
            ainda não informam. Até eles serem reinstalados, o casamento é feito pelo nome que você deu à chave
            ao criá-la.
          </p>
        )}
        <DataTable
          columns={colunas}
          rows={linhas}
          keyFn={(l) => l.host.hostname}
          loading={carregando}
          error={erro}
          fit
          searchable
          searchText={(l) => `${l.host.hostname} ${l.host.display_name ?? ""} ${l.host.agent_version ?? ""}`}
          searchPlaceholder="Buscar servidor…"
          initialSort={{ key: "servidor", dir: "asc" }}
          empty="Nenhum servidor no inventário ainda. Instale o agente em um servidor (Infraestrutura → Adicionar servidor) e ele aparece aqui."
        />
      </Collapsible>

      {!carregando && vinculos.soltas.length > 0 && (
        <Collapsible
          titulo="Chaves sem servidor identificado"
          contador={vinculos.soltas.length}
          resumo="o painel não conseguiu amarrá-las a um servidor"
          persistKey="agent-updates-chaves-soltas"
        >
          <p style={{ color: "var(--text-2)", marginTop: 0 }}>
            Chaves de acesso que o painel não conseguiu atribuir com segurança a nenhum servidor da lista acima,
            porque foram batizadas com um nome que não é o de nenhum servidor (“Teste traces”), porque o servidor
            delas ainda não reportou nada, ou porque duas chaves disputam a mesma máquina. Elas ficam aqui, e não
            escondidas: enquanto o agente não informar o próprio nome, esta é a única forma de segurar a
            atualização desses hosts. Antes de usar o freio aqui, confirme de qual servidor é a chave.
          </p>
          <DataTable
            columns={colunasSoltas}
            rows={vinculos.soltas}
            keyFn={(c) => c.id}
            fit
            empty="Nenhuma, todas as chaves estão amarradas a um servidor."
          />
        </Collapsible>
      )}

      <Modal
        open={segurando !== null}
        onClose={() => setSegurando(null)}
        title={`Segurar a atualização de ${segurando?.rotulo || "servidor"}`}
        footer={
          <>
            <Button onClick={() => setSegurando(null)}>Cancelar</Button>
            <Button
              variant="primary"
              disabled={motivoHold.trim() === "" || aplicandoHold !== null}
              onClick={() => {
                const alvo = segurando;
                setSegurando(null);
                if (alvo) void aplicarHold(alvo.chave, true, motivoHold.trim());
              }}
            >
              Segurar este servidor
            </Button>
          </>
        }
      >
        <p style={{ color: "var(--text-2)", marginTop: 0, fontSize: "var(--fs-13)" }}>
          Este servidor deixa de trocar de versão até alguém soltá-lo aqui, mesmo que a frota inteira atualize.
          A coleta de métricas e logs não muda em nada.
        </p>
        {segurando && (
          <p style={{ ...MUTED_12, marginTop: 0 }}>
            O freio será gravado na chave de acesso{" "}
            <span style={{ fontFamily: "var(--font-mono)" }}>{segurando.chave.serverkey}</span>
            {segurando.chave.hostname ? ` (“${segurando.chave.hostname}”)` : ""}.
          </p>
        )}
        <FormField
          label="Motivo"
          required
          hint="Aparece na lista, ao lado do servidor. Ex.: “banco de produção, só atualiza na janela de domingo”."
        >
          <textarea
            className="field"
            rows={2}
            autoFocus
            value={motivoHold}
            onChange={(e) => setMotivoHold(e.target.value)}
            placeholder="Por que este servidor fica de fora?"
          />
        </FormField>
      </Modal>

      <HelpPanel
        open={helpOpen}
        onClose={() => setHelpOpen(false)}
        title={help.pages.agentUpdates.title}
        sections={[
          { heading: "O que é esta tela", body: help.pages.agentUpdates.what },
          {
            heading: "Como usar",
            body: (
              <ol className="help-section__how">
                {help.pages.agentUpdates.how.map((h, i) => (
                  <li key={i}>{h}</li>
                ))}
              </ol>
            ),
          },
          ...(help.pages.agentUpdates.faq ?? []).map((f) => ({ heading: f.q, body: f.a })),
        ]}
      />
    </div>
  );
}

// CelulaRelato: o que aconteceu na última tentativa de atualização DESTE servidor.
// Quando não há relato, ela diz por que não há — nunca finge um estado.
function CelulaRelato({ linha }: { linha: LinhaServidor }) {
  const chave = linha.vinculo.chave;
  if (!chave) {
    const m = VINCULO_META[linha.vinculo.tipo];
    return (
      <div style={CELULA}>
        <span title={m.ajuda}>
          <Badge state="neutral">não dá para saber</Badge>
        </span>
        <span style={{ ...MUTED_12, wordBreak: "break-word" }}>{m.label}</span>
      </div>
    );
  }
  return <RelatoDaChave chave={chave} />;
}

// CelulaRelatoChave é o mesmo conteúdo na tabela das chaves soltas, onde não há
// servidor para nomear.
function CelulaRelatoChave({ chave }: { chave: AgentKey }) {
  return <RelatoDaChave chave={chave} />;
}

function RelatoDaChave({ chave }: { chave: AgentKey }) {
  const r = chave.update_report;
  // Sem NENHUM registro de consulta: este agente nunca perguntou ao painel. É o caso
  // da frota 0.7.0 inteira — e o texto tem que dizer isso, não "sem relato".
  const m = !r ? SEM_CONSULTA : !r.estado ? SEM_TENTATIVA : metaDoEstado(r.estado);
  const quando = r?.quando ? fmtRelAbs(r.quando) : null;
  return (
    <div style={CELULA}>
      <span title={m.ajuda}>
        <Badge state={m.state}>{m.label}</Badge>
      </span>
      {/* O erro/motivo cru do agente é o que resolve o chamado — mostrar só o rótulo
          bonito obrigaria a ir ao log do servidor. */}
      {(r?.erro || r?.motivo) && (
        <span style={{ ...MUTED_12, wordBreak: "break-word" }}>{r?.erro || r?.motivo}</span>
      )}
      {quando && (
        <span style={MUTED_12} title={quando.abs}>
          {quando.rel}
        </span>
      )}
    </div>
  );
}

// CelulaFreio: o estado do freio E, quando não há chave, o motivo de não haver freio
// possível. As duas informações moram na mesma coluna porque respondem à mesma
// pergunta: "este servidor vai trocar de versão sozinho?".
function CelulaFreio({ vinculo }: { vinculo: Vinculo }) {
  const m = VINCULO_META[vinculo.tipo];
  if (!vinculo.chave) {
    return (
      <div style={CELULA}>
        <span title={m.ajuda}>
          <Badge state={m.state}>{m.label}</Badge>
        </span>
        {vinculo.candidatas.length > 0 && (
          <span style={{ ...MUTED_12, wordBreak: "break-word" }}>
            candidatas: {vinculo.candidatas.map((c) => c.hostname || c.serverkey).join(", ")}
          </span>
        )}
      </div>
    );
  }
  return (
    <div style={CELULA}>
      <SeloFreio chave={vinculo.chave} />
      {/* De onde saiu a amarração. "pelo nome da chave" é palpite de cadastro, e o
          operador precisa saber disso ANTES de apertar o freio. */}
      <span style={{ ...MUTED_12, cursor: "help" }} title={m.ajuda}>
        {m.label}
      </span>
    </div>
  );
}

function SeloFreio({ chave }: { chave: AgentKey }) {
  if (!chave.update_hold) {
    return <span style={{ color: "var(--text-3)" }}>segue a frota</span>;
  }
  return (
    <div style={CELULA}>
      <span title="Este servidor não troca de versão, mesmo que a frota atualize.">
        <Badge state="warn">segurado</Badge>
      </span>
      <span style={{ ...MUTED_12, wordBreak: "break-word" }}>
        {chave.update_hold_reason || "sem motivo registrado"}
      </span>
    </div>
  );
}


// EstadoAtual: a frase de uma linha que responde "e agora, como está?" antes de
// qualquer campo de formulário. Inclui QUEM mexeu e QUANDO — num freio de frota,
// essa é a primeira pergunta de quem chega depois.
function EstadoAtual({ politica, segurados }: { politica: AgentUpdatePolicy | null; segurados: number }) {
  if (!politica) return null;
  const pin = (politica.pin ?? "").trim();
  const ligada = !politica.off && pin === "";
  const estado: State = politica.off ? "crit" : pin !== "" ? "warn" : "ok";
  const resumo = politica.off
    ? "Auto-atualização DESLIGADA para a frota"
    : pin !== ""
      ? `Frota fixada na versão ${pin}`
      : "Auto-atualização ligada (sempre a versão mais nova)";
  // updated_at vem zerado ("0001-01-01…") quando ninguém nunca gravou a política —
  // mostrar "01/01/0001" seria pior que não mostrar nada.
  const nunca = !politica.updated_at || politica.updated_at.startsWith("0001-");
  return (
    <div
      style={{
        display: "flex",
        flexDirection: "column",
        gap: 4,
        border: "1px solid var(--border)",
        borderRadius: "var(--radius-sm)",
        padding: "var(--sp-3)",
      }}
    >
      <span style={{ display: "flex", gap: "var(--sp-2)", alignItems: "center", flexWrap: "wrap" }}>
        <Badge state={estado}>{resumo}</Badge>
        {segurados > 0 && (
          <span title="Servidores com freio próprio: eles não trocam de versão nem quando a frota atualiza.">
            <Badge state="warn">
              {segurados} servidor{segurados === 1 ? "" : "es"} segurado{segurados === 1 ? "" : "s"}
            </Badge>
          </span>
        )}
      </span>
      {!ligada && politica.motivo && (
        <span style={{ fontSize: "var(--fs-13)", color: "var(--text-2)", wordBreak: "break-word" }}>
          Motivo: {politica.motivo}
        </span>
      )}
      <span style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}>
        {nunca
          ? "Nunca alterada, a frota está no comportamento padrão."
          : `Alterada por ${politica.updated_by || "desconhecido"} em ${formatDateTime(politica.updated_at)}.`}
      </span>
    </div>
  );
}
