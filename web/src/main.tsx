import { StrictMode } from "react";
import { createRoot } from "react-dom/client";
import { App } from "./App";
import { initTheme } from "./theme";
import { MotionRoot } from "./motion";
// Geist servida pelo próprio painel (sem CDN: o painel roda em rede fechada e em TV).
// O CSS do fontsource declara cada subconjunto com unicode-range — o navegador baixa
// só o latino que o pt-BR usa.
import "@fontsource-variable/geist";
import "@fontsource-variable/geist-mono";
import "./styles/tokens.css";

initTheme();

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <MotionRoot>
      <App />
    </MotionRoot>
  </StrictMode>,
);
