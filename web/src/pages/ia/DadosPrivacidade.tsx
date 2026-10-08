// Dados e privacidade (aba Modelos e preços, só administrador): apagar o que foi
// gravado das chamadas de IA. Três pedidos, do menor para o maior:
//  - uma conversa (pedido de titular de dado, LGPD): chamadas + conteúdo;
//  - uma execução (trace): chamadas + conteúdo;
//  - todo o conteúdo gravado (prompts e respostas): só o texto, custo e tokens ficam.
// O servidor também barra quem não é administrador, e cada pedido vai para a
// auditoria. As remoções do ClickHouse são assíncronas: levam alguns segundos.
import { useState, type FormEvent } from "react";
import { FRASE_PURGE_TODO_CONTEUDO, purgeIa, type IaPurge } from "../../api.ia";
import { Button, Collapsible, ConfirmDialog, FormField, useToast } from "../../components";
import { mensagemDeErro } from "../../format";

/** Mesmo teto do servidor para o id da conversa. */
const MAX_CONVERSA = 256;
/** Trace id: hexadecimal, até 64 caracteres (mesma regra do servidor). */
const TRACE_ID = /^[0-9a-f]{1,64}$/;

const DEMORA = "A remoção roda em segundo plano no banco e leva alguns segundos para sumir das telas.";

type AlvoPorId = "conversa" | "trace";

interface ConfigAlvo {
  titulo: string;
  rotulo: string;
  ajuda: string;
  placeholder: string;
  botao: string;
  alvoConfirmacao: (v: string) => string;
  consequencias: string;
  /** Normaliza e valida: devolve o valor a mandar ou a mensagem de erro. */
  validar: (texto: string) => { valor: string } | { erro: string };
}

const CONSEQUENCIA_CHAMADAS =
  "As chamadas de IA e o conteúdo (prompts e respostas) somem das telas, junto com o custo e os tokens delas. Os totais por minuto usados nos alertas não mudam. Não dá para desfazer.";

const ALVOS: Record<AlvoPorId, ConfigAlvo> = {
  conversa: {
    titulo: "Apagar uma conversa",
    rotulo: "Id da conversa",
    ajuda: "O id que a aplicação manda como gen_ai.conversation.id. Aparece no topo do replay de cada execução (“conversa …”). Apaga todas as execuções dessa conversa.",
    placeholder: "ex.: conv-8f2a",
    botao: "Apagar conversa",
    alvoConfirmacao: (v) => `a conversa ${v}`,
    consequencias: `${CONSEQUENCIA_CHAMADAS} ${DEMORA}`,
    validar: (texto) => {
      const v = texto.trim();
      if (!v) return { erro: "Informe o id da conversa." };
      if (v.length > MAX_CONVERSA) return { erro: `O id da conversa tem no máximo ${MAX_CONVERSA} caracteres.` };
      return { valor: v };
    },
  },
  trace: {
    titulo: "Apagar uma execução",
    rotulo: "Id da execução (trace id)",
    ajuda: "O trace id da execução, em hexadecimal. É o final do endereço do replay: #/ia/execucoes/<id>.",
    placeholder: "ex.: 4bf92f3577b34da6a3ce929d0e0e4736",
    botao: "Apagar execução",
    alvoConfirmacao: (v) => `a execução ${v}`,
    consequencias: `${CONSEQUENCIA_CHAMADAS} ${DEMORA}`,
    validar: (texto) => {
      const v = texto.trim().toLowerCase();
      if (!v) return { erro: "Informe o id da execução." };
      if (!TRACE_ID.test(v)) return { erro: "O id da execução só tem 0–9 e a–f (até 64 caracteres), como 4bf92f3577b34da6." };
      return { valor: v };
    },
  },
};

/** Manda o pedido e conta o resultado num toast. Devolve se deu certo. */
function usePedirRemocao() {
  const toast = useToast();
  const [enviando, setEnviando] = useState(false);
  const pedir = async (p: IaPurge, sucesso: string): Promise<boolean> => {
    setEnviando(true);
    try {
      await purgeIa(p);
      toast.success(`${sucesso} ${DEMORA}`);
      return true;
    } catch (e) {
      toast.error(mensagemDeErro(e, "Não foi possível pedir a remoção."));
      return false;
    } finally {
      setEnviando(false);
    }
  };
  return { pedir, enviando };
}

