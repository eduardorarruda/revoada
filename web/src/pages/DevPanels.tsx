// /dev/panels — vitrine dos painéis essenciais com dados representativos (só em dev).
// Dados reais do ClickHouse chegam via Query API na tela de dashboard.
import { useState } from "react";
import { Button } from "../components";
import { getTheme, setTheme, type Theme } from "../theme";
import { DEFAULT_THRESHOLDS } from "../metrics/thresholds";
import {
  BarGaugePanel,
  GaugePanel,
  StatPanel,
  TablePanel,
  TimeSeriesPanel,
  type TimeSeriesData,
} from "../panels";

function sampleSeries(): TimeSeriesData {
  const now = Math.floor(Date.now() / 1000);
  const ts: number[] = [];
  const a: number[] = [];
  const b: number[] = [];
  for (let i = 120; i >= 0; i--) {
    ts.push(now - i * 60);
    a.push(40 + 20 * Math.sin(i / 8) + Math.random() * 6);
    b.push(60 + 15 * Math.cos(i / 10) + Math.random() * 5);
  }
  return { ts, series: [{ label: "host-01", values: a }, { label: "host-02", values: b }] };
}

const grid: React.CSSProperties = {
  display: "grid",
  gridTemplateColumns: "repeat(auto-fill, minmax(320px, 1fr))",
  gap: "var(--sp-4)",
};

export function DevPanels() {
  const spark = Array.from({ length: 24 }, (_, i) => 50 + 10 * Math.sin(i / 3));
  const [theme, setThemeState] = useState<Theme>(getTheme());

  const toggle = () => {
    const next: Theme = theme === "dark" ? "light" : "dark";
    setTheme(next);
    setThemeState(next);
  };

  return (
    <div style={{ padding: "var(--sp-6)", maxWidth: 1200, margin: "0 auto" }}>
      <header
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          marginBottom: "var(--sp-4)",
        }}
      >
        <div>
          <h1 style={{ margin: 0, fontSize: "var(--fs-28)" }}>Painéis essenciais</h1>
          <p style={{ color: "var(--text-2)", margin: "var(--sp-1) 0 0" }}>
            Tema atual: <strong>{theme}</strong>
          </p>
        </div>
        <Button variant="primary" onClick={toggle}>
          Alternar tema
        </Button>
      </header>
      <div style={grid}>
        <TimeSeriesPanel title="CPU (%)" description="Uso de CPU por host. Normal < 70%." data={sampleSeries()} freshness={{ status: "live", ageSeconds: 2 }} />
        <StatPanel title="Uptime médio" description="Disponibilidade dos hosts nas últimas 24h." value={99.94} unit="percent" trend={0.3} spark={spark} />
        <GaugePanel title="Uso de disco /" description="Espaço usado na raiz. Limiar padrão de disco: atenção ≥ 75%, crítico ≥ 90%." value={82.4} threshold={DEFAULT_THRESHOLDS.disk} />
        <BarGaugePanel
          title="Memória por host"
          description="RAM usada (%). Limiar padrão de RAM: atenção ≥ 70%, crítico ≥ 90%."
          threshold={DEFAULT_THRESHOLDS.mem}
          items={[
            { label: "host-01", value: 62 },
            { label: "host-02", value: 88 },
            { label: "host-03", value: 45 },
          ]}
        />
        <TablePanel
          title="Top processos"
          description="Processos por consumo de CPU."
          columns={[
            { key: "pid", label: "PID", numeric: true },
            { key: "cmd", label: "Comando" },
            { key: "cpu", label: "%CPU", numeric: true, threshold: { warn: 20, crit: 50 } },
            { key: "mem", label: "%MEM", numeric: true, threshold: { warn: 10, crit: 25 } },
          ]}
          rows={[
            { pid: 3009, cmd: "app", cpu: 62, mem: 5.7 },
            { pid: 1200, cmd: "postgres", cpu: 18, mem: 12 },
            { pid: 880, cmd: "nginx", cpu: 4, mem: 1.2 },
          ]}
        />
      </div>
    </div>
  );
}
