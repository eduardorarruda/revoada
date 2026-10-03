// Notificações (UX.8): para onde os alertas vão. A tela ficou reduzida a "Canais"
// (inclui a integração WhatsApp) — Rotas/Plantão/Escalonamento/Histórico foram removidos.
import { useState, type ReactNode } from "react";
import { ReceiptText } from "lucide-react";
import { Button, HelpPanel, PageHeader } from "../components";
import { help } from "../help";
import { Channels } from "./notify/Channels";
import { HistoryModal } from "./notify/HistoryModal";

// Monta as seções do HelpPanel a partir de help.pages.notify (padrão das telas).
function notifyHelpSections(): { heading: string; body: ReactNode }[] {
  const p = help.pages.notify;
  const sections: { heading: string; body: ReactNode }[] = [
    { heading: "O que é esta tela", body: p.what },
    {
      heading: "Como usar",
      body: (
        <ol className="help-section__how">
          {p.how.map((h, i) => (
            <li key={i}>{h}</li>
          ))}
        </ol>
      ),
    },
  ];
  if (p.faq) {
    sections.push({
      heading: "Perguntas frequentes",
      body: (
        <div className="help-faq">
          {p.faq.map((f, i) => (
            <div key={i}>
              <p className="help-faq__q">{f.q}</p>
              <p className="help-faq__a">{f.a}</p>
            </div>
          ))}
        </div>
      ),
    });
  }
  return sections;
}

export function Notify() {
  const [helpOpen, setHelpOpen] = useState(false);
  const [historyOpen, setHistoryOpen] = useState(false);

  return (
    <div className="page">
      <PageHeader
        title="Canais de alerta"
        subtitle="Os canais para onde os alertas são enviados"
        actions={
          <Button onClick={() => setHistoryOpen(true)}>
            <ReceiptText size={16} strokeWidth={1.75} /> Histórico de envios
          </Button>
        }
        onHelp={() => setHelpOpen(true)}
      />

      <Channels />

      <HistoryModal open={historyOpen} onClose={() => setHistoryOpen(false)} />
      <HelpPanel open={helpOpen} onClose={() => setHelpOpen(false)} title={help.pages.notify.title} sections={notifyHelpSections()} />
    </div>
  );
}
