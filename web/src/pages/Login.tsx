import { useEffect, useState } from "react";
import { Button, FormField } from "../components";
import { Telemetria } from "../components/Telemetria";
import "./login.css";
import { BrandLogo } from "../components/BrandLogo";
import { login, loadMe, changePassword, verificarMFA, estadoInicial, register } from "../api";

type Etapa = "entrar" | "mfa" | "nova-senha" | "configurar";

const MIN_SENHA = 12;

export function Login({ onDone }: { onDone: () => void }) {
  const [etapa, setEtapa] = useState<Etapa>("entrar");
  const [username, setUsername] = useState("");
  const [password, setPassword] = useState("");
  const [err, setErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  // 2ª etapa (2FA): o desafio prova que a senha já foi conferida.
  const [desafio, setDesafio] = useState("");
  const [codigo, setCodigo] = useState("");
  // Senha provisória a trocar, ou senha do primeiro administrador.
  const [newPass, setNewPass] = useState("");
  const [newPass2, setNewPass2] = useState("");

  // Sistema sem nenhum usuário: a tela vira "criar o administrador".
  useEffect(() => {
    let vivo = true;
    estadoInicial().then((e) => {
      if (vivo && e.precisaConfigurar) setEtapa("configurar");
    });
    return () => {
      vivo = false;
    };
  }, []);

  const concluir = async (mustReset: boolean) => {
    if (mustReset) {
      setEtapa("nova-senha");
      return;
    }
    await loadMe().catch(() => undefined);
    onDone();
  };

  const rodar = async (fn: () => Promise<void>, falha: string) => {
    setErr(null);
    setBusy(true);
    try {
      await fn();
    } catch (e) {
      setErr(e instanceof Error && e.message ? e.message : falha);
    } finally {
      setBusy(false);
    }
  };

  const submit = (e: React.FormEvent) => {
    e.preventDefault();
    void rodar(async () => {
      const r = await login(username, password);
      if (r.tipo === "mfa") {
        setDesafio(r.desafio);
        setCodigo("");
        setEtapa("mfa");
        return;
      }
      await concluir(r.mustReset);
    }, "Usuário ou senha inválidos.");
  };

  const submitCodigo = (e: React.FormEvent) => {
    e.preventDefault();
    void rodar(async () => {
      const r = await verificarMFA(desafio, codigo);
      await concluir(r.mustReset);
    }, "Código inválido.");
  };

  const conferirNovaSenha = (): string | null => {
    if (newPass.length < MIN_SENHA) return `A senha precisa ter pelo menos ${MIN_SENHA} caracteres.`;
    if (newPass !== newPass2) return "As senhas não conferem.";
    return null;
  };

  const submitNewPassword = (e: React.FormEvent) => {
    e.preventDefault();
    const problema = conferirNovaSenha();
    if (problema) {
      setErr(problema);
      return;
    }
    void rodar(async () => {
      // No primeiro acesso a senha atual não é exigida pelo backend (must_reset).
      await changePassword(password, newPass);
      await loadMe().catch(() => undefined);
      onDone();
    }, "Não foi possível trocar a senha. Tente novamente.");
  };

  const submitConfigurar = (e: React.FormEvent) => {
    e.preventDefault();
    const problema = conferirNovaSenha();
    if (problema) {
      setErr(problema);
      return;
    }
    void rodar(async () => {
      await register(username, newPass);
      const r = await login(username, newPass);
      if (r.tipo === "ok") await concluir(r.mustReset);
    }, "Não foi possível criar o administrador.");
  };

  const erro = err && (
    <span role="alert" className="login__erro">
      {err}
    </span>
  );

  return (
    <div className="login">
      {/* Vitrine (só desktop): o produto respirando ao fundo — linhas de métrica
          correndo como num painel de verdade. */}
      <aside className="login__vitrine" aria-label="Sobre o painel">
        <Telemetria />
        <div className="login__vitrine-texto">
          <span className="login__selo">
            <span className="login__selo-ponto" /> monitorando agora
          </span>
          <p className="login__manchete">Cada servidor, cada site, cada alerta, num lugar só.</p>
          <p className="login__apoio">
            Métricas em tempo real, avisos no WhatsApp e no e-mail, e a resposta para “está tudo bem?” antes de
            qualquer gráfico.
          </p>
        </div>
      </aside>

      <main className="login__lado">
        <div className="login__caixa">
          <div className="login__marca">
            <BrandLogo variant="full" size="lg" />
          </div>
          {etapa === "configurar" && (
            <>
              <h1 className="login__titulo">Bem-vindo ao Revoada</h1>
              <p className="login__sub">
                Este painel ainda não tem nenhum usuário. Crie agora o administrador — ele poderá convidar o resto da
                equipe depois.
              </p>
              <form onSubmit={submitConfigurar} className="login__form">
                <FormField label="Email do administrador">
                  <input
                    className="field"
                    autoFocus
                    autoComplete="username"
                    placeholder="voce@empresa.com.br"
                    value={username}
                    onChange={(e) => setUsername(e.target.value)}
                  />
                </FormField>
                <FormField label="Senha" hint={`Pelo menos ${MIN_SENHA} caracteres. Uma frase longa é mais forte que símbolos.`}>
                  <input
                    className="field"
                    type="password"
                    autoComplete="new-password"
                    value={newPass}
                    onChange={(e) => setNewPass(e.target.value)}
                  />
                </FormField>
                <FormField label="Repita a senha">
                  <input
                    className="field"
                    type="password"
                    autoComplete="new-password"
                    value={newPass2}
                    onChange={(e) => setNewPass2(e.target.value)}
                  />
                </FormField>
                {erro}
                <Button variant="primary" type="submit" disabled={busy || !username}>
                  {busy ? "Criando…" : "Criar administrador e entrar"}
                </Button>
              </form>
              <p className="login__rodape">
                No primeiro acesso você vai ativar a verificação em duas etapas (2FA) — ela é obrigatória para
                administradores.
              </p>
            </>
          )}

          {etapa === "mfa" && (
            <>
              <h1 className="login__titulo">Verificação em duas etapas</h1>
              <p className="login__sub">
                Abra o app autenticador no celular e digite o código de 6 dígitos do Revoada. Sem o celular? Use um dos
                códigos de recuperação.
              </p>
              <form onSubmit={submitCodigo} className="login__form">
                <FormField label="Código">
                  <input
                    className="field login__codigo"
                    autoFocus
                    inputMode="text"
                    autoComplete="one-time-code"
                    placeholder="000000"
                    maxLength={11}
                    value={codigo}
                    onChange={(e) => setCodigo(e.target.value.toUpperCase())}
                  />
                </FormField>
                {erro}
                <Button variant="primary" type="submit" disabled={busy || codigo.trim().length < 6}>
                  {busy ? "Conferindo…" : "Confirmar"}
                </Button>
                <Button
                  variant="ghost"
                  type="button"
                  onClick={() => {
                    setEtapa("entrar");
                    setErr(null);
                  }}
                >
                  Voltar
                </Button>
              </form>
            </>
          )}

          {etapa === "nova-senha" && (
            <>
              <h1 className="login__titulo">Defina uma nova senha</h1>
              <p className="login__sub">Sua senha é provisória. Escolha uma nova para concluir o primeiro acesso.</p>
              <form onSubmit={submitNewPassword} className="login__form">
                <FormField label="Nova senha" hint={`Pelo menos ${MIN_SENHA} caracteres.`}>
                  <input
                    className="field"
                    type="password"
                    autoComplete="new-password"
                    autoFocus
                    value={newPass}
                    onChange={(e) => setNewPass(e.target.value)}
                  />
                </FormField>
                <FormField label="Repita a nova senha">
                  <input
                    className="field"
                    type="password"
                    autoComplete="new-password"
                    value={newPass2}
                    onChange={(e) => setNewPass2(e.target.value)}
                  />
                </FormField>
                {erro}
                <Button variant="primary" type="submit" disabled={busy}>
                  {busy ? "Salvando…" : "Salvar e entrar"}
                </Button>
              </form>
            </>
          )}

          {etapa === "entrar" && (
            <>
              <h1 className="login__titulo">Entrar no painel</h1>
              <p className="login__sub">Observabilidade e migração de bancos de dados, no seu servidor.</p>
              <form onSubmit={submit} className="login__form">
                <FormField label="Email">
                  <input
                    className="field"
                    autoFocus
                    autoComplete="username"
                    placeholder="seu email de acesso"
                    value={username}
                    onChange={(e) => setUsername(e.target.value)}
                  />
                </FormField>
                <FormField label="Senha">
                  <input
                    className="field"
                    type="password"
                    autoComplete="current-password"
                    value={password}
                    onChange={(e) => setPassword(e.target.value)}
                  />
                </FormField>
                {erro}
                <Button variant="primary" type="submit" disabled={busy}>
                  {busy ? "Entrando…" : "Entrar"}
                </Button>
              </form>
              <p className="login__rodape">Esqueceu a senha? Peça a um administrador para redefini-la.</p>
            </>
          )}
        </div>
      </main>
    </div>
  );
}
