// "Adicionar servidor" — o fluxo inteiro num modal só.
//
// Antes isto era duas telas separadas ("Adicionar servidor", que na verdade só
// instalava em Linux com systemd, e "Instalar agente", com chaves e instaladores
// misturados). Quem chegava tinha de adivinhar qual servia para a sua máquina, e
// descobria o limite no erro. Agora o modal começa por uma ESCOLHA — três jeitos de
// instalar mais a gestão do que já existe —, cada um dizendo onde funciona e o que
// exige, antes de pedir qualquer dado.
import { useEffect, useState } from "react";
import { Download, KeyRound, Layers, Terminal } from "lucide-react";
import { Badge, Button, FormField, Modal, useToast } from "../../components";
import { help } from "../../help";
import type { AgentOS } from "../../api";
import { SshProvisionForm } from "../AddServerModal";
import { CAIXA, SeletorSO, comoRodar, errMsg, installCommand, type AlvoInstalador } from "./comum";
import { PassoGerenciar } from "./PassoGerenciar";
import { useCredenciais } from "./useCredenciais";

type Passo = "escolha" | "arquivo" | "ssh" | "frota" | "gerenciar" | "existente";

const TITULO: Record<Passo, string> = {
  escolha: "Adicionar servidor",
  arquivo: "Configurar um servidor específico",
  ssh: "Instalar por SSH, a partir do painel",
  frota: "Baixar o instalador universal",
  gerenciar: "Chaves de acesso",
  existente: "Baixar de novo",
};

