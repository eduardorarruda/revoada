// ReauthModal: quando uma ação crítica responde "confirme sua identidade", o cliente
// da API chama este modal, espera a pessoa confirmar e repete a ação sozinho.
import { useCallback, useEffect, useRef, useState } from "react";
import { Button, FormField } from ".";
import { Modal } from "./Modal";
import { getMe, loadMe, reautenticar, registrarPedidoReauth } from "../api";
import "./reauth.css";

export function ReauthModal() {
  const [aberto, setAberto] = useState(false);
  const [valor, setValor] = useState("");
  const [erro, setErro] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [comMFA, setComMFA] = useState(false);
  const resolver = useRef<((ok: boolean) => void) | null>(null);

  const pedir = useCallback(async () => {
    const me = getMe() ?? (await loadMe().catch(() => null));
    setComMFA(me?.mfa_ativo === true);
    setValor("");
    setErro(null);
    setAberto(true);
    return new Promise<boolean>((res) => {
      resolver.current = res;
    });
  }, []);

  useEffect(() => {
    registrarPedidoReauth(pedir);
    return () => registrarPedidoReauth(null);
  }, [pedir]);

  const fechar = (ok: boolean) => {
    setAberto(false);
    resolver.current?.(ok);
    resolver.current = null;
  };

  const enviar = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setErro(null);
    try {
      await reautenticar(comMFA ? { codigo: valor.trim() } : { senha: valor });
      fechar(true);
    } catch (e) {
      const m = e instanceof Error ? e.message.replace(/^\d{3}:\s*/, "") : "";
      setErro(m || "Não confere. Tente de novo.");
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal open={aberto} onClose={() => fechar(false)} title="Confirme que é você">
      <form onSubmit={enviar} className="reauth">
        <p className="reauth__texto">
          Esta é uma ação crítica. {comMFA ? "Digite o código atual do seu app autenticador." : "Digite sua senha."} A
          confirmação vale por 5 minutos.
        </p>
        <FormField label={comMFA ? "Código do app" : "Senha"}>
          <input
            className="field"
            autoFocus
            type={comMFA ? "text" : "password"}
            inputMode={comMFA ? "numeric" : undefined}
            autoComplete={comMFA ? "one-time-code" : "current-password"}
            maxLength={comMFA ? 6 : undefined}
            value={valor}
            onChange={(e) => setValor(comMFA ? e.target.value.replace(/\D/g, "") : e.target.value)}
          />
        </FormField>
        {erro && (
          <p role="alert" className="reauth__erro">
            {erro}
          </p>
        )}
        <div className="reauth__acoes">
          <Button variant="ghost" type="button" onClick={() => fechar(false)}>
            Cancelar
          </Button>
          <Button variant="primary" type="submit" disabled={busy || !valor}>
            {busy ? "Conferindo…" : "Confirmar"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
