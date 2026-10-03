// Segurança da conta: 2FA (TOTP), troca de senha e o que o papel da pessoa permite.
// Toda regra mora no back-end; esta tela explica e conduz o passo a passo.
import { useEffect, useState } from "react";
import { motion, useReducedMotion } from "motion/react";
import { Badge, Button, Card, FormField, PageHeader } from "../components";
import { useToast } from "../components/Toast";
import {
  NOME_PAPEL,
  changePassword,
  confirmarMFA,
  desativarMFA,
  getMe,
  iniciarMFA,
  loadMe,
  type InicioMFA,
  type MeResponse,
} from "../api";
import "./seguranca.css";

type Passo = "parado" | "ler-qr" | "codigos";

const PODE_POR_PAPEL: Record<string, string[]> = {
  admin: [
    "Tudo o que o operador faz",
    "Cadastrar conexões de banco e credenciais",
    "Inscrever e revogar agentes",
    "Gerenciar usuários, papéis e tokens",
  ],
  operador: [
    "Ver painéis, execuções e relatórios",
    "Criar e editar mapeamentos de migração",
    "Simular, executar, pausar e reverter migrações",
    "Rodar deploys",
  ],
  leitor: ["Ver painéis, execuções e relatórios", "Nada que altere servidores ou bancos"],
};