export function InstalarServidorModal({
  open,
  onClose,
  onProvisioned,
}: {
  open: boolean;
  onClose: () => void;
  onProvisioned: () => void;
}) {
  const toast = useToast();
  const cred = useCredenciais(open);
  const [passo, setPasso] = useState<Passo>("escolha");
  const [so, setSo] = useState<AgentOS>("linux");
  const [nome, setNome] = useState("");
  const [rotulo, setRotulo] = useState("");
  const [alvo, setAlvo] = useState<AlvoInstalador | null>(null);
  const [ocupado, setOcupado] = useState(false);

  useEffect(() => {
    if (!open) return;
    setPasso("escolha");
    setNome("");
    setRotulo("");
    setAlvo(null);
  }, [open]);

  function copiar(texto: string, oQue: string) {
    navigator.clipboard.writeText(texto).then(
      () => toast.success(`${oQue} copiado.`),
      () => toast.error("Não foi possível copiar."),
    );
  }

  // voltar sempre limpa o resultado do download: manter o painel "arquivo baixado"
  // na tela de outro caminho faria parecer que aquele caminho baixou algo.
  function voltar(destino: Passo) {
    cred.setBaixado(null);
    if (destino !== "existente") setAlvo(null);
    setPasso(destino);
  }

  // Para onde o "Voltar" leva, a partir do passo em que se está. Só "existente"
  // tem um pai diferente da escolha inicial: ele nasce de dentro da gestão.
  const passoAnterior: Passo = passo === "existente" ? "gerenciar" : "escolha";

  return (
    <Modal open={open} onClose={onClose} wide title={TITULO[passo]}>
      {passo !== "escolha" && (
        <button type="button" className="voltar" onClick={() => voltar(passoAnterior)}>
          ‹ Voltar
        </button>
      )}

      {passo === "escolha" && (
        <Escolha
          onEscolher={setPasso}
          chaves={cred.chaves.length}
          lotes={cred.tokens.length}
        />
      )}

      {passo === "arquivo" &&
        (cred.baixado ? (
          <Resultado cred={cred} onOutro={() => cred.setBaixado(null)} onFechar={onClose} />
        ) : (
          <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-3)" }}>
            <p style={{ color: "var(--text-2)", fontSize: "var(--fs-13)", margin: 0 }}>
              O painel gera a chave deste servidor e a coloca dentro do arquivo. Você leva o arquivo
              até a máquina e roda, nada de editar configuração nem colar chave.
            </p>
            <div style={{ ...CAIXA, display: "flex", flexDirection: "column", gap: "var(--sp-3)" }}>
              <FormField label="Nome do servidor" help={help.fields["provision.name"]} required>
                <input
                  className="field"
                  placeholder="ex.: app-prod-01"
                  value={nome}
                  autoFocus
                  onChange={(e) => setNome(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && nome.trim()) void gerar();
                  }}
                />
              </FormField>
              <SeletorSO value={so} onChange={setSo} name="so-arquivo" />
              <div style={{ display: "flex", gap: "var(--sp-2)", flexWrap: "wrap" }}>
                <Button variant="primary" disabled={ocupado || !nome.trim()} onClick={() => void gerar()}>
                  {cred.baixando ? "Gerando…" : "Gerar e baixar"}
                </Button>
                <Button
                  disabled={ocupado || !nome.trim()}
                  onClick={() => void soAChave()}
                  title="Para quem prefere colar um comando no terminal em vez de levar um arquivo."
                >
                  Só a chave
                </Button>
              </div>
            </div>
            {cred.chaveCriada && (
              <div style={{ ...CAIXA, borderColor: "var(--accent)" }}>
                <div style={{ fontSize: "var(--fs-13)", marginBottom: "var(--sp-2)" }}>
                  Chave de <strong>{cred.chaveCriada.hostname}</strong> criada. Rode isto no servidor,
                  como root:
                </div>
                <Comando texto={installCommand(cred.chaveCriada.serverkey, cred.limites)} />
                <div style={{ display: "flex", gap: "var(--sp-2)", marginTop: "var(--sp-2)" }}>
                  <Button onClick={() => copiar(installCommand(cred.chaveCriada!.serverkey, cred.limites), "Comando")}>
                    Copiar comando
                  </Button>
                  <Button variant="ghost" onClick={() => copiar(cred.chaveCriada!.serverkey, "Chave")}>
                    Copiar só a chave
                  </Button>
                </div>
              </div>
            )}
          </div>
        ))}

      {passo === "frota" &&
        (cred.baixado ? (
          <Resultado cred={cred} universal onOutro={() => cred.setBaixado(null)} onFechar={onClose} />
        ) : (
          <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-3)" }}>
            <p style={{ color: "var(--text-2)", fontSize: "var(--fs-13)", margin: 0 }}>
              Este arquivo não carrega uma chave: carrega um convite. Ao rodar, <strong>cada máquina
              pede ao painel a chave dela</strong>, por isso o mesmo arquivo serve para quantos
              servidores você quiser, e revogar um servidor não derruba os outros.
            </p>
            <div style={{ ...CAIXA, display: "flex", flexDirection: "column", gap: "var(--sp-3)" }}>
              <FormField
                label="Nome do lote"
                required
                hint="Serve para você saber, depois, quais servidores entraram por este arquivo."
              >
                <input
                  className="field"
                  placeholder="ex.: Mutirão agosto"
                  value={rotulo}
                  autoFocus
                  onChange={(e) => setRotulo(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" && rotulo.trim()) void gerarFrota();
                  }}
                />
              </FormField>
              <SeletorSO value={so} onChange={setSo} name="so-frota" />
              <Button variant="primary" disabled={ocupado || !rotulo.trim()} onClick={() => void gerarFrota()}>
                {cred.baixando ? "Gerando…" : "Gerar e baixar"}
              </Button>
            </div>
            <p style={{ color: "var(--text-3)", fontSize: "var(--fs-12)", margin: 0 }}>
              O arquivo vale até você revogá-lo. Quem o tiver consegue cadastrar servidores no painel
, não consegue ler dado nenhum nem entrar na sua conta. Se ele vazar, revogue o lote em{" "}
              <strong>Chaves de acesso</strong>: as entradas novas param na hora e quem já entrou
              continua reportando.
            </p>
          </div>
        ))}

      {passo === "existente" &&
        (cred.baixado ? (
          <Resultado
            cred={cred}
            universal={alvo?.tipo === "token"}
            onOutro={() => voltar("gerenciar")}
            onFechar={onClose}
            rotuloOutro="Voltar à lista"
          />
        ) : (
          <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-3)" }}>
            <p style={{ color: "var(--text-2)", fontSize: "var(--fs-13)", margin: 0 }}>
              {alvo?.tipo === "token" ? (
                <>
                  Gera o arquivo do lote <strong>{alvo.nome || "sem nome"}</strong> para outro sistema
                  operacional. Os servidores que entrarem por ele continuam contando no mesmo lugar.
                </>
              ) : (
                <>
                  Gera o instalador com a <strong>mesma chave</strong> de{" "}
                  <strong>{alvo?.nome || "servidor"}</strong>, para reinstalar sem criar uma chave
                  nova e sem deixar chave órfã na lista.
                </>
              )}
            </p>
            <div style={{ ...CAIXA, display: "flex", flexDirection: "column", gap: "var(--sp-3)" }}>
              <SeletorSO value={so} onChange={setSo} name="so-existente" />
              <Button variant="primary" disabled={ocupado} onClick={() => void gerarExistente()}>
                {cred.baixando ? "Gerando…" : "Baixar instalador"}
              </Button>
            </div>
          </div>
        ))}

      {passo === "ssh" && (
        <SshProvisionForm
          onProvisioned={() => {
            onProvisioned();
            onClose();
          }}
          onCancel={() => voltar("escolha")}
        />
      )}

      {passo === "gerenciar" && (
        <PassoGerenciar
          cred={cred}
          onCopiar={copiar}
          onBaixar={(a) => {
            setAlvo(a);
            cred.setBaixado(null);
            setPasso("existente");
          }}
        />
      )}
    </Modal>
  );

  async function gerar() {
    setOcupado(true);
    try {
      await cred.gerarInstalador(so, nome.trim());
    } finally {
      setOcupado(false);
    }
  }

  async function gerarFrota() {
    setOcupado(true);
    try {
      await cred.gerarUniversal(so, rotulo.trim());
    } finally {
      setOcupado(false);
    }
  }

  async function gerarExistente() {
    if (!alvo) return;
    setOcupado(true);
    try {
      await cred.baixarDeExistente(alvo, so);
    } finally {
      setOcupado(false);
    }
  }

  async function soAChave() {
    setOcupado(true);
    try {
      await cred.criarSoAChave(nome.trim());
    } catch (e) {
      toast.error(errMsg(e));
    } finally {
      setOcupado(false);
    }
  }
}

