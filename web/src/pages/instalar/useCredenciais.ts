// Estado compartilhado do fluxo "Adicionar servidor": chaves de ingestão, lotes do
// instalador universal, limites de recurso e o download do instalador.
//
// Fica num hook, e não espalhado pelos passos, porque quase toda ação mexe em mais
// de uma tela: gerar um instalador cria uma chave (que a aba de gestão precisa
// mostrar), revogar um lote muda o que a lista exibe, e o painel "arquivo baixado"
// é o mesmo em todos os caminhos.
import { useCallback, useEffect, useState } from "react";
import { useToast } from "../../components";
import {
  createAgent,
  createEnrollToken,
  deleteAgent,
  deleteEnrollToken,
  downloadAgentInstaller,
  getAgentResourceLimits,
  listAgents,
  listEnrollTokens,
  revealAgentKey,
  setAgentResourceLimits,
  setAgentRevoked,
  setEnrollTokenRevoked,
  type AgentKey,
  type AgentOS,
  type AgentResourceLimits,
  type EnrollToken,
} from "../../api";
import { errMsg, type AlvoInstalador } from "./comum";

// Baixado descreve o último arquivo gerado: a tela precisa dizer o que fazer com
// ele, e a instrução muda conforme o sistema operacional.
export interface Baixado {
  arquivo: string;
  os: AgentOS;
  nome: string;
}

