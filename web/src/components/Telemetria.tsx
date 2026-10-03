// Telemetria — o fundo vivo da tela de login: linhas de métrica correndo da
// direita para a esquerda, como um painel de verdade respirando.
//
// É um canvas desenhado à mão (nada de vídeo nem imagem): ~2 KB de código e uma
// animação que custa quase nada. Regras de boa convivência:
//  - pausa com a aba escondida (ninguém olhando, nada desenhando);
//  - com "menos movimento" pedido no sistema, desenha UM quadro e para;
//  - tem botão de pausar: movimento contínuo precisa de um jeito de parar
//    (WCAG 2.2.2), e a preferência do sistema sozinha não conta como um;
//  - cores saem dos tokens do tema (nada de cor fixa no código).
import { useEffect, useRef, useState } from "react";
import { Pause, Play } from "lucide-react";
import { cssVar } from "../panels/chart";

interface Linha {
  pontos: number[];
  fase: number;
  amplitude: number;
  base: number;
  cor: string;
  largura: number;
}

export function Telemetria() {
  const ref = useRef<HTMLCanvasElement>(null);
  const [pausado, setPausado] = useState(false);
  const pausadoRef = useRef(false);
  const retomar = useRef<() => void>(() => {});
  pausadoRef.current = pausado;
  useEffect(() => {
    if (!pausado) retomar.current();
  }, [pausado]);

  useEffect(() => {
    const canvas = ref.current;
    const ctx = canvas?.getContext("2d");
    if (!canvas || !ctx) return;
    const parado = window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;
    const cores = [cssVar("--accent"), cssVar("--accent-dim"), cssVar("--info"), cssVar("--accent")];
    const grade = cssVar("--border");
    const dpr = Math.min(window.devicePixelRatio || 1, 2);
    const PASSO = 6; // px entre pontos
    let largura = 0;
    let altura = 0;
    let linhas: Linha[] = [];
    let quadro = 0;
    let ultimo = 0;
    let t = 0;

    const amostra = (l: Linha) => {
      t += 0.0009;
      l.fase += 0.07 + l.amplitude * 0.0006;
      const ruido = Math.sin(l.fase) * 0.5 + Math.sin(l.fase * 2.3 + t * 40) * 0.3 + (Math.random() - 0.5) * 0.35;
      // Pico ocasional: é assim que métrica de verdade se parece.
      const pico = Math.random() < 0.012 ? (Math.random() - 0.3) * 2.4 : 0;
      return l.base + (ruido + pico) * l.amplitude;
    };

    const montar = () => {
      const r = canvas.getBoundingClientRect();
      largura = r.width;
      altura = r.height;
      canvas.width = Math.round(largura * dpr);
      canvas.height = Math.round(altura * dpr);
      ctx.setTransform(dpr, 0, 0, dpr, 0, 0);
      const n = Math.ceil(largura / PASSO) + 2;
      linhas = [0.28, 0.45, 0.62, 0.78].map((y, i) => {
        const l: Linha = {
          pontos: [],
          fase: Math.random() * 10,
          amplitude: altura * (0.035 + i * 0.01),
          base: altura * y,
          cor: cores[i % cores.length],
          largura: i === 1 ? 2 : 1.25,
        };
        for (let k = 0; k < n; k++) l.pontos.push(amostra(l));
        return l;
      });
    };

    const desenhar = () => {
      ctx.clearRect(0, 0, largura, altura);
      // grade discreta
      ctx.strokeStyle = grade;
      ctx.lineWidth = 1;
      for (let x = (largura % 48) / 2; x < largura; x += 48) {
        ctx.beginPath();
        ctx.moveTo(x, 0);
        ctx.lineTo(x, altura);
        ctx.stroke();
      }
      for (const l of linhas) {
        const g = ctx.createLinearGradient(0, 0, largura, 0);
        g.addColorStop(0, l.cor + "00");
        g.addColorStop(0.55, l.cor + "66");
        g.addColorStop(1, l.cor);
        ctx.strokeStyle = g;
        ctx.lineWidth = l.largura;
        ctx.lineJoin = "round";
        ctx.beginPath();
        l.pontos.forEach((y, k) => (k === 0 ? ctx.moveTo(k * PASSO, y) : ctx.lineTo(k * PASSO, y)));
        ctx.stroke();
        // cabeça da linha: o dado que acabou de chegar
        const xFim = (l.pontos.length - 1) * PASSO;
        const yFim = l.pontos[l.pontos.length - 1];
        ctx.fillStyle = l.cor;
        ctx.shadowColor = l.cor;
        ctx.shadowBlur = 14;
        ctx.beginPath();
        ctx.arc(Math.min(xFim, largura - 6), yFim, l.largura + 1.5, 0, Math.PI * 2);
        ctx.fill();
        ctx.shadowBlur = 0;
      }
    };

    const rodar = (agora: number) => {
      // Vitrine escondida (celular) = canvas 0×0: nada a desenhar, nada a gastar.
      if (pausadoRef.current || largura === 0) {
        quadro = 0;
        return;
      }
      quadro = requestAnimationFrame(rodar);
      if (agora - ultimo < 1000 / 30) return; // 30 quadros/s bastam
      ultimo = agora;
      for (const l of linhas) {
        l.pontos.shift();
        l.pontos.push(amostra(l));
      }
      desenhar();
    };

    const visibilidade = () => {
      cancelAnimationFrame(quadro);
      if (document.visibilityState === "visible" && !parado && !pausadoRef.current) quadro = requestAnimationFrame(rodar);
    };
    retomar.current = () => {
      if (!parado && !quadro && document.visibilityState === "visible") quadro = requestAnimationFrame(rodar);
    };

    montar();
    desenhar();
    if (!parado) quadro = requestAnimationFrame(rodar);
    const ro = new ResizeObserver(() => {
      montar();
      desenhar();
      retomar.current();
    });
    ro.observe(canvas);
    document.addEventListener("visibilitychange", visibilidade);
    return () => {
      cancelAnimationFrame(quadro);
      ro.disconnect();
      document.removeEventListener("visibilitychange", visibilidade);
    };
  }, []);

  return (
    <>
      <canvas ref={ref} className="telemetria" aria-hidden="true" />
      <button
        type="button"
        className="telemetria__pausa"
        onClick={() => setPausado((p) => !p)}
        aria-pressed={pausado}
      >
        {pausado ? <Play size={14} aria-hidden={true} /> : <Pause size={14} aria-hidden={true} />}
        {pausado ? "Retomar animação" : "Pausar animação"}
      </button>
    </>
  );
}
