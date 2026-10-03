// usePolling: executa `fn` imediatamente e depois a cada `intervalMs`, porém a
// REPETIÇÃO só acontece enquanto a aba está visível. Com a aba em segundo plano
// (document.hidden / visibilityState === "hidden") o intervalo é suspenso; ao voltar
// a ficar visível, dispara um refetch imediato e retoma. Faz cleanup do timer no
// unmount e nunca chama `fn` depois de desmontado.
//
// A primeira carga é a exceção deliberada: ela ocorre mesmo com a aba escondida,
// porque quem monta a tela precisa de conteúdo (ver o comentário no `boot` abaixo).
//
// Não altera a cadência de negócio — apenas passa a respeitar a visibilidade,
// evitando que N abas esquecidas mantenham o servidor sob carga 24/7.
import { useEffect, useRef, type DependencyList } from "react";

export function usePolling(fn: () => void, intervalMs: number, deps: DependencyList = []): void {
  // Guarda sempre a última versão de `fn` sem re-assinar o efeito a cada render.
  const fnRef = useRef(fn);
  fnRef.current = fn;

  useEffect(() => {
    let alive = true;
    let timer: ReturnType<typeof setInterval> | null = null;

    const hidden = () => document.visibilityState === "hidden";

    // Só invoca `fn` se ainda montado e com a aba visível.
    const tick = () => {
      if (!alive || hidden()) return;
      fnRef.current();
    };

    // A PRIMEIRA carga acontece sempre, mesmo com a aba escondida. Suspender a
    // repetição é correto; suprimir o boot não é: uma TV/kiosk que sobe com a aba em
    // segundo plano (o caso normal de um monitor de parede) ficava em "Carregando…"
    // para sempre — sem erro, sem idade, sem nada. Medido: /#/tv/<token> passou 52 s
    // sem uma única chamada e /#/status passou 181 s. É UMA requisição por montagem;
    // a carga contínua continua pausada enquanto ninguém olha.
    const boot = () => {
      if (!alive) return;
      fnRef.current();
    };

    const start = () => {
      if (timer === null) timer = setInterval(tick, intervalMs);
    };
    const stop = () => {
      if (timer !== null) {
        clearInterval(timer);
        timer = null;
      }
    };

    const onVisibility = () => {
      if (hidden()) {
        stop();
      } else {
        tick(); // refetch imediato ao reganhar foco
        start();
      }
    };

    // Boot: busca uma vez sempre; o intervalo só gira com a aba visível.
    boot();
    if (!hidden()) start();
    document.addEventListener("visibilitychange", onVisibility);

    return () => {
      alive = false;
      stop();
      document.removeEventListener("visibilitychange", onVisibility);
    };
    // A cadência e as deps do chamador determinam quando re-assinar. O `fn` entra por
    // ref (fnRef) de propósito, e as deps vêm de quem chama: o lint não vê através do spread.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [intervalMs, ...deps]);
}
