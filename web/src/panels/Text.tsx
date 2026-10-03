import { PanelFrame } from "./PanelFrame";
import type { PanelBaseProps } from "./types";

// Painel de texto/markdown-lite: títulos (#), negrito (**), listas (-) e parágrafos.
export function TextPanel({ markdown, ...base }: PanelBaseProps & { markdown: string }) {
  return (
    <PanelFrame {...base}>
      <div className="panel__text">{render(markdown)}</div>
    </PanelFrame>
  );
}

function render(md: string) {
  return md.split("\n").map((line, i) => {
    if (line.startsWith("# ")) return <h4 key={i} style={{ margin: "var(--sp-2) 0" }}>{inline(line.slice(2))}</h4>;
    if (line.startsWith("- ")) return <li key={i}>{inline(line.slice(2))}</li>;
    if (line.trim() === "") return <br key={i} />;
    return <p key={i} style={{ margin: "var(--sp-1) 0", color: "var(--text-2)" }}>{inline(line)}</p>;
  });
}

// negrito **texto**
function inline(s: string) {
  const parts = s.split(/(\*\*[^*]+\*\*)/g);
  return parts.map((p, i) =>
    p.startsWith("**") && p.endsWith("**") ? <strong key={i}>{p.slice(2, -2)}</strong> : <span key={i}>{p}</span>,
  );
}