// ─── Passo 1: por qual caminho? ──────────────────────────────────────────────

interface Caminho {
  id: Passo;
  icone: typeof Download;
  titulo: string;
  onde: string;
  ondeEstado: "ok" | "warn" | "neutral";
  texto: string;
  requisito: string;
}

// A ordem é a do uso mais comum para o mais específico: instalar em vários de uma
// vez é o caso do dia a dia de quem opera uma frota; o SSH vem por último entre os
// que instalam porque é o mais restrito (Linux com systemd).
const CAMINHOS: Caminho[] = [
  {
    id: "frota",
    icone: Layers,
    titulo: "Baixar o instalador UNIVERSAL",
    onde: "Linux · Windows · macOS",
    ondeEstado: "ok",
    texto:
      "Um arquivo só para a frota inteira. Ele não carrega uma chave: carrega um convite. Ao rodar, cada máquina pede ao painel a chave DELA, por isso o mesmo arquivo serve para quantos servidores você quiser, e revogar um servidor não derruba os outros.",
    requisito:
      "Vale até você revogar o lote. Quem tiver o arquivo consegue cadastrar servidores no painel (não consegue ler dado nenhum nem entrar na sua conta), e cada entrada fica registrada na Auditoria.",
  },
  {
    id: "arquivo",
    icone: Download,
    // Nome deliberadamente diferente de "Baixar o instalador UNIVERSAL": dois
    // cartões começando igual obrigam a ler o parágrafo inteiro para saber qual é
    // qual. Aqui a primeira palavra já separa os dois.
    titulo: "Configurar um servidor específico",
    onde: "Linux · Windows · macOS",
    ondeEstado: "ok",
    texto:
      "Um arquivo para UM servidor, com a chave dele já dentro. Você leva até a máquina e roda: ele instala o agente e o deixa subindo junto com o sistema.",
    requisito: "Você precisa conseguir copiar um arquivo para o servidor e executá-lo como administrador.",
  },
  {
    id: "ssh",
    icone: Terminal,
    titulo: "Instalar por SSH, a partir do painel",
    onde: "Só Linux com systemd",
    ondeEstado: "warn",
    texto:
      "O painel conecta no servidor e faz tudo sozinho, gera a chave, instala o agente e sobe o serviço, mostrando cada passo ao vivo. Você não toca na máquina.",
    requisito:
      "Você precisa de IP, usuário e senha (ou chave SSH) com permissão de root. Windows e macOS não entram por aqui.",
  },
];

