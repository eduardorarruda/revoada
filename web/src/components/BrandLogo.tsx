import "./brand.css";

type Props = {
  /** "full" = pássaro + nome Revoada; "mark" = só o símbolo (o pássaro). */
  variant?: "full" | "mark";
  /** md = menu e cabeçalhos; lg = telas de entrada (login, status público). */
  size?: "md" | "lg";
  className?: string;
};

/**
 * Logotipo do Revoada. O símbolo é o mesmo pássaro do favicon; o nome é texto
 * (não imagem), então herda a cor do tema pelos tokens e fica nítido em qualquer tela.
 * O pássaro tem traço fino (asas em rede): abaixo de ~36px ele vira um borrão, por
 * isso o menor tamanho usado é 40px.
 */
export function BrandLogo({ variant = "full", size = "md", className }: Props) {
  const cls = ["brand-logo", `brand-logo--${variant}`, `brand-logo--${size}`, className]
    .filter(Boolean)
    .join(" ");

  if (variant === "mark") {
    return <img src="/logo-mark.png" alt="Revoada" className={cls} />;
  }

  return (
    <span className={cls} aria-label="Revoada" role="img">
      <img src="/logo-mark.png" alt="" className="brand-logo__mark" />
      <span className="brand-logo__name" aria-hidden="true">
        Revoada
      </span>
    </span>
  );
}
