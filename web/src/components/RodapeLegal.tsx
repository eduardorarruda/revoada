import "./rodape-legal.css";

/**
 * Rodapé legal: a política de privacidade (LGPD) a um clique de qualquer tela, com ou
 * sem login, e o link do código aberto. Vai no login, na status page e no menu do
 * painel.
 */
export function RodapeLegal({ className }: { className?: string }) {
  return (
    <nav className={["rodape-legal", className].filter(Boolean).join(" ")} aria-label="Informações legais">
      <a href="#/privacidade">Política de privacidade</a>
      <span aria-hidden="true">·</span>
      <a href="https://github.com/eduardorarruda/revoada" target="_blank" rel="noopener noreferrer">
        Revoada, código aberto (AGPL-3.0)
      </a>
    </nav>
  );
}
