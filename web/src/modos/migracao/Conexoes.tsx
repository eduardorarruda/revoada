// Conexões de banco: a senha vai para o cofre do painel e nunca volta; a leitura da
// estrutura é feita por um agente, com a senha selada só para ele.
import { useCallback, useEffect, useState } from "react";
import { motion, useReducedMotion } from "motion/react";
import { Badge, Button, FormField } from "../../components";
import { Modal } from "../../components/Modal";
import { useToast } from "../../components/Toast";
import {
  apagarConexao,
  capturarEsquema,
  criarConexao,
  isAdmin,
  listarAgentes,
  listarConexoes,
  podeOperar,
  type Agente,
  type ConexaoBanco,
  type Motor,
} from "../../api";
import { mensagemErro } from "./util";

export function Conexoes() {
  const toast = useToast();
  const reduzir = useReducedMotion();
  const [conexoes, setConexoes] = useState<ConexaoBanco[] | null>(null);
  const [agentes, setAgentes] = useState<Agente[]>([]);
  const [nova, setNova] = useState(false);
  const [lendo, setLendo] = useState<string | null>(null);

  const carregar = useCallback(async () => {
    try {
      const [cs, ags] = await Promise.all([listarConexoes(), listarAgentes()]);
      setConexoes(cs);
      setAgentes(ags.filter((a) => !a.revogado && a.capacidades.includes("migracao.esquema")));
    } catch (e) {
      toast.error(mensagemErro(e));
    }
  }, [toast]);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  const capturar = async (c: ConexaoBanco, agenteId: string) => {
    setLendo(c.id);
    try {
      const r = await capturarEsquema(c.id, agenteId);
      toast.success(`Estrutura lida: ${r.tabelas} tabelas, ${c.motor} ${r.versao}, charset ${r.charset}.`);
      void carregar();
    } catch (e) {
      toast.error(mensagemErro(e));
    } finally {
      setLendo(null);
    }
  };

  const apagar = async (c: ConexaoBanco) => {
    if (!window.confirm(`Apagar a conexão "${c.nome}"? A senha guardada é destruída junto.`)) return;
    try {
      await apagarConexao(c.id);
      void carregar();
    } catch (e) {
      toast.error(mensagemErro(e));
    }
  };

  return (
    <section className="mm-sec">
      <div className="mm-sec__cabeca">
        <div>
          <h1>Conexões de banco</h1>
          <p>
            A senha fica cifrada no cofre do painel e nunca volta para a tela. Para ler a estrutura, o painel sela a senha
            para um agente específico, que lê só o schema — nenhum dado das tabelas sai do servidor.
          </p>
        </div>
        {isAdmin() && (
          <Button variant="primary" onClick={() => setNova(true)}>
            Nova conexão
          </Button>
        )}
      </div>

      {conexoes?.length === 0 && (
        <div className="mm-vazio">
          <p className="mm-vazio__titulo">Nenhuma conexão ainda</p>
          <p>Cadastre o banco de origem (ex.: o Firebird do ERP) e o de destino.</p>
        </div>
      )}

      <ul className="mm-lista">
        {conexoes?.map((c, i) => (
          <motion.li
            key={c.id}
            className="mm-cartao"
            initial={reduzir ? false : { opacity: 0, y: 10 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ delay: reduzir ? 0 : i * 0.04 }}
          >
            <div className="mm-cartao__topo">
              <span className={`mm-motor mm-motor--${c.motor}`}>{c.motor === "firebird" ? "Firebird" : "PostgreSQL"}</span>
              <strong>{c.nome}</strong>
              {c.versao_detectada ? <Badge state="ok">{c.versao_detectada.split(" ")[0]}</Badge> : <Badge state="neutral">não lido</Badge>}
            </div>
            <code className="mm-cartao__dsn">
              {c.usuario}@{c.endereco}/{c.banco}
            </code>
            <div className="mm-cartao__acoes">
              {podeOperar() &&
                (agentes.length === 0 ? (
                  <span className="mm-dica">Nenhum agente com “migracao.esquema” liberado.</span>
                ) : (
                  <SeletorAgente
                    agentes={agentes}
                    preferido={c.agente_preferido_id}
                    ocupado={lendo === c.id}
                    onEscolher={(id) => capturar(c, id)}
                  />
                ))}
              {isAdmin() && (
                <Button variant="ghost" onClick={() => apagar(c)}>
                  Apagar
                </Button>
              )}
            </div>
          </motion.li>
        ))}
      </ul>

      {nova && (
        <ModalConexao
          agentes={agentes}
          onClose={() => setNova(false)}
          onCriada={() => {
            setNova(false);
            void carregar();
          }}
        />
      )}
    </section>
  );
}

function SeletorAgente({
  agentes,
  preferido,
  ocupado,
  onEscolher,
}: {
  agentes: Agente[];
  preferido?: string;
  ocupado: boolean;
  onEscolher: (id: string) => void;
}) {
  const [id, setId] = useState(preferido && agentes.some((a) => a.id === preferido) ? preferido : agentes[0]?.id ?? "");
  return (
    <div className="mm-seletor">
      <select className="field" value={id} onChange={(e) => setId(e.target.value)} aria-label="Agente que vai ler o banco">
        {agentes.map((a) => (
          <option key={a.id} value={a.id} disabled={a.estado === "offline"}>
            {a.hostname || a.id} {a.estado === "offline" ? "(offline)" : ""}
          </option>
        ))}
      </select>
      <Button onClick={() => onEscolher(id)} disabled={ocupado || !id}>
        {ocupado ? "Lendo…" : "Ler estrutura"}
      </Button>
    </div>
  );
}

