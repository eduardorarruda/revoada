import { render, screen, fireEvent, within } from "@testing-library/react";
import { DataTable, type Column } from "./DataTable";

interface Row {
  id: number;
  name: string;
  cpu: number;
}

const rows: Row[] = [
  { id: 1, name: "alfa", cpu: 30 },
  { id: 2, name: "bravo", cpu: 10 },
  { id: 3, name: "charlie", cpu: 20 },
];

const columns: Column<Row>[] = [
  { key: "name", label: "Nome", sortable: true },
  { key: "cpu", label: "CPU", align: "right", sortable: true, sortValue: (r) => r.cpu },
];

function bodyNames(): string[] {
  const tbody = document.querySelector("tbody");
  if (!tbody) return [];
  return within(tbody as HTMLElement)
    .getAllByRole("row")
    .map((tr) => tr.querySelector("td")!.textContent!.trim());
}

describe("DataTable v2", () => {
  it("ordena por coluna ao clicar no cabeçalho (asc → desc → sem)", () => {
    render(<DataTable columns={columns} rows={rows} keyFn={(r) => r.id} empty={<i>vazio</i>} />);
    fireEvent.click(screen.getByText("CPU"));
    expect(bodyNames()).toEqual(["bravo", "charlie", "alfa"]); // cpu 10,20,30
    fireEvent.click(screen.getByText("CPU"));
    expect(bodyNames()).toEqual(["alfa", "charlie", "bravo"]); // desc
  });

  it("filtra pela busca", () => {
    render(
      <DataTable
        columns={columns}
        rows={rows}
        keyFn={(r) => r.id}
        empty={<i>vazio</i>}
        searchable
        searchText={(r) => r.name}
      />,
    );
    fireEvent.change(screen.getByLabelText("Buscar na tabela"), { target: { value: "brav" } });
    expect(bodyNames()).toEqual(["bravo"]);
  });

  it("pagina os resultados", () => {
    render(<DataTable columns={columns} rows={rows} keyFn={(r) => r.id} empty={<i>vazio</i>} pageSize={2} />);
    expect(bodyNames().length).toBe(2);
    fireEvent.click(screen.getByText(/Próxima/));
    expect(bodyNames().length).toBe(1);
  });

  it("mostra estado de erro e de carregando com prioridade", () => {
    const { rerender } = render(
      <DataTable columns={columns} rows={rows} keyFn={(r) => r.id} empty={<i>vazio</i>} error="falhou" />,
    );
    expect(screen.getByText("falhou")).toBeInTheDocument();
    rerender(<DataTable columns={columns} rows={[]} keyFn={(r) => r.id} empty={<i>vazio</i>} loading />);
    expect(document.querySelector(".dt-skel-row")).not.toBeNull();
  });

  it("lista vazia mostra o empty", () => {
    render(<DataTable columns={columns} rows={[]} keyFn={(r) => r.id} empty={<i>nada aqui</i>} />);
    expect(screen.getByText("nada aqui")).toBeInTheDocument();
  });

  // Regressão de /websites: 9 colunas com largura fixa somavam 1044px num
  // container de 1108px, e as colunas SEM largura ficavam com ~30px — o nome e a
  // URL do site quebravam uma letra por linha (linha de 725px de altura, medida no
  // navegador). O piso faz o container rolar de lado em vez de espremer.
  it("modo fit aplica o piso de largura (--dt-min) só quando pedido", () => {
    const { rerender } = render(
      <DataTable columns={columns} rows={rows} keyFn={(r) => r.id} empty={<i>vazio</i>} fit minWidth="1100px" />,
    );
    const table = document.querySelector("table.dtable--fit") as HTMLElement;
    expect(table.style.getPropertyValue("--dt-min")).toBe("1100px");

    // sem `fit` não existe colgroup nem piso — o piso só faz sentido com larguras fixas.
    rerender(<DataTable columns={columns} rows={rows} keyFn={(r) => r.id} empty={<i>vazio</i>} minWidth="1100px" />);
    const auto = document.querySelector("table.dtable") as HTMLElement;
    expect(auto.classList.contains("dtable--fit")).toBe(false);
    expect(auto.style.getPropertyValue("--dt-min")).toBe("");
  });

  it("headerExtra vai só no cabeçalho — o data-label do mobile continua texto", () => {
    const comAjuda: Column<Row>[] = [
      { key: "name", label: "Nome" },
      { key: "cpu", label: "CPU", headerExtra: <span>ajuda-da-coluna</span> },
    ];
    render(<DataTable columns={comAjuda} rows={rows} keyFn={(r) => r.id} empty={<i>vazio</i>} />);
    const th = screen.getAllByRole("columnheader")[1];
    expect(within(th).getByText("ajuda-da-coluna")).toBeInTheDocument();
    const td = document.querySelectorAll("tbody tr")[0].querySelectorAll("td")[1];
    expect(td.getAttribute("data-label")).toBe("CPU");
    expect(td.textContent).not.toContain("ajuda-da-coluna");
  });

  it("headerExtra não dispara a ordenação da coluna", () => {
    const ordenavel: Column<Row>[] = [
      { key: "name", label: "Nome" },
      { key: "cpu", label: "CPU", sortable: true, sortValue: (r) => r.cpu, headerExtra: <span>ⓘ</span> },
    ];
    render(<DataTable columns={ordenavel} rows={rows} keyFn={(r) => r.id} empty={<i>vazio</i>} />);
    const antes = bodyNames();
    fireEvent.click(screen.getByText("ⓘ"));
    expect(bodyNames()).toEqual(antes);
    fireEvent.click(screen.getByText("CPU"));
    expect(bodyNames()).toEqual(["bravo", "charlie", "alfa"]);
  });
});
