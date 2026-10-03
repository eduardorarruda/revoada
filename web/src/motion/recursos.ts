// Recursos de animação do Motion, carregados SOB DEMANDA pelo MotionRoot.
//
// domMax (e não domAnimation) porque o indicador do menu usa `layoutId` — a pílula
// da marca que desliza de um item para o outro — e animação de layout só existe no
// domMax. Por ficar num import dinâmico, nada disto entra no pacote inicial: a tela
// pinta primeiro, e o motor de animação chega logo depois.
import { domMax } from "motion/react";

export default domMax;
