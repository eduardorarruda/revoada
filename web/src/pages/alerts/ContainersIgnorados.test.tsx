import { fireEvent, render, screen } from "@testing-library/react";
import type { IgnoredContainer } from "../../api";
import { ContainersIgnorados, podeIgnorar } from "./ContainersIgnorados";

const item = (container: string, host = "srv-reserva"): IgnoredContainer => ({
  host,
  container,
  created_by: "Ana",
  created_at: "2026-09-28T12:00:00Z",
});
const hostLabel = (h: string) => (h === "srv-reserva" ? "Servidor Reserva Nuvem" : h);

describe("containers ignorados", () => {
  it("mostra cada container com o servidor pelo nome amigável e quem ignorou", () => {
    render(<ContainersIgnorados lista={[item("app-web")]} hostLabel={hostLabel} onVoltar={() => {}} />);
    expect(screen.getByText("app-web")).toBeInTheDocument();
    expect(screen.getByText("Servidor Reserva Nuvem")).toBeInTheDocument();
    expect(screen.getByText(/Ana/)).toBeInTheDocument();
  });

  it("'Voltar a vigiar' devolve o container certo", () => {
    const onVoltar = vi.fn();
    render(
      <ContainersIgnorados
        lista={[item("app-web"), item("standby-pg")]}
        hostLabel={hostLabel}
        onVoltar={onVoltar}
      />,
    );
    fireEvent.click(screen.getByRole("button", { name: /Voltar a vigiar standby-pg/ }));
    expect(onVoltar).toHaveBeenCalledWith(item("standby-pg"));
  });

  it("lista vazia explica como ignorar, em vez de sumir", () => {
    render(<ContainersIgnorados lista={[]} hostLabel={hostLabel} onVoltar={() => {}} />);
    expect(screen.getByText(/Nenhum container ignorado/)).toBeInTheDocument();
  });
});

describe("podeIgnorar", () => {
  it("só alerta de um container (com servidor) pode ser ignorado", () => {
    expect(podeIgnorar({ host: "srv-reserva", container: "app-web" })).toBe(true);
    expect(podeIgnorar({ host: "srv-reserva" })).toBe(false);
    expect(podeIgnorar({ container: "app-web" })).toBe(false);
    expect(podeIgnorar({})).toBe(false);
  });
});