const PADRAO: Record<Motor, { porta: string; banco: string; usuario: string }> = {
  firebird: { porta: "3050", banco: "/dados/erp.fdb", usuario: "SYSDBA" },
  postgres: { porta: "5432", banco: "erp", usuario: "postgres" },
};

function ModalConexao({ agentes, onClose, onCriada }: { agentes: Agente[]; onClose: () => void; onCriada: () => void }) {
  const toast = useToast();
  const [motor, setMotor] = useState<Motor>("firebird");
  const [f, setF] = useState({ nome: "", endereco: "", banco: "", usuario: "", senha: "", charset: "", agente: "", diretorio: "", cripto: false });
  const [erroCampo, setErroCampo] = useState<{ campo: string; mensagem: string } | null>(null);
  const [busy, setBusy] = useState(false);
  const p = PADRAO[motor];
  const set = (k: keyof typeof f) => (e: React.ChangeEvent<HTMLInputElement | HTMLSelectElement>) => setF({ ...f, [k]: e.target.value });

  const salvar = async () => {
    setBusy(true);
    setErroCampo(null);
    try {
      const opcoes: Record<string, string> = {};
      if (f.charset) opcoes.charset = f.charset;
      if (motor === "firebird" && f.cripto) opcoes.wire_crypt = "true";
      if (motor === "firebird" && f.diretorio.trim()) opcoes.diretorio_backup = f.diretorio.trim();
      await criarConexao({
        nome: f.nome, motor, endereco: f.endereco, banco: f.banco, usuario: f.usuario, senha: f.senha, opcoes,
        agente_preferido_id: f.agente || undefined,
      });
      toast.success("Conexão criada. A senha foi para o cofre.");
      onCriada();
    } catch (e) {
      const m = mensagemErro(e);
      try {
        setErroCampo(JSON.parse(m));
      } catch {
        toast.error(m);
      }
    } finally {
      setBusy(false);
    }
  };

  const erroDe = (campo: string) => (erroCampo?.campo === campo ? erroCampo.mensagem : undefined);

  return (
    <Modal
      open
      onClose={onClose}
      title="Nova conexão de banco"
      footer={
        <div className="row">
          <Button onClick={onClose}>Cancelar</Button>
          <Button variant="primary" onClick={salvar} disabled={busy}>
            {busy ? "Salvando…" : "Salvar no cofre"}
          </Button>
        </div>
      }
    >
      <div className="mm-form">
        <div className="mm-motores" role="radiogroup" aria-label="Banco">
          {(["firebird", "postgres"] as Motor[]).map((m) => (
            <button key={m} type="button" role="radio" aria-checked={motor === m} className={`mm-motores__op${motor === m ? " is-ativo" : ""}`} onClick={() => setMotor(m)}>
              {m === "firebird" ? "Firebird (1.5 a 5)" : "PostgreSQL"}
            </button>
          ))}
        </div>
        <FormField label="Nome" hint={erroDe("nome")}>
          <input className="field" value={f.nome} onChange={set("nome")} placeholder="ERP do cliente X — produção" />
        </FormField>
        <FormField label="Servidor (host:porta)" hint={erroDe("endereco") ?? `Porta padrão ${p.porta}.`}>
          <input className="field" value={f.endereco} onChange={set("endereco")} placeholder={`10.0.0.5:${p.porta}`} />
        </FormField>
        <FormField label={motor === "firebird" ? "Caminho do arquivo .fdb no servidor" : "Nome do banco"} hint={erroDe("banco")}>
          <input className="field" value={f.banco} onChange={set("banco")} placeholder={p.banco} />
        </FormField>
        <div className="mm-form__linha">
          <FormField label="Usuário" hint={erroDe("usuario") ?? "De preferência só leitura na origem."}>
            <input className="field" value={f.usuario} onChange={set("usuario")} placeholder={p.usuario} autoComplete="off" />
          </FormField>
          <FormField label="Senha" hint={erroDe("senha")}>
            <input className="field" type="password" value={f.senha} onChange={set("senha")} autoComplete="new-password" />
          </FormField>
        </div>
        {motor === "firebird" && (
          <FormField label="Charset da conexão" hint="Bancos brasileiros antigos costumam ser WIN1252 ou NONE.">
            <select className="field" value={f.charset} onChange={set("charset")}>
              <option value="">UTF8 (padrão)</option>
              <option value="WIN1252">WIN1252</option>
              <option value="ISO8859_1">ISO8859_1</option>
              <option value="NONE">NONE</option>
            </select>
          </FormField>
        )}
        {motor === "firebird" && (
          <>
            <label className="mm-param mm-param--check">
              <input type="checkbox" checked={f.cripto} onChange={(e) => setF({ ...f, cripto: e.target.checked })} />
              <span>criptografia do protocolo (Firebird 3+ com usuário SRP — obrigatória no Firebird 5 padrão)</span>
            </label>
            <FormField
              label="Diretório compartilhado (só para upgrade)"
              hint={erroDe("opcoes.diretorio_backup") ?? "Pasta que o servidor antigo e o Firebird 5 enxergam: o .fbk passa por ela."}
            >
              <input className="field" value={f.diretorio} onChange={set("diretorio")} placeholder="/firebird/upgrade" autoComplete="off" />
            </FormField>
          </>
        )}
        {agentes.length > 0 && (
          <FormField label="Agente preferido para ler este banco">
            <select className="field" value={f.agente} onChange={set("agente")}>
              <option value="">escolher na hora</option>
              {agentes.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.hostname || a.id}
                </option>
              ))}
            </select>
          </FormField>
        )}
      </div>
    </Modal>
  );
}