export function Seguranca() {
  const toast = useToast();
  const reduzir = useReducedMotion();
  const [me, setMe] = useState<MeResponse | null>(getMe());
  const [passo, setPasso] = useState<Passo>("parado");
  const [inicio, setInicio] = useState<InicioMFA | null>(null);
  const [codigo, setCodigo] = useState("");
  const [codigos, setCodigos] = useState<string[]>([]);
  const [guardou, setGuardou] = useState(false);
  const [busy, setBusy] = useState(false);
  const [erro, setErro] = useState<string | null>(null);

  useEffect(() => {
    loadMe()
      .then(setMe)
      .catch(() => undefined);
  }, []);

  const papel = me?.role ?? "leitor";
  const ativo = me?.mfa_ativo === true;

  const comecar = async () => {
    setErro(null);
    setBusy(true);
    try {
      setInicio(await iniciarMFA());
      setCodigo("");
      setPasso("ler-qr");
    } catch (e) {
      setErro(mensagem(e));
    } finally {
      setBusy(false);
    }
  };

  const confirmar = async (e: React.FormEvent) => {
    e.preventDefault();
    setErro(null);
    setBusy(true);
    try {
      const r = await confirmarMFA(codigo.trim());
      setCodigos(r.codigos_recuperacao);
      setPasso("codigos");
      setMe(await loadMe());
    } catch (e) {
      setErro(mensagem(e));
    } finally {
      setBusy(false);
    }
  };

  const desativar = async () => {
    setErro(null);
    setBusy(true);
    try {
      await desativarMFA();
      setMe(await loadMe());
      toast.success("Verificação em duas etapas desativada.");
    } catch (e) {
      setErro(mensagem(e));
    } finally {
      setBusy(false);
    }
  };

  const baixarCodigos = () => {
    const texto = `Revoada — códigos de recuperação de ${me?.username ?? ""}\nCada código vale UMA vez.\n\n${codigos.join("\n")}\n`;
    const url = URL.createObjectURL(new Blob([texto], { type: "text/plain" }));
    const a = document.createElement("a");
    a.href = url;
    a.download = "revoada-codigos-de-recuperacao.txt";
    a.click();
    URL.revokeObjectURL(url);
  };

  const entrada = reduzir ? {} : { initial: { opacity: 0, y: 8 }, animate: { opacity: 1, y: 0 } };

  return (
    <div className="page seguranca">
      <PageHeader
        title="Segurança da conta"
        subtitle="Proteja o acesso ao painel: ele guarda credenciais de bancos e executa tarefas nos seus servidores."
      />

      <div className="seguranca__grade">
        <Card
          title={
            <span className="seguranca__titulo">
              Verificação em duas etapas (2FA)
              <Badge state={ativo ? "ok" : me?.mfa_obrigatorio ? "crit" : "warn"}>
                {ativo ? "Ativa" : me?.mfa_obrigatorio ? "Obrigatória — pendente" : "Desativada"}
              </Badge>
            </span>
          }
        >
          {passo === "parado" && (
            <motion.div {...entrada} className="seguranca__bloco">
              <p className="seguranca__texto">
                Além da senha, o login pede um código de 6 dígitos que muda a cada 30 segundos no seu celular. Mesmo que
                alguém descubra sua senha, não entra sem o aparelho.
              </p>
              {me?.mfa_obrigatorio && !ativo && (
                <p className="seguranca__aviso" role="note">
                  Para o papel {NOME_PAPEL[papel] ?? papel}, o 2FA é obrigatório: sem ele, ações como executar
                  migrações e deploys ficam bloqueadas.
                </p>
              )}
              {erro && (
                <p role="alert" className="seguranca__erro">
                  {erro}
                </p>
              )}
              {ativo ? (
                me?.mfa_obrigatorio ? (
                  <p className="seguranca__texto seguranca__texto--fraco">
                    Perdeu o celular? Peça a um administrador para redefinir o seu 2FA.
                  </p>
                ) : (
                  <Button onClick={desativar} disabled={busy}>
                    Desativar 2FA
                  </Button>
                )
              ) : (
                <Button variant="primary" onClick={comecar} disabled={busy}>
                  {busy ? "Preparando…" : "Ativar 2FA"}
                </Button>
              )}
            </motion.div>
          )}

          {passo === "ler-qr" && inicio && (
            <motion.form {...entrada} className="seguranca__bloco" onSubmit={confirmar}>
              <ol className="seguranca__passos">
                <li>
                  Instale um app autenticador no celular (Google Authenticator, Microsoft Authenticator, Authy ou
                  1Password).
                </li>
                <li>No app, escolha “adicionar conta” e aponte a câmera para o código abaixo.</li>
                <li>Digite o código de 6 dígitos que o app mostrar.</li>
              </ol>
              <div className="seguranca__qr">
                <img src={inicio.qr} alt="QR code para o app autenticador" width={200} height={200} />
                <div>
                  <span className="seguranca__rotulo">A câmera não leu? Digite esta chave no app:</span>
                  <code className="seguranca__chave">{agrupar(inicio.segredo)}</code>
                </div>
              </div>
              <FormField label="Código do app">
                <input
                  className="field seguranca__codigo"
                  inputMode="numeric"
                  autoComplete="one-time-code"
                  autoFocus
                  maxLength={6}
                  placeholder="000000"
                  value={codigo}
                  onChange={(e) => setCodigo(e.target.value.replace(/\D/g, ""))}
                />
              </FormField>
              {erro && (
                <p role="alert" className="seguranca__erro">
                  {erro}
                </p>
              )}
              <div className="seguranca__acoes">
                <Button variant="primary" type="submit" disabled={busy || codigo.length !== 6}>
                  {busy ? "Conferindo…" : "Confirmar e ativar"}
                </Button>
                <Button variant="ghost" type="button" onClick={() => setPasso("parado")}>
                  Cancelar
                </Button>
              </div>
            </motion.form>
          )}

          {passo === "codigos" && (
            <motion.div {...entrada} className="seguranca__bloco">
              <p className="seguranca__texto">
                <strong>2FA ativado.</strong> Guarde estes códigos de recuperação num lugar seguro (gerenciador de
                senhas ou papel). Cada um vale <strong>uma vez</strong> e é a única forma de entrar se você perder o
                celular. Eles não serão mostrados de novo.
              </p>
              <ul className="seguranca__lista-codigos">
                {codigos.map((c, i) => (
                  <motion.li
                    key={c}
                    initial={reduzir ? false : { opacity: 0, scale: 0.96 }}
                    animate={{ opacity: 1, scale: 1 }}
                    transition={{ delay: reduzir ? 0 : i * 0.03 }}
                  >
                    <code>{c}</code>
                  </motion.li>
                ))}
              </ul>
              <div className="seguranca__acoes">
                <Button onClick={baixarCodigos}>Baixar .txt</Button>
                <Button
                  onClick={() =>
                    navigator.clipboard
                      .writeText(codigos.join("\n"))
                      .then(() => toast.success("Códigos copiados."))
                      .catch(() => toast.error("Não deu para copiar; use o botão de baixar."))
                  }
                >
                  Copiar
                </Button>
              </div>
              <label className="seguranca__check">
                <input type="checkbox" checked={guardou} onChange={(e) => setGuardou(e.target.checked)} /> Guardei os
                códigos em um lugar seguro
              </label>
              <Button variant="primary" disabled={!guardou} onClick={() => setPasso("parado")}>
                Concluir
              </Button>
            </motion.div>
          )}
        </Card>

        <Card title={`Seu papel: ${me?.papel_nome ?? NOME_PAPEL[papel] ?? papel}`}>
          <p className="seguranca__texto">O que o seu papel permite fazer:</p>
          <ul className="seguranca__permissoes">
            {(PODE_POR_PAPEL[papel] ?? PODE_POR_PAPEL.leitor).map((p) => (
              <li key={p}>{p}</li>
            ))}
          </ul>
          <p className="seguranca__texto seguranca__texto--fraco">
            Ações críticas (executar migração, deploy, mexer em credenciais ou usuários) pedem que você confirme sua
            identidade de novo. A confirmação vale por 5 minutos.
          </p>
        </Card>

        <TrocarSenha />
      </div>
    </div>
  );
}

