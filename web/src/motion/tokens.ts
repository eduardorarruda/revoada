// Tokens de movimento para as animações em JS — espelho de --dur-* e --ease-* em
// styles/tokens.css. Mudou lá, muda aqui: CSS e JS têm de andar no mesmo ritmo.
export const DUR = { rapido: 0.12, base: 0.2, calmo: 0.32, longo: 0.52 } as const;

export const EASE = {
  saida: [0.16, 1, 0.3, 1],
  vaiVolta: [0.65, 0, 0.35, 1],
} as const;

/** Mola usada em painéis que entram (modal, gaveta, paleta): assenta sem quicar. */
export const MOLA = { type: "spring", stiffness: 420, damping: 36, mass: 0.8 } as const;
