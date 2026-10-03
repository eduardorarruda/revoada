// Projetos de migração: origem, destino e as versões do mapeamento.
import { useCallback, useEffect, useState } from "react";
import { motion, useReducedMotion } from "motion/react";
import { ArrowRight } from "lucide-react";
import { Badge, Button, FormField } from "../../components";
import { Modal } from "../../components/Modal";
import { useToast } from "../../components/Toast";
import { criarProjeto, listarConexoes, listarProjetos, podeOperar, type ConexaoBanco, type ProjetoMigracao } from "../../api";
import { mensagemErro } from "./util";
import { Garantias } from "./Garantias";

export function Projetos() {
  const toast = useToast();
  const reduzir = useReducedMotion();
  const [projetos, setProjetos] = useState<ProjetoMigracao[] | null>(null);
  const [conexoes, setConexoes] = useState<ConexaoBanco[]>([]);
  const [novo, setNovo] = useState(false);

  const carregar = useCallback(async () => {
    try {
      const [ps, cs] = await Promise.all([listarProjetos(), listarConexoes()]);
      setProjetos(ps);
      setConexoes(cs);
    } catch (e) {
      toast.error(mensagemErro(e));
    }
  }, [toast]);

  useEffect(() => {
    void carregar();
  }, [carregar]);

  const nome = (id?: string) => conexoes.find((c) => c.id === id)?.nome ?? "—";

  return (
    <section className="mm-sec">
      <div className="mm-sec__cabeca">
        <div>
          <h1>Projetos de migração</h1>
          <p>
            Um projeto liga um banco de origem a um destino. O mapeamento (tabela → tabela, coluna → coluna) é salvo em
            versões; só uma versão sem erros pode ser aprovada para simular e executar.
          </p>
        </div>
        {podeOperar() && (
          <Button variant="primary" onClick={() => setNovo(true)} disabled={conexoes.length === 0}>
            Novo projeto
          </Button>
        )}
      </div>
      <Garantias />
      {conexoes.length === 0 && projetos !== null && (
        <div className="mm-vazio">
          <p className="mm-vazio__titulo">Comece pelas conexões</p>
          <p>
            Cadastre os bancos em <a href="#/migracao/conexoes">Conexões</a> e leia a estrutura deles com um agente.
          </p>
        </div>
      )}
      <ul className="mm-lista">
        {projetos?.map((p, i) => (
          <motion.li
            key={p.id}
            className="mm-cartao mm-cartao--link"
            initial={reduzir ? false : { opacity: 0, y: 10 }}
            animate={{ opacity: 1, y: 0 }}
            transition={{ delay: reduzir ? 0 : i * 0.04 }}
          >
            <a href={`#/migracao/projetos/${encodeURIComponent(p.id)}`}>
              <div className="mm-cartao__topo">
                <strong>{p.nome}</strong>
                <Badge state={p.estado === "aprovado" ? "ok" : "neutral"}>{p.estado}</Badge>
              </div>
              <div className="mm-fluxo">
                <span>{nome(p.origem_id)}</span>
                <ArrowRight size={16} aria-hidden={true} />
                <span>{p.tipo === "upgrade_versao" ? "Firebird 5 (upgrade)" : nome(p.destino_id)}</span>
              </div>
            </a>
          </motion.li>
        ))}
      </ul>
      {novo && (
        <ModalProjeto
          conexoes={conexoes}
          onClose={() => setNovo(false)}
          onCriado={(id) => {
            window.location.hash = `/migracao/projetos/${encodeURIComponent(id)}`;
          }}
        />
      )}
    </section>
  );
}

function ModalProjeto({ conexoes, onClose, onCriado }: { conexoes: ConexaoBanco[]; onClose: () => void; onCriado: (id: string) => void }) {
  const toast = useToast();
  const [nome, setNome] = useState("");
  const [tipo, setTipo] = useState<"troca_de_banco" | "upgrade_versao">("troca_de_banco");
  const [origem, setOrigem] = useState(conexoes[0]?.id ?? "");
  const [destino, setDestino] = useState(conexoes[1]?.id ?? "");
  const [busy, setBusy] = useState(false);

  const criar = async () => {
    setBusy(true);
    try {
      const p = await criarProjeto({ nome, tipo, origem_id: origem, destino_id: destino || undefined });
      onCriado(p.id);
    } catch (e) {
      toast.error(mensagemErro(e));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      open
      onClose={onClose}
      title="Novo projeto de migração"
      footer={
        <div className="row">
          <Button onClick={onClose}>Cancelar</Button>
          <Button variant="primary" onClick={criar} disabled={busy || !nome || !origem}>
            Criar
          </Button>
        </div>
      }
    >
      <div className="mm-form">
        <FormField label="Nome do projeto">
          <input className="field" value={nome} onChange={(e) => setNome(e.target.value)} placeholder="ERP cliente X → PostgreSQL" />
        </FormField>
        <div className="mm-motores" role="radiogroup" aria-label="Tipo">
          <button type="button" role="radio" aria-checked={tipo === "troca_de_banco"} className={`mm-motores__op${tipo === "troca_de_banco" ? " is-ativo" : ""}`} onClick={() => {
              setTipo("troca_de_banco");
              setDestino(conexoes.find((c) => c.id !== origem)?.id ?? "");
            }}>
            Trocar de banco
          </button>
          <button type="button" role="radio" aria-checked={tipo === "upgrade_versao"} className={`mm-motores__op${tipo === "upgrade_versao" ? " is-ativo" : ""}`} onClick={() => {
              setTipo("upgrade_versao");
              setDestino("");
            }}>
            Upgrade de versão (Firebird → 5)
          </button>
        </div>
        <FormField label="Origem">
          <select className="field" value={origem} onChange={(e) => setOrigem(e.target.value)}>
            {conexoes.map((c) => (
              <option key={c.id} value={c.id}>
                {c.nome} ({c.motor})
              </option>
            ))}
          </select>
        </FormField>
        {tipo === "upgrade_versao" && (
          <FormField label="Servidor Firebird 5 (opcional agora; obrigatório para o ensaio e o upgrade)" hint="O “banco” desta conexão é o caminho do arquivo NOVO.">
            <select className="field" value={destino} onChange={(e) => setDestino(e.target.value)}>
              <option value="">— definir depois —</option>
              {conexoes
                .filter((c) => c.id !== origem && c.motor === "firebird")
                .map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.nome}
                  </option>
                ))}
            </select>
          </FormField>
        )}
        {tipo === "troca_de_banco" && (
          <FormField label="Destino">
            <select className="field" value={destino} onChange={(e) => setDestino(e.target.value)}>
              {conexoes
                .filter((c) => c.id !== origem)
                .map((c) => (
                  <option key={c.id} value={c.id}>
                    {c.nome} ({c.motor})
                  </option>
                ))}
            </select>
          </FormField>
        )}
      </div>
    </Modal>
  );
}