export function useCredenciais(open: boolean) {
  const toast = useToast();
  const [chaves, setChaves] = useState<AgentKey[]>([]);
  const [tokens, setTokens] = useState<EnrollToken[]>([]);
  const [carregando, setCarregando] = useState(false);
  const [limites, setLimites] = useState<AgentResourceLimits | null>(null);
  const [baixando, setBaixando] = useState(false);
  const [baixado, setBaixado] = useState<Baixado | null>(null);
  // Chave recém-criada sem instalador (caminho "só a chave", para quem prefere o
  // comando de uma linha).
  const [chaveCriada, setChaveCriada] = useState<{ serverkey: string; hostname: string } | null>(null);

  const recarregar = useCallback(async () => {
    setCarregando(true);
    try {
      const [a, t] = await Promise.all([listAgents(), listEnrollTokens()]);
      setChaves(a.agents);
      setTokens(t.tokens);
    } catch (e) {
      toast.error(`Erro ao carregar as chaves: ${errMsg(e)}`);
    } finally {
      setCarregando(false);
    }
  }, [toast]);

  useEffect(() => {
    if (!open) return;
    setBaixado(null);
    setChaveCriada(null);
    void recarregar();
    getAgentResourceLimits()
      .then(setLimites)
      .catch(() => setLimites(null)); // indisponível: o comando usa os defaults do install.sh
  }, [open, recarregar]);

  // baixar centraliza os três jeitos de pedir um instalador (host novo, chave
  // existente, lote universal) para que o tratamento de erro e o aviso de sucesso
  // sejam sempre os mesmos.
  const baixar = useCallback(
    async (os: AgentOS, req: { hostname?: string; agent_id?: string; enroll_token?: string }, nome: string) => {
      setBaixando(true);
      try {
        const arquivo = await downloadAgentInstaller({ os, ...req });
        setBaixado({ arquivo, os, nome });
        setChaveCriada(null);
        toast.success(`${arquivo} baixado.`);
        return true;
      } catch (e) {
        toast.error(`Erro ao gerar o instalador: ${errMsg(e)}`);
        return false;
      } finally {
        setBaixando(false);
      }
    },
    [toast],
  );

  // gerarInstalador: servidor novo — cria a chave e baixa o arquivo num passo só.
  const gerarInstalador = useCallback(
    async (os: AgentOS, nome: string) => {
      const ok = await baixar(os, { hostname: nome }, nome);
      if (ok) void recarregar();
      return ok;
    },
    [baixar, recarregar],
  );

  // gerarUniversal: cria o lote e já baixa o instalador que serve a frota inteira.
  const gerarUniversal = useCallback(
    async (os: AgentOS, rotulo: string) => {
      try {
        const { token } = await createEnrollToken(rotulo);
        const ok = await baixar(os, { enroll_token: token }, rotulo);
        void recarregar();
        return ok;
      } catch (e) {
        toast.error(`Erro ao criar o instalador universal: ${errMsg(e)}`);
        return false;
      }
    },
    [baixar, recarregar, toast],
  );

  // baixarDeExistente: reaproveita uma credencial que já existe — a chave de um
  // servidor (reinstalar sem deixar chave órfã) ou um lote (outro sistema).
  //
  // Para a chave manda o `agent_id` (id público), não a serverkey: baixar um
  // instalador deixou de exigir que a chave em claro passe pelo navegador. Quem
  // resolve id → chave é o backend, que já é dono dela.
  const baixarDeExistente = useCallback(
    async (alvo: AlvoInstalador, os: AgentOS) => {
      const req = alvo.tipo === "chave" ? { agent_id: alvo.id } : { enroll_token: alvo.id };
      return baixar(os, req, alvo.nome);
    },
    [baixar],
  );

  const criarSoAChave = useCallback(
    async (hostname: string) => {
      try {
        const r = await createAgent(hostname);
        setChaveCriada(r);
        setBaixado(null);
        // A lista NÃO mostra mais a chave em claro: prometer que ela "também a
        // mostra" mandaria a pessoa fechar isto aqui e procurar um texto que não
        // existe mais. Lá ela é mascarada e só sai por "Revelar", que é auditado.
        toast.success("Chave gerada. Copie agora, na lista ela aparece mascarada.");
        void recarregar();
      } catch (e) {
        toast.error(`Erro ao gerar a chave: ${errMsg(e)}`);
      }
    },
    [recarregar, toast],
  );

  // Revogar/excluir usam o `id` PÚBLICO da chave. A listagem não traz mais a
  // serverkey em claro, e não precisa: quem sabe traduzir id → chave é o backend.
  const revogarChave = useCallback(
    async (a: AgentKey) => {
      try {
        await setAgentRevoked(a.id, !a.revoked);
        toast.success(a.revoked ? "Chave reativada." : "Chave revogada.");
        void recarregar();
      } catch (e) {
        toast.error(`Erro: ${errMsg(e)}`);
      }
    },
    [recarregar, toast],
  );

  const excluirChave = useCallback(
    async (a: AgentKey) => {
      try {
        await deleteAgent(a.id);
        toast.success("Chave excluída.");
        void recarregar();
      } catch (e) {
        toast.error(`Erro: ${errMsg(e)}`);
      }
    },
    [recarregar, toast],
  );

  // revelarChave busca o TEXTO de uma chave, sob demanda. Só é chamada no clique de
  // quem pediu para ver: a revelação fica na trilha de auditoria, então dispará-la
  // ao carregar a lista encheria a trilha de ruído e recriaria — em N requisições —
  // exatamente o vazamento em massa que a máscara veio impedir.
  const revelarChave = useCallback(
    async (a: AgentKey): Promise<string | null> => {
      try {
        const r = await revealAgentKey(a.id);
        return r.serverkey;
      } catch (e) {
        toast.error(`Não foi possível revelar a chave: ${errMsg(e)}`);
        return null;
      }
    },
    [toast],
  );

  const revogarToken = useCallback(
    async (t: EnrollToken) => {
      try {
        await setEnrollTokenRevoked(t.token, !t.revoked);
        toast.success(
          t.revoked
            ? "Instalador reativado."
            : "Instalador revogado. Os servidores que já entraram continuam reportando.",
        );
        void recarregar();
      } catch (e) {
        toast.error(`Erro: ${errMsg(e)}`);
      }
    },
    [recarregar, toast],
  );

  const excluirToken = useCallback(
    async (t: EnrollToken) => {
      try {
        await deleteEnrollToken(t.token);
        toast.success("Instalador universal excluído.");
        void recarregar();
      } catch (e) {
        toast.error(`Erro: ${errMsg(e)}`);
      }
    },
    [recarregar, toast],
  );

  const salvarLimites = useCallback(
    async (l: AgentResourceLimits) => {
      try {
        const saved = await setAgentResourceLimits(l);
        setLimites(saved); // reflete o saneamento do backend (valores <=0 caem no default)
        toast.success("Limites salvos. Valem para novas instalações e reprovisionamentos.");
      } catch (e) {
        toast.error(`Erro ao salvar os limites: ${errMsg(e)}`);
      }
    },
    [toast],
  );

  return {
    chaves,
    tokens,
    carregando,
    limites,
    setLimites,
    baixando,
    baixado,
    setBaixado,
    chaveCriada,
    recarregar,
    gerarInstalador,
    gerarUniversal,
    baixarDeExistente,
    criarSoAChave,
    revogarChave,
    excluirChave,
    revelarChave,
    revogarToken,
    excluirToken,
    salvarLimites,
  };
}
