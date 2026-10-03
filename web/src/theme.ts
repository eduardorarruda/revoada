// Controle de tema (dark-first). Persiste em localStorage e reflete em data-theme.
export type Theme = "dark" | "light";

const KEY = "revoada-theme";

export function getTheme(): Theme {
  const el = document.documentElement.getAttribute("data-theme");
  return el === "light" ? "light" : "dark";
}

export function setTheme(theme: Theme): void {
  document.documentElement.setAttribute("data-theme", theme);
  try {
    localStorage.setItem(KEY, theme);
  } catch {
    /* ignore */
  }
}

export function initTheme(): void {
  let stored: string | null = null;
  try {
    stored = localStorage.getItem(KEY);
  } catch {
    /* ignore */
  }
  // Mesma decisão do script inline em index.html (UX-19): valor salvo, senão a
  // preferência do sistema. Idempotente — reflete o que o boot já aplicou.
  if (stored === "light" || stored === "dark") {
    setTheme(stored);
    return;
  }
  const prefersLight =
    typeof window !== "undefined" &&
    typeof window.matchMedia === "function" &&
    window.matchMedia("(prefers-color-scheme: light)").matches;
  setTheme(prefersLight ? "light" : "dark");
}
