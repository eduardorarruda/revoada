"""Modelo de resultado e saída (tabela no terminal, JSON e Markdown)."""
import json
import threading
import time

PASSOU, FALHOU, INCONCLUSIVO = "ok", "falhou", "inconclusivo"
_SIMBOLO = {PASSOU: "✅", FALHOU: "❌", INCONCLUSIVO: "⚠️"}


class Resultados:
    def __init__(self):
        self.itens = []
        self._mu = threading.Lock()

    def add(self, grupo, nome, esperado, observado, tolerancia, veredito, detalhe=""):
        if veredito is True:
            veredito = PASSOU
        elif veredito is False:
            veredito = FALHOU
        elif veredito is None:
            veredito = INCONCLUSIVO
        item = {
            "grupo": grupo, "nome": nome, "esperado": str(esperado), "observado": str(observado),
            "tolerancia": str(tolerancia), "veredito": veredito, "detalhe": detalhe,
            "instante": time.strftime("%Y-%m-%dT%H:%M:%S%z"),
        }
        with self._mu:
            self.itens.append(item)
        print(f"  {_SIMBOLO[veredito]} [{grupo}] {nome}: esperado {esperado} | observado {observado}"
              f" | tol {tolerancia}" + (f" — {detalhe}" if detalhe and veredito != PASSOU else ""), flush=True)
        return item

    def resumo(self):
        c = {PASSOU: 0, FALHOU: 0, INCONCLUSIVO: 0}
        for i in self.itens:
            c[i["veredito"]] += 1
        return c


def _corta(s, n):
    s = s.replace("\n", " ").replace("|", "\\|")
    return s if len(s) <= n else s[: n - 1] + "…"


def tabela_terminal(res):
    linhas = ["", f"{'grupo':<10} {'verificação':<46} {'esperado':<28} {'observado':<28} {'tol':<14} v"]
    for i in res.itens:
        linhas.append(f"{i['grupo'][:10]:<10} {_corta(i['nome'], 46):<46} {_corta(i['esperado'], 28):<28} "
                      f"{_corta(i['observado'], 28):<28} {_corta(i['tolerancia'], 14):<14} {_SIMBOLO[i['veredito']]}")
    r = res.resumo()
    linhas.append(f"\n{r[PASSOU]} ok, {r[FALHOU]} falharam, {r[INCONCLUSIVO]} inconclusivas")
    return "\n".join(linhas)


def gravar(res, caminho_base, contexto):
    dados = {"contexto": contexto, "resumo": res.resumo(), "verificacoes": res.itens}
    with open(caminho_base + ".json", "w") as f:
        json.dump(dados, f, ensure_ascii=False, indent=2)
    md = [f"# Validação do Revoada — {contexto.get('inicio', '')}", ""]
    for k, v in contexto.items():
        md.append(f"- **{k}**: {v}")
    r = res.resumo()
    md += ["", f"**Resumo:** {r[PASSOU]} ok · {r[FALHOU]} falharam · {r[INCONCLUSIVO]} inconclusivas", ""]
    grupos = []
    for i in res.itens:
        if i["grupo"] not in grupos:
            grupos.append(i["grupo"])
    for g in grupos:
        md += [f"## {g}", "", "| verificação | esperado | observado | tolerância | | detalhe |", "|---|---|---|---|---|---|"]
        for i in res.itens:
            if i["grupo"] == g:
                md.append(f"| {_corta(i['nome'], 90)} | {_corta(i['esperado'], 120)} | {_corta(i['observado'], 120)} | "
                          f"{_corta(i['tolerancia'], 60)} | {_SIMBOLO[i['veredito']]} | {_corta(i['detalhe'], 300)} |")
        md.append("")
    with open(caminho_base + ".md", "w") as f:
        f.write("\n".join(md))
    return caminho_base + ".json", caminho_base + ".md"
