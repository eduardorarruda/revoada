import { render, screen, waitFor } from "@testing-library/react";
import type { NotificationLogEntry } from "../../api";

// O histórico é a tela onde o painel dizia "enviado" para mensagem que o WhatsApp
// tinha recusado — e, depois, para mensagem que ele aceitou e nunca entregou. Estes
// testes trancam o vocabulário: só "enviado" afirma envio; o resto se explica.
const notificationLog = vi.fn();
vi.mock("../../api", () => ({ notificationLog: () => notificationLog() }));

const { HistoryModal } = await import("./HistoryModal");

const entry = (id: number, status: string, detail = "", destino = "5511999999999"): NotificationLogEntry => ({
  id,
  channel_type: "whatsapp",
  channel_name: "WhatsApp Ops",
  destination: destino,
  route_name: "",
  subject: `assunto ${id}`,
  alert_count: 1,
  status,
  detail,
  sent_at: "2026-07-29T11:50:00Z",
});

beforeEach(() => {
  notificationLog.mockReset();
  notificationLog.mockResolvedValue({
    log: [entry(1, "sent"), entry(2, "error", "o WhatsApp recusou a mensagem")],
  });
});

describe("histórico de envios", () => {
  it("mostra a recusa com o motivo, em vez de dar por enviada", async () => {
    render(<HistoryModal open onClose={() => {}} />);
    await waitFor(() => expect(screen.getByText("enviado")).toBeInTheDocument());

    // O ponto do conserto: uma recusa do provedor tem que aparecer como falha,
    // com o motivo legível ao lado — nunca somer atrás de um "enviado" verde.
    expect(screen.getByText("falhou")).toBeInTheDocument();
    expect(screen.getByText("o WhatsApp recusou a mensagem")).toBeInTheDocument();
  });

  it("distingue 'aceito sem confirmação' de 'enviado'", async () => {
    // O caso do WhatsApp sem LID: o provedor respondeu 200 e a mensagem não chegou.
    // Continuar chamando isso de "enviado" é a mentira que este estado desfaz.
    notificationLog.mockResolvedValue({
      log: [entry(3, "unverified", "o wuzapi aceitou a mensagem, mas sem LID não dá para confirmar que ela chegou")],
    });
    render(<HistoryModal open onClose={() => {}} />);

    await waitFor(() => expect(screen.getByText("sem confirmação")).toBeInTheDocument());
    expect(screen.queryByText("enviado")).not.toBeInTheDocument();
    expect(screen.getByText(/sem LID não dá para confirmar/)).toBeInTheDocument();
  });

  it("mostra um status desconhecido como falha, em vez de escondê-lo", async () => {
    notificationLog.mockResolvedValue({ log: [entry(9, "bizarro")] });
    render(<HistoryModal open onClose={() => {}} />);
    await waitFor(() => expect(screen.getByText("bizarro")).toBeInTheDocument());
  });

  it("diz PARA QUEM foi — sem isso, dois canais de WhatsApp são indistinguíveis", async () => {
    render(<HistoryModal open onClose={() => {}} />);
    await waitFor(() => expect(screen.getAllByText("WhatsApp Ops").length).toBe(2));
    expect(screen.getAllByText("→ 5511999999999").length).toBe(2);
  });

  it("envio antigo, gravado antes da coluna existir, mostra travessão — não o destino de hoje", async () => {
    notificationLog.mockResolvedValue({
      log: [{ ...entry(4, "sent"), channel_name: "", destination: "" }],
    });
    render(<HistoryModal open onClose={() => {}} />);
    await waitFor(() => expect(screen.getByText("→ —")).toBeInTheDocument());
    expect(screen.queryByText("WhatsApp Ops")).not.toBeInTheDocument();
  });

  it("não busca nada enquanto está fechado", () => {
    render(<HistoryModal open={false} onClose={() => {}} />);
    expect(notificationLog).not.toHaveBeenCalled();
  });
});
