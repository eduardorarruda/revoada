import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { vi } from "vitest";
import { InstalarServidorModal } from "./InstalarServidorModal";
import { ToastProvider } from "../../components";

// O valor desta tela está em ESCOLHER CERTO antes de digitar qualquer coisa: o
// caminho por SSH só funciona em Linux com systemd, e antes disso a pessoa
// descobria o limite no erro, depois de digitar a senha do servidor. Os testes
// abaixo guardam justamente isso — que os caminhos aparecem, que cada um diz onde
// funciona, e que o aviso do SSH vem ANTES do formulário.
//
// Os cartões são buscados pelo NOME ACESSÍVEL exato (string, não regex): o nome de
// cada botão é só o título, via aria-labelledby. Regex aqui é armadilha — foi assim
// que "Baixar o instalador" casou também com "Baixar o instalador UNIVERSAL", o que
// aliás motivou renomear o primeiro para "Configurar um servidor específico".

vi.mock("../../api", async () => {
  const real = await vi.importActual<typeof import("../../api")>("../../api");
  return {
    ...real,
    listAgents: vi.fn().mockResolvedValue({ agents: [] }),
    listEnrollTokens: vi.fn().mockResolvedValue({ tokens: [] }),
    getAgentResourceLimits: vi.fn().mockResolvedValue(null),
  };
});

// useHostNames/useHostContainers fazem polling da API; a tela de gestão só precisa
// deles para rotular hostnames, e aqui não há host nenhum.
vi.mock("../../hooks/useHostNames", () => ({
  useHostNames: () => ({ hostLabel: (h: string) => h, names: {} }),
}));
vi.mock("../../hooks/useHostContainers", () => ({
  useHostContainers: () => ({ hosts: [] }),
}));

// campoNome: o rótulo aparece também no botão de ajuda ao lado do campo, então
// buscamos pelo placeholder, que é único.
function campoNome() {
  return screen.getByPlaceholderText("ex.: app-prod-01");
}

function abrir() {
  return render(
    <ToastProvider>
      <InstalarServidorModal open onClose={() => {}} onProvisioned={() => {}} />
    </ToastProvider>,
  );
}

describe("Adicionar servidor — escolha do caminho", () => {
  it("oferece os três caminhos, cada um dizendo onde funciona", async () => {
    abrir();
    // A ordem importa: o universal vem primeiro (é o caso do dia a dia de quem
    // opera uma frota) e a gestão por último.
    const cartoes = (await screen.findAllByRole("button")).filter((b) => b.classList.contains("opcao"));
    expect(cartoes.map((b) => b.getAttribute("aria-labelledby"))).toEqual([
      "opcao-frota-titulo",
      "opcao-arquivo-titulo",
      "opcao-ssh-titulo",
      "opcao-gerenciar-titulo",
    ]);

    // O limite do SSH é a informação que mais faltava: tem de estar visível já na
    // escolha, não escondida no formulário.
    const ssh = screen.getByRole("button", { name: "Instalar por SSH, a partir do painel" });
    expect(ssh).toHaveTextContent("Só Linux com systemd");
    expect(ssh).toHaveTextContent(/Windows e macOS não entram por aqui/);
  });

  it("os dois caminhos de arquivo anunciam que servem para os três sistemas", async () => {
    abrir();
    // Os dois nomes precisam se distinguir pela PRIMEIRA palavra: dois cartões
    // começando com "Baixar o instalador" obrigavam a ler o resto para escolher.
    expect(await screen.findByRole("button", { name: "Configurar um servidor específico" })).toHaveTextContent(
      "Linux · Windows · macOS",
    );
    const universal = screen.getByRole("button", { name: "Baixar o instalador UNIVERSAL" });
    expect(universal).toHaveTextContent("Linux · Windows · macOS");
    // O cartão do universal precisa explicar o mecanismo: é o que faz um arquivo só
    // servir para a frota, e é o que justifica poder revogar sem derrubar ninguém.
    expect(universal).toHaveTextContent(/cada máquina pede ao painel a chave DELA/);
    expect(universal).toHaveTextContent(/Vale até você revogar o lote/);
  });

  it("escolher um caminho leva ao passo dele, com volta", async () => {
    abrir();
    fireEvent.click(await screen.findByRole("button", { name: "Configurar um servidor específico" }));

    expect(campoNome()).toBeInTheDocument();
    // Sistema operacional é escolha explícita, não adivinhação pelo navegador.
    expect(screen.getByRole("radio", { name: /Linux/ })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /Windows/ })).toBeInTheDocument();
    expect(screen.getByRole("radio", { name: /macOS/ })).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: /Voltar/ }));
    expect(screen.getByRole("button", { name: "Instalar por SSH, a partir do painel" })).toBeInTheDocument();
  });

  it("o aviso de compatibilidade do SSH aparece antes de pedir a senha", async () => {
    abrir();
    fireEvent.click(await screen.findByRole("button", { name: "Instalar por SSH, a partir do painel" }));

    const aviso = screen.getByText(/Linux com systemd/);
    const senha = screen.getByPlaceholderText("senha do usuário no servidor");
    // compareDocumentPosition: o aviso precede o campo no documento.
    expect(aviso.compareDocumentPosition(senha) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
  });

  it("não deixa gerar instalador sem nomear o servidor", async () => {
    abrir();
    fireEvent.click(await screen.findByRole("button", { name: "Configurar um servidor específico" }));

    const gerar = screen.getByRole("button", { name: /Gerar e baixar/ });
    expect(gerar).toBeDisabled();
    fireEvent.change(campoNome(), { target: { value: "app-prod-01" } });
    await waitFor(() => expect(gerar).toBeEnabled());
  });

  it("a gestão de chaves fica atrás de uma escolha, não competindo com instalar", async () => {
    abrir();
    const gerir = await screen.findByRole("button", { name: "Chaves de acesso" });
    fireEvent.click(gerir);
    expect(await screen.findByText("Chaves de ingestão")).toBeInTheDocument();
    expect(screen.getByText("Instaladores universais")).toBeInTheDocument();

    // O "Voltar" da gestão precisa levar de volta à escolha. Já ficou preso aqui
    // quando o destino era decidido pelo alvo selecionado, e não pelo passo atual.
    fireEvent.click(screen.getByRole("button", { name: /Voltar/ }));
    expect(screen.getByRole("button", { name: "Configurar um servidor específico" })).toBeInTheDocument();
  });
});
