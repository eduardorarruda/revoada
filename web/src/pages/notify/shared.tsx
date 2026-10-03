// Utilitários compartilhados pela aba Canais de Notificações.
import type { ReactNode } from "react";
import { Skeleton } from "../../components";
import { formatDateTime } from "../../format";

// Estilos utilitários (texto secundário e monoespaçado) via variáveis do tema.
export const MUTED = { color: "var(--text-3)" } as const;
export const MONO = { fontFamily: "var(--font-mono)", fontSize: "var(--fs-12)" } as const;

// Formata um ISO 8601 como "DD/MM/AAAA HH:MM" no HORÁRIO DE BRASÍLIA. Antes o
// código truncava a string (`slice(0,16)`), descartando o offset e exibindo o
// horário do servidor (UTC); depois passou a usar o fuso do navegador, que numa
// máquina em UTC dá o mesmo erro de 3 h. Agora o fuso é fixo e explícito.
export function fmtWhen(iso: string): string {
  if (!iso) return "—";
  return formatDateTime(iso, { seconds: false });
}

// Enquanto data===null mostra skeletons; senão entrega as linhas ao filho.
export function TableOrSkeleton<T>({ data, children }: { data: T[] | null; children: (rows: T[]) => ReactNode }) {
  if (data === null) {
    return (
      <div style={{ display: "flex", flexDirection: "column", gap: "var(--sp-2)" }}>
        {[0, 1, 2, 3].map((i) => (
          <Skeleton key={i} height={40} />
        ))}
      </div>
    );
  }
  return <>{children(data)}</>;
}
