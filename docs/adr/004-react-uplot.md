# ADR 004 — Frontend React + uPlot/ECharts

- **Status:** aceito
- **Referência:** [ARQUITETURA.md §5](../ARQUITETURA.md) (decisões de arquitetura)

## Contexto
A UI precisa renderizar séries temporais densas em tempo real (TV 24/7) sem travar, com um
design system consistente e dark-first.

## Decisão
**React 19 + TypeScript + Vite**; **uPlot** para séries temporais (leve, altíssima performance)
e **ECharts** para os demais tipos (heatmap, gauge, etc.); TanStack Query + Zustand para estado.

## Alternativas consideradas
- Chart.js/Recharts — reflow caro com muitos pontos e atualização ao vivo.
- D3 puro — flexível, porém custo de desenvolvimento alto para cada painel.
- Vue/Svelte — ecossistema de observabilidade e familiaridade menores no time.

## Consequências
- (+) uPlot aguenta milhares de pontos com atualização contínua sem drop de frames.
- (+) Dois motores cobrem todos os painéis sem reinventar cada um.
- (−) Dois motores de gráfico para manter coeso via o design system (tokens.css).