function ApagarPorId({ alvo }: { alvo: AlvoPorId }) {
  const cfg = ALVOS[alvo];
  const { pedir, enviando } = usePedirRemocao();
  const [texto, setTexto] = useState("");
  const [erro, setErro] = useState<string>();
  const [confirmar, setConfirmar] = useState<string | null>(null);

  const enviar = (e: FormEvent) => {
    e.preventDefault();
    const r = cfg.validar(texto);
    if ("erro" in r) {
      setErro(r.erro);
      return;
    }
    setErro(undefined);
    setConfirmar(r.valor);
  };
  const confirmado = async () => {
    const valor = confirmar;
    setConfirmar(null);
    if (valor && (await pedir({ alvo, valor }, `Pedido para apagar ${cfg.alvoConfirmacao(valor)} enviado.`))) setTexto("");
  };

  return (
    <form className="ia-purge__bloco" onSubmit={enviar} noValidate aria-label={cfg.titulo}>
      <h4 className="ia-purge__titulo">{cfg.titulo}</h4>
      <div className="ia-purge__linha">
        <FormField label={cfg.rotulo} help={cfg.ajuda} error={erro}>
          <input className="field" value={texto} placeholder={cfg.placeholder} onChange={(e) => setTexto(e.target.value)} />
        </FormField>
        <Button type="submit" className="btn--danger" disabled={enviando}>
          {enviando ? "Pedindo…" : cfg.botao}
        </Button>
      </div>
      <ConfirmDialog
        open={confirmar !== null}
        onCancel={() => setConfirmar(null)}
        onConfirm={confirmado}
        verb="Apagar"
        target={cfg.alvoConfirmacao(confirmar ?? "")}
        consequences={cfg.consequencias}
        danger
      />
    </form>
  );
}

function ApagarTodoConteudo() {
  const { pedir, enviando } = usePedirRemocao();
  const [frase, setFrase] = useState("");
  const [confirmar, setConfirmar] = useState(false);
  const confere = frase === FRASE_PURGE_TODO_CONTEUDO;

  const enviar = (e: FormEvent) => {
    e.preventDefault();
    if (confere) setConfirmar(true);
  };
  const confirmado = async () => {
    setConfirmar(false);
    if (await pedir({ alvo: "todo_conteudo", frase }, "Pedido para apagar todo o conteúdo gravado enviado.")) setFrase("");
  };

  return (
    <form className="ia-purge__bloco" onSubmit={enviar} noValidate aria-label="Apagar todo o conteúdo gravado">
      <h4 className="ia-purge__titulo">Apagar todo o conteúdo gravado</h4>
      <p className="ia-texto">
        Apaga todos os prompts e respostas guardados. Custo, tokens, latência e o passo a passo das execuções continuam: só o
        texto some.
      </p>
      <div className="ia-purge__linha">
        <FormField
          label="Para confirmar, digite a frase"
          hint={`Digite exatamente: ${FRASE_PURGE_TODO_CONTEUDO}`}
          error={frase !== "" && !confere ? "A frase ainda não confere (maiúsculas e acentos contam)." : undefined}
        >
          <input className="field" value={frase} autoComplete="off" spellCheck={false} onChange={(e) => setFrase(e.target.value)} />
        </FormField>
        <Button type="submit" className="btn--danger" disabled={!confere || enviando}>
          {enviando ? "Pedindo…" : "Apagar todo o conteúdo"}
        </Button>
      </div>
      <ConfirmDialog
        open={confirmar}
        onCancel={() => setConfirmar(false)}
        onConfirm={confirmado}
        verb="Apagar"
        target="todo o conteúdo gravado das chamadas de IA"
        consequences={`Todos os prompts e respostas guardados são apagados. Custo, tokens e o passo a passo continuam. Não dá para desfazer. ${DEMORA}`}
        danger
      />
    </form>
  );
}

/** Seção discreta, fechada por padrão: só administrador a recebe (quem chama decide). */
export function DadosPrivacidade() {
  return (
    <Collapsible titulo="Dados e privacidade" resumo="apagar conversa, execução ou conteúdo gravado">
      <div className="stack">
        <p className="ia-texto">
          Para atender um pedido de titular de dado (LGPD) ou limpar o que foi gravado. Cada pedido fica registrado na
          Auditoria. {DEMORA}
        </p>
        <ApagarPorId alvo="conversa" />
        <ApagarPorId alvo="trace" />
        <ApagarTodoConteudo />
      </div>
    </Collapsible>
  );
}