function TrocarSenha() {
  const toast = useToast();
  const [atual, setAtual] = useState("");
  const [nova, setNova] = useState("");
  const [nova2, setNova2] = useState("");
  const [erro, setErro] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const enviar = async (e: React.FormEvent) => {
    e.preventDefault();
    setErro(null);
    if (nova.length < 12) return setErro("A nova senha precisa ter pelo menos 12 caracteres.");
    if (nova !== nova2) return setErro("As senhas não conferem.");
    setBusy(true);
    try {
      await changePassword(atual, nova);
      setAtual("");
      setNova("");
      setNova2("");
      toast.success("Senha trocada.");
    } catch (e) {
      setErro(mensagem(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Card title="Trocar senha">
      <form className="seguranca__bloco" onSubmit={enviar}>
        <FormField label="Senha atual">
          <input className="field" type="password" autoComplete="current-password" value={atual} onChange={(e) => setAtual(e.target.value)} />
        </FormField>
        <FormField label="Nova senha" hint="Pelo menos 12 caracteres. Uma frase longa é mais forte que símbolos.">
          <input className="field" type="password" autoComplete="new-password" value={nova} onChange={(e) => setNova(e.target.value)} />
        </FormField>
        <FormField label="Repita a nova senha">
          <input className="field" type="password" autoComplete="new-password" value={nova2} onChange={(e) => setNova2(e.target.value)} />
        </FormField>
        {erro && (
          <p role="alert" className="seguranca__erro">
            {erro}
          </p>
        )}
        <Button type="submit" disabled={busy || !atual || !nova}>
          {busy ? "Salvando…" : "Trocar senha"}
        </Button>
      </form>
    </Card>
  );
}

// mensagem tira o prefixo "403: " que o cliente da API põe nos erros.
function mensagem(e: unknown): string {
  const m = e instanceof Error ? e.message : String(e);
  return m.replace(/^\d{3}:\s*/, "").trim() || "Algo deu errado. Tente de novo.";
}

function agrupar(s: string): string {
  return s.replace(/(.{4})/g, "$1 ").trim();
}
