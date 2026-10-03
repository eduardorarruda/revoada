import { useEffect, useRef } from "react";
import uPlot from "uplot";
import "uplot/dist/uPlot.min.css";
import { PanelFrame } from "./PanelFrame";
import type { PanelBaseProps, TimeSeriesData } from "./types";
import { cssVar, seriesPalette } from "./chart";
import { openCorrelation } from "../api";
import { APP_TIME_ZONE, formatValue, rotulosEixo } from "../format";
import { instante, rotulosTempo } from "./tempo";

// tzDate obriga o eixo X e o tooltip do uPlot a falarem HORÁRIO DE BRASÍLIA.
// Sem isto o uPlot usa o fuso do navegador: numa TV/kiosk ou VM em UTC (padrão de
// imagem de servidor), o gráfico marcava o incidente 3 h à frente do que o resto
// do painel e das notificações diziam.
const tzDate = (ts: number) => uPlot.tzDate(new Date(ts * 1000), APP_TIME_ZONE);

// Painel de série temporal (uPlot — leve e rápido para tempo real).
export function TimeSeriesPanel({ data, ...base }: PanelBaseProps & { data: TimeSeriesData }) {
  const ref = useRef<HTMLDivElement>(null);
  const plotRef = useRef<uPlot | null>(null);
  // O gráfico é recriado a cada atualização de dados; a entrada animada (a linha
  // se desenhando) só acontece na PRIMEIRA vez — repetir a cada 15 s seria ruído.
  const jaDesenhou = useRef(false);

  useEffect(() => {
    if (!ref.current || base.state === "loading") return;
    const palette = seriesPalette();
    const el = ref.current;
    const unit = base.unit ?? "none";
    // Formatadores alimentados pelo dicionário de unidades: o eixo Y usa a forma
    // compacta (ticks) e a legenda/valor a forma completa. Sem isto o uPlot mostrava
    // ticks crus "0 1 2 3" e a legenda "--" fora do cursor.
    const valFmt = (v: number | null) => (v == null ? "—" : formatValue(v, unit));

    // Degradê sob a linha: forte perto do traço, some até a base. Dá volume à série
    // sem esconder a grade — e é o que faz o gráfico parecer desenhado, não plotado.
    // Na primeira passada o uPlot ainda não mediu a área de desenho (bbox sem
    // números) e createLinearGradient lança exceção — cai no preenchimento chapado.
    const degrade = (cor: string) => (u: uPlot) => {
      const { top, height } = u.bbox ?? {};
      if (!Number.isFinite(top) || !Number.isFinite(height) || height <= 0) return cor + "18";
      const g = u.ctx.createLinearGradient(0, top, 0, top + height);
      g.addColorStop(0, cor + "38");
      g.addColorStop(1, cor + "00");
      return g;
    };

    const alignedData: uPlot.AlignedData = [
      data.ts,
      ...data.series.map((s) => s.values),
    ];

    const opts: uPlot.Options = {
      width: el.clientWidth || 400,
      height: 220,
      padding: [8, 8, 0, 0],
      // O marcador da legenda usa a cor SÓLIDA da série: o preenchimento é um degradê
      // (ou a cor a 9% antes de medir o canvas) e deixava o marcador invisível —
      // a legenda deixava de dizer qual linha é qual.
      legend: {
        live: true,
        markers: { fill: (_u: uPlot, i: number) => (i === 0 ? "transparent" : palette[(i - 1) % palette.length]) },
      },
      cursor: { sync: { key: "revoada" } },
      scales: { x: { time: true } },
      tzDate,
      axes: [
        {
          stroke: cssVar("--text-3"),
          grid: { stroke: cssVar("--border") },
          ticks: { stroke: cssVar("--border") },
          // Horário de Brasília em 24 h, com a data quando o dia vira (padrão do
          // uPlot era "4:30pm" e "9/24/26").
          values: (u, splits) => {
            const [min, max] = [u.scales.x.min ?? 0, u.scales.x.max ?? 0];
            return rotulosTempo(splits, max - min);
          },
          font: `11px ${cssVar("--font-sans")}`,
        },
        {
          stroke: cssVar("--text-3"),
          grid: { stroke: cssVar("--border") },
          ticks: { stroke: cssVar("--border") },
          values: (_u, splits) => rotulosEixo(splits, unit),
          font: `11px ${cssVar("--font-sans")}`,
          size: 56,
        },
      ],
      series: [
        { label: "Tempo", value: (_u: uPlot, v: number | null) => instante(v) },
        ...data.series.map((s, i) => ({
          label: s.label,
          stroke: palette[i % palette.length],
          width: 1.75,
          fill: degrade(palette[i % palette.length]),
          points: { show: false },
          value: (_u: uPlot, v: number | null) => valFmt(v),
        })),
      ],
    };

    const plot = new uPlot(opts, alignedData, el);
    plotRef.current = plot;
    if (!jaDesenhou.current) {
      jaDesenhou.current = true;
      el.classList.add("uplot-wrap--entrada");
      // Tira a classe depois da entrada: o canvas é recriado a cada atualização e,
      // com a classe ainda no invólucro, a linha se redesenharia a cada 15 s.
      // (Fora do cleanup do efeito de propósito — uma atualização no meio não pode
      // cancelar a remoção e deixar a classe presa.)
      window.setTimeout(() => el.classList.remove("uplot-wrap--entrada"), 1000);
    }

    // Clique (sem arrastar) num ponto → painel de correlação daquele instante (P5.4).
    const onClick = () => {
      if (plot.select.width > 2) return; // foi um drag-zoom, não um clique
      const left = plot.cursor.left;
      if (left == null || left < 0) return;
      const xVal = plot.posToVal(left, "x");
      if (!isFinite(xVal)) return;
      openCorrelation({ tsMs: Math.round(xVal * 1000), host: base.correlateHost, label: base.title });
    };
    plot.over.addEventListener("click", onClick);

    const ro = new ResizeObserver(() => plot.setSize({ width: el.clientWidth, height: 220 }));
    ro.observe(el);

    return () => {
      ro.disconnect();
      plot.over.removeEventListener("click", onClick);
      plot.destroy();
      plotRef.current = null;
    };
    // O clique abre a correlação com o host e o título ATUAIS; sem eles aqui, um painel
    // renomeado continuaria abrindo com o título antigo.
  }, [data, base.state, base.unit, base.correlateHost, base.title]);

  return (
    <PanelFrame {...base}>
      <div ref={ref} className="uplot-wrap" />
    </PanelFrame>
  );
}
