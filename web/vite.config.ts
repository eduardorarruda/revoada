/// <reference types="vitest/config" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Porta do dev server e alvo do proxy /api são configuráveis por env (útil para
// rodar uma segunda instância contra um server em outra porta). Defaults: 5173→:8091.
const devPort = Number(process.env.VITE_PORT) || 5173;
const proxyTarget = process.env.PROXY_TARGET || "http://127.0.0.1:8091";

export default defineConfig({
  plugins: [react()],
  server: {
    port: devPort,
    host: true,
    // Em dev, encaminha /api para o server (evita CORS). O server roda em :8091.
    proxy: { "/api": { target: proxyTarget, changeOrigin: true, ws: true } },
  },
  test: {
    environment: "jsdom",
    globals: true,
    setupFiles: ["./src/test/setup.ts"],
  },
});
