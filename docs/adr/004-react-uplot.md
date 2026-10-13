# ADR 004 — Frontend React + uPlot

- **Status:** aceito
- **Referência:** [ARQUITETURA.md §5](../ARQUITETURA.md) (decisões de arquitetura)

## Contexto
A UI precisa renderizar séries temporais densas em tempo real (TV 24/7) sem travar, com um
design system consistente e dark-first.

## Decisão
**React 19 + TypeScript + Vite**; **uPlot** para séries temporais (leve, feito para muitos
pontos). Os demais painéis (`stat`, `gauge`, `bargauge`, `table`) são componentes próprios em
`web/src/panels/` (SVG e HTML), sem outra biblioteca de gráficos. O estado fica em hooks do
React (ex.: `usePolling`), sem biblioteca de estado.

## Alternativas consideradas
- Chart.js/Recharts — reflow caro com muitos pontos e atualização ao vivo.
- D3 puro — flexível, porém custo de desenvolvimento alto para cada painel.
- Vue/Svelte — ecossistema de observabilidade e familiaridade menores no time.

## Consequências
- (+) uPlot aguenta milhares de pontos com atualização contínua sem drop de frames.
- (+) Uma única dependência de gráfico (`uplot` em `web/package.json`).
- (−) Cada tipo de painel que não é série temporal é código próprio para manter, alinhado ao
  design system (`tokens.css`).
