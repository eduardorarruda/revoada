// CountUp: o número do indicador corre do valor anterior até o novo.
//
// Recebe o TEXTO já formatado ("47,2%", "1/3", "1.284 req/s") e anima só o primeiro
// número dele, mantendo casas decimais, separador de milhar e o resto do texto. Assim
// entra em qualquer KPI sem que a tela troque a forma como formata.
//
// O movimento carrega informação: um número que sobe de 12 para 47 diz "subiu" antes
// de a pessoa ler. Com menos movimento pedido no sistema, o número só troca.
import { useEffect, useRef, useState } from "react";
import { useReducedMotion } from "motion/react";
import { DUR } from "./tokens";

// Curva de saída (mesma de --ease-out): rápida no começo, assenta no fim.
const saida = (t: number) => 1 - Math.pow(1 - t, 4);

// Interpolação própria em vez do `animate()` do Motion: ele traz o motor híbrido
// inteiro (~18 KB) para animar um número. Isto aqui são 15 linhas.
function interpolar(de: number, para: number, duracaoS: number, aCada: (v: number) => void, fim: () => void): () => void {
  const inicio = performance.now();
  let quadro = requestAnimationFrame(function passo(agora) {
    // O carimbo do rAF pode ser anterior ao performance.now() tirado acima.
    const t = Math.min(1, Math.max(0, (agora - inicio) / (duracaoS * 1000)));
    aCada(de + (para - de) * saida(t));
    if (t < 1) quadro = requestAnimationFrame(passo);
    else fim();
  });
  return () => cancelAnimationFrame(quadro);
}

interface Partes {
  antes: string;
  numero: number;
  casas: number;
  milhar: boolean;
  depois: string;
}

// Número pt-BR: milhar com ponto, decimal com vírgula ("1.284,5"; "47,2"; "3").
const NUMERO = /^(.*?)(-?\d{1,3}(?:\.\d{3})+(?:,\d+)?|-?\d+(?:,\d+)?)(.*)$/s;

export function separar(texto: string): Partes | null {
  const m = NUMERO.exec(texto);
  if (!m) return null;
  const bruto = m[2];
  const [inteiro, frac = ""] = bruto.split(",");
  const numero = Number(`${inteiro.replace(/\./g, "")}.${frac || "0"}`);
  if (!Number.isFinite(numero)) return null;
  return { antes: m[1], numero, casas: frac.length, milhar: inteiro.includes("."), depois: m[3] };
}

export function montar(p: Partes, valor: number): string {
  const txt = valor.toLocaleString("pt-BR", {
    minimumFractionDigits: p.casas,
    maximumFractionDigits: p.casas,
    useGrouping: p.milhar,
  });
  return `${p.antes}${txt}${p.depois}`;
}

export function CountUp({ texto }: { texto: string }) {
  const reduzir = useReducedMotion();
  // Nasce já no ponto de partida (zero): começar pelo valor final e só depois
  // voltar a zero fazia cada KPI piscar no primeiro carregamento.
  const [mostrado, setMostrado] = useState(() => {
    const p = separar(texto);
    return p && !reduzir ? montar(p, 0) : texto;
  });
  const anterior = useRef<number | null>(null);

  useEffect(() => {
    const p = separar(texto);
    if (!p || reduzir) {
      setMostrado(texto);
      // Texto sem número ("—" de dado ausente) NÃO apaga o último valor medido: quando
      // o dado volta, o número segue dali, em vez de correr do zero como se tivesse
      // zerado de verdade.
      if (p) anterior.current = p.numero;
      return;
    }
    // Primeira aparição corre do zero; depois, do último valor mostrado.
    const de = anterior.current ?? 0;
    anterior.current = p.numero;
    if (de === p.numero) {
      setMostrado(texto);
      return;
    }
    return interpolar(de, p.numero, DUR.longo + 0.18, (v) => setMostrado(montar(p, v)), () => setMostrado(texto));
  }, [texto, reduzir]);

  return <>{mostrado}</>;
}
