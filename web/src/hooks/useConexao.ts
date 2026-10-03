// useConexao — assina o estado real da conexão (web/src/conexao.ts) para a tela
// poder mostrar "no ar" ou "reconectando…" com base no que de fato está
// acontecendo, e não com base numa constante escrita no código.
import { useEffect, useState } from "react";
import { estadoConexao, ouvirConexao, type EstadoConexao } from "../conexao";

export function useConexao(): EstadoConexao {
  const [estado, setEstado] = useState<EstadoConexao>(() => estadoConexao());
  useEffect(() => ouvirConexao(setEstado), []);
  return estado;
}
