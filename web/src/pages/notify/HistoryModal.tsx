// Modal de Histórico de envios: auditoria das notificações disparadas (canal,
// assunto, quantos alertas, status). Deixou de ser uma aba — agora abre por um botão
// na tela de Notificações. Só busca os dados enquanto está aberto (poll a cada 15s).
import { useCallback, useState } from "react";
import { ReceiptText } from "lucide-react";
import { Badge, DataTable, EmptyState, Modal } from "../../components";
import { usePolling } from "../../hooks/usePolling";
import { notificationLog, type NotificationLogEntry } from "../../api";
import { MONO, MUTED, TableOrSkeleton, fmtWhen } from "./shared";

// Vocabulário do status — três estados, porque a verdade tem três.
//
// "enviado" é afirmação: o provedor aceitou pelo endereçamento que sabidamente
// chega. "sem confirmação" é o meio-termo honesto: aceitou e ninguém confirma a
// entrega — no WhatsApp, é a mensagem para quem ainda não tem LID, que o provedor
// aceita e o aparelho nunca recebe. "falhou" é recusa. Empurrar o do meio para
// dentro de "enviado" foi o que escondeu 165 alertas perdidos.
const STATUS: Record<string, { label: string; state: "ok" | "warn" | "crit"; hint: string }> = {
  sent: { label: "enviado", state: "ok", hint: "" },
  unverified: { label: "sem confirmação", state: "warn", hint: "o provedor aceitou, mas ninguém confirma que chegou" },
  error: { label: "falhou", state: "crit", hint: "" },
};

export function HistoryModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const [log, setLog] = useState<NotificationLogEntry[] | null>(null);

  // Busca ao abrir e a cada 15 s enquanto aberto; para de sondar ao fechar E com a
  // aba em segundo plano. Era um `setInterval` cru: um modal deixado aberto numa aba
  // esquecida mantinha 5.760 requisições/dia contra um histórico que ninguém estava
  // lendo. `usePolling` já é o lugar único dessa regra de visibilidade — a guarda de
  // `open` fica dentro da função porque hooks não podem ser condicionais.
  const reload = useCallback(() => {
    if (!open) return;
    notificationLog().then((r) => setLog(r.log)).catch(() => setLog([]));
  }, [open]);
  usePolling(reload, 15000, [reload]);

  return (
    <Modal open={open} onClose={onClose} title="Histórico de envios" wide>
      <TableOrSkeleton data={log}>
        {(rows) => (
          <DataTable<NotificationLogEntry>
            rows={rows}
            keyFn={(e) => e.id}
            columns={[
              { key: "sent_at", label: "Quando", render: (e) => <span style={MUTED}>{fmtWhen(e.sent_at)}</span> },
              {
                // Canal + PARA QUEM. A coluna dizia só "whatsapp": com dois canais
                // de WhatsApp cadastrados, não dava para saber qual número recebeu
                // (ou deixou de receber) o alerta — que é justamente a pergunta de
                // quem reclama não ter sido avisado.
                //
                // Nome e destino são os do INSTANTE DO ENVIO, gravados na linha
                // (notification_log.channel_name/destination). Envios anteriores a
                // essa coluna existir aparecem com "—": preferimos o travessão a
                // exibir o destino de hoje ao lado de um envio de semanas atrás.
                key: "channel_type",
                label: "Canal e destinatário",
                render: (e) => (
                  <div>
                    <Badge state="info">{e.channel_type}</Badge>
                    {e.channel_name && (
                      <div style={{ fontSize: "var(--fs-12)", marginTop: 2 }}>{e.channel_name}</div>
                    )}
                    <div style={{ ...MONO, ...MUTED, marginTop: 2 }}>
                      {e.destination ? `→ ${e.destination}` : "→ —"}
                    </div>
                  </div>
                ),
              },
              { key: "subject", label: "Assunto", render: (e) => <span style={{ fontSize: "var(--fs-12)" }}>{e.subject}</span> },
              { key: "alert_count", label: "Alertas", hideOnMobile: true, render: (e) => <span style={MONO}>{e.alert_count}</span> },
              {
                key: "status",
                label: "Status",
                render: (e) => {
                  const s = STATUS[e.status] ?? { label: e.status, state: "crit" as const, hint: "" };
                  return (
                    <span className="row" style={{ gap: "var(--sp-1)", alignItems: "center", flexWrap: "wrap" }}>
                      <Badge state={s.state}>{s.label}</Badge>
                      {/* O motivo herda a cor do status: um aviso de "sem confirmação"
                          em vermelho de falha assusta sem necessidade. */}
                      {e.detail && (
                        <span style={{ color: `var(--${s.state})`, fontSize: "var(--fs-12)" }}>{e.detail}</span>
                      )}
                      {!e.detail && s.hint && (
                        <span style={{ color: "var(--text-3)", fontSize: "var(--fs-12)" }}>{s.hint}</span>
                      )}
                    </span>
                  );
                },
              },
            ]}
            empty={
              <EmptyState
                icon={<ReceiptText size={32} strokeWidth={1.5} />}
                title="Nenhum envio ainda"
                body="Aqui fica o registro de cada notificação enviada, canal, assunto e se deu certo. Assim que um alerta for encaminhado aos canais, o envio aparece nesta lista."
              />
            }
          />
        )}
      </TableOrSkeleton>
    </Modal>
  );
}