function Escolha({
  onEscolher,
  chaves,
  lotes,
}: {
  onEscolher: (p: Passo) => void;
  chaves: number;
  lotes: number;
}) {
  // O quarto caminho não instala nada: administra o que já existe. Entra na mesma
  // lista (e não num link solto no rodapé) porque é para lá que se vai quando o
  // assunto é "tirar um servidor do ar", que é tão comum quanto colocar um.
  const gestao: Caminho = {
    id: "gerenciar",
    icone: KeyRound,
    titulo: "Chaves de acesso",
    onde: `${chaves} chave${chaves === 1 ? "" : "s"} · ${lotes} lote${lotes === 1 ? "" : "s"}`,
    ondeEstado: "neutral",
    texto:
      "A lista das chaves de ingestão e dos instaladores universais já criados. Aqui você revoga (corta o envio na hora) ou exclui de vez, e baixa de novo o instalador de uma chave que já existe.",
    requisito:
      "Revogar a chave de um servidor derruba só ele. Revogar um instalador universal impede entradas novas e não afeta quem já entrou.",
  };

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-3)" }}>
      <p style={{ color: "var(--text-2)", fontSize: "var(--fs-13)", margin: 0 }}>
        O agente é um programinha que roda no servidor e envia CPU, memória, disco e rede para cá.
        Escolha como ele vai chegar até lá:
      </p>

      {[...CAMINHOS, gestao].map((c) => {
        const Icone = c.icone;
        // aria-labelledby/describedby em vez de deixar o botão herdar todo o texto
        // como nome: sem isto, um leitor de tela anuncia o parágrafo inteiro como
        // sendo o nome do botão. Assim o nome é o título e a explicação vem depois,
        // como descrição — que é como a pessoa vidente também lê o cartão.
        return (
          <button
            key={c.id}
            type="button"
            className="opcao"
            aria-labelledby={`opcao-${c.id}-titulo`}
            aria-describedby={`opcao-${c.id}-texto`}
            onClick={() => onEscolher(c.id)}
          >
            <Icone size={22} className="opcao__icone" aria-hidden />
            <span style={{ minWidth: 0 }}>
              <span className="opcao__titulo">
                <span id={`opcao-${c.id}-titulo`}>{c.titulo}</span>
                <Badge state={c.ondeEstado}>{c.onde}</Badge>
              </span>
              <p id={`opcao-${c.id}-texto`} className="opcao__texto">
                {c.texto}
              </p>
              <p className="opcao__requisito">{c.requisito}</p>
            </span>
          </button>
        );
      })}
    </div>
  );
}

// ─── Resultado do download ───────────────────────────────────────────────────

function Resultado({
  cred,
  universal,
  onOutro,
  onFechar,
  rotuloOutro = "Adicionar outro servidor",
}: {
  cred: ReturnType<typeof useCredenciais>;
  universal?: boolean;
  onOutro: () => void;
  onFechar: () => void;
  rotuloOutro?: string;
}) {
  const b = cred.baixado;
  if (!b) return null;
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-3)" }}>
      <div style={{ ...CAIXA, borderColor: "var(--ok)" }}>
        <div style={{ fontSize: "var(--fs-13)", marginBottom: "var(--sp-1)" }}>
          Baixado: <code style={{ fontFamily: "var(--font-mono)" }}>{b.arquivo}</code>
        </div>
        <p style={{ fontSize: "var(--fs-13)", color: "var(--text-1)", margin: 0 }}>{comoRodar(b.os, b.arquivo)}</p>
        <p style={{ fontSize: "var(--fs-12)", color: "var(--text-3)", margin: "var(--sp-2) 0 0" }}>
          {universal ? (
            <>
              Guarde o arquivo como uma senha: quem o tiver consegue cadastrar servidores no painel.
              Para estancar, revogue o lote em <strong>Chaves de acesso</strong>.
            </>
          ) : (
            <>
              O arquivo carrega a chave deste servidor: trate como senha e apague depois de instalar.
            </>
          )}
        </p>
      </div>
      <p style={{ color: "var(--text-2)", fontSize: "var(--fs-13)", margin: 0 }}>
        O servidor aparece na lista em até um minuto, assim que enviar as primeiras métricas.
      </p>
      <div style={{ display: "flex", gap: "var(--sp-2)", flexWrap: "wrap" }}>
        <Button variant="primary" onClick={onFechar}>
          Concluir
        </Button>
        <Button onClick={onOutro}>{rotuloOutro}</Button>
      </div>
    </div>
  );
}

// Comando pronto para copiar — bloco monoespaçado que quebra em telas estreitas.
function Comando({ texto }: { texto: string }) {
  return (
    <pre
      style={{
        margin: 0,
        padding: "var(--sp-2)",
        background: "var(--bg-0)",
        border: "1px solid var(--border)",
        borderRadius: "var(--radius-sm)",
        overflowX: "auto",
        whiteSpace: "pre-wrap",
        wordBreak: "break-all",
        fontFamily: "var(--font-mono)",
        fontSize: "var(--fs-12)",
        color: "var(--text-1)",
      }}
    >
      {texto}
    </pre>
  );
}
