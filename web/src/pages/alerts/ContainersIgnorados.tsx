// Containers ignorados: parados de propósito (desativados, reserva), deixam de
// alertar em todas as regras. A lista fica na aba Regras, com "Voltar a vigiar";
// o botão de ignorar fica na linha do alerta, na aba Ativos.
import { ActionIcons, Button, Card, DataTable, EmptyState, Skeleton } from "../../components";
import type { IgnoredContainer } from "../../api";
import { fmtRelAbs } from "../../format";

/** Só alerta de UM container, com servidor, pode ser ignorado. */
export function podeIgnorar(labels: Record<string, string> | undefined): boolean {
  return !!labels?.host && !!labels?.container;
}

export function ContainersIgnorados({
  lista,
  hostLabel,
  onVoltar,
}: {
  lista: IgnoredContainer[] | null;
  hostLabel: (h: string) => string;
  onVoltar: (c: IgnoredContainer) => void;
}) {
  return (
    <Card title="Containers ignorados">
      <p style={{ marginTop: 0, maxWidth: "70ch", color: "var(--text-2)" }}>
        Containers parados de propósito, que o painel não vigia mais: não abrem alerta em nenhuma regra e não aparecem
        na TV. Nada muda no servidor. Ao voltar a vigiar, se o container seguir parado, o alerta abre de novo em cerca
        de 1 minuto.
      </p>
      {lista === null ? (
        <Skeleton height={48} />
      ) : (
        <DataTable<IgnoredContainer>
          rows={lista}
          keyFn={(c) => `${c.host}\u0000${c.container}`}
          columns={[
            { key: "container", label: "Container", render: (c) => <strong>{c.container}</strong> },
            { key: "host", label: "Servidor", render: (c) => <span>{hostLabel(c.host)}</span> },
            {
              key: "created_at",
              label: "Ignorado",
              hideOnMobile: true,
              render: (c) => (
                <span style={{ color: "var(--text-3)" }} title={fmtRelAbs(c.created_at).abs}>
                  {fmtRelAbs(c.created_at).rel}
                  {c.created_by ? ` · por ${c.created_by}` : ""}
                </span>
              ),
            },
          ]}
          rowActions={(c) => (
            <Button variant="ghost" onClick={() => onVoltar(c)} aria-label={`Voltar a vigiar ${c.container}`}>
              <ActionIcons.unignore size={16} aria-hidden={true} /> Voltar a vigiar
            </Button>
          )}
          empty={
            <EmptyState
              title="Nenhum container ignorado"
              body="Se um container está parado de propósito, abra a aba Ativos e use o botão de sino cortado na linha do alerta dele."
            />
          }
        />
      )}
    </Card>
  );
}
