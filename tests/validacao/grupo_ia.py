"""Grupo ia: chamadas de agentes de IA (tela Agentes de IA).

Envia ao gateway, por OTLP/JSON, spans de IA montados à mão com números conhecidos e
confere o que as MESMAS rotas da tela devolvem:

  - visão geral          -> GET /api/ia/resumo
  - lista de execuções   -> GET /api/ia/execucoes
  - replay               -> GET /api/ia/execucoes/{trace}
  - conteúdo             -> GET /api/ia/execucoes/{trace}/conteudo
  - waterfall (rótulos)  -> GET /api/traces/{trace}
  - séries de alerta     -> POST /api/query (llm.chamadas, gravada pelo runner com 5 min de atraso)

O preço usado é uma linha criada pelo próprio teste (modelo exclusivo da rodada, preço
redondo), então o custo esperado é conta de padaria — e a linha é apagada no fim.
A verdade aqui é o que foi ENVIADO: cada token, cada erro e cada ferramenta.
"""
import json
import time
import urllib.error
import urllib.request
from datetime import datetime, timezone

G = "ia"
PRECO_ENTRADA, PRECO_SAIDA, PRECO_CACHE = 2.0, 8.0, 0.5  # US$ por 1 mi de tokens
CHAVE_SECRETA = "sk-proj-VALIDACAO0123456789abcdef"
CARTAO = "4111 1111 1111 1111"


def _iso(t):
    return datetime.fromtimestamp(t, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


def _valor(v):
    if isinstance(v, bool):
        return {"boolValue": v}
    if isinstance(v, int):
        return {"intValue": str(v)}
    if isinstance(v, float):
        return {"doubleValue": v}
    if isinstance(v, list):
        return {"arrayValue": {"values": [_valor(x) for x in v]}}
    return {"stringValue": str(v)}


def _span(trace, span, pai, nome, inicio, dur_ms, attrs, erro=False):
    return {"traceId": trace, "spanId": span, "parentSpanId": pai, "name": nome, "kind": 3,
            "startTimeUnixNano": str(int(inicio * 1e9)), "endTimeUnixNano": str(int((inicio + dur_ms / 1000) * 1e9)),
            "attributes": [{"key": k, "value": _valor(v)} for k, v in attrs.items()],
            "status": {"code": 2 if erro else 1}}


class Ctx:
    def __init__(self, res, painel, args, run):
        self.res, self.painel, self.run = res, painel, run
        self.gateway = args.gateway.rstrip("/")
        self.chave = args.chave_ingestao
        self.host = args.host
        self.service = f"validacao-ia-{run}"
        self.modelo = f"val-modelo-{run}"
        self.t0 = time.time() - 60
        self.preco_id = None
        self.seq = 0

    def novo_id(self, n):
        self.seq += 1
        return (f"{self.run}{self.seq:04d}" + "a" * 32)[:n]

    def enviar(self, spans):
        corpo = {"resourceSpans": [{"resource": {"attributes": [
            {"key": "service.name", "value": {"stringValue": self.service}},
            {"key": "host.name", "value": {"stringValue": self.host}}]},
            "scopeSpans": [{"scope": {"name": "validacao"}, "spans": spans}]}]}
        r = urllib.request.Request(self.gateway + "/v1/traces", data=json.dumps(corpo).encode(), method="POST")
        r.add_header("Content-Type", "application/json")
        r.add_header("X-Revoada-Key", self.chave)
        with urllib.request.urlopen(r, timeout=30) as resp:
            return resp.status

    def resumo(self, **extra):
        return self.painel.get("/api/ia/resumo", service=self.service, **{"from": _iso(self.t0), "to": _iso(time.time() + 60)}, **extra)

    def esperar(self, condicao, timeout=60):
        fim, ultimo = time.time() + timeout, None
        while time.time() < fim:
            try:
                ultimo = condicao()
                if ultimo:
                    return ultimo
            except RuntimeError:
                pass
            time.sleep(2)
        return ultimo


def _cenario(c):
    """Execução principal: agente, 3 chamadas com tokens (uma com cache, uma com erro),
    uma sem tokens, uma num modelo sem preço, e a ferramenta 'clima' 3 vezes seguidas."""
    trace, agora = c.novo_id(32), time.time() - 30
    raiz = c.novo_id(16)
    chat = {"gen_ai.operation.name": "chat", "gen_ai.provider.name": "openai", "gen_ai.request.model": c.modelo}
    spans = [_span(trace, raiz, "", "invoke_agent Validador", agora, 9000,
                   {"gen_ai.operation.name": "invoke_agent", "gen_ai.agent.name": f"Validador-{c.run}",
                    "gen_ai.conversation.id": f"conv-{c.run}"})]
    tokens = [(1000, 200, 400, False), (3000, 100, 0, False), (500, 50, 0, True)]
    for i, (ent, sai, cache, erro) in enumerate(tokens):
        a = dict(chat, **{"gen_ai.usage.input_tokens": ent, "gen_ai.usage.output_tokens": sai,
                          "gen_ai.response.finish_reasons": ["stop"]})
        if cache:
            a["gen_ai.usage.cache_read.input_tokens"] = cache
        if erro:
            a["error.type"] = "RateLimitError"
        if i == 0:
            a["gen_ai.input.messages"] = json.dumps([{"role": "user", "parts": [
                {"type": "text", "content": f"minha chave é {CHAVE_SECRETA} e o cartão {CARTAO}"}]}])
        spans.append(_span(trace, c.novo_id(16), raiz, "chat", agora + 1 + i * 2, 800 + i * 100, a, erro))
        spans.append(_span(trace, c.novo_id(16), raiz, "execute_tool clima", agora + 2 + i * 2, 50,
                           {"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "clima"}))
    spans.append(_span(trace, c.novo_id(16), raiz, "chat", agora + 8, 300, dict(chat)))  # sem tokens
    spans.append(_span(trace, c.novo_id(16), raiz, "chat", agora + 8.5, 300,
                       dict(chat, **{"gen_ai.request.model": f"sem-preco-{c.run}", "gen_ai.usage.input_tokens": 10,
                                     "gen_ai.usage.output_tokens": 1})))
    custo = sum((e - ca) * PRECO_ENTRADA + ca * PRECO_CACHE + s * PRECO_SAIDA for e, s, ca, _ in tokens) / 1e6
    return trace, spans, custo


def caso_resumo_e_custo(c, trace, custo):
    r = c.esperar(lambda: (lambda x: x if x["totais"]["chamadas"] >= 5 else None)(c.resumo()))
    t = (r or {}).get("totais", {})
    c.res.add(G, "Chamadas de modelo", 5, t.get("chamadas"), "exato", t.get("chamadas") == 5)
    c.res.add(G, "Chamadas com erro", 1, t.get("erros"), "exato", t.get("erros") == 1)
    c.res.add(G, "Tokens de entrada (cache incluso)", 4510, t.get("tokens_entrada"), "exato", t.get("tokens_entrada") == 4510)
    c.res.add(G, "Tokens de saída", 351, t.get("tokens_saida"), "exato", t.get("tokens_saida") == 351)
    obs = t.get("custo_usd")
    c.res.add(G, "Custo estimado (preço da rodada)", f"US$ {custo:.6f}", f"US$ {obs}" if obs is not None else "null",
              "1e-9", obs is not None and abs(obs - custo) < 1e-9)
    c.res.add(G, "Custo marcado como parcial", True, t.get("custo_parcial"), "exato", t.get("custo_parcial") is True,
              "há uma chamada sem tokens e uma sem preço: o total não pode se dizer completo")
    c.res.add(G, "Chamada sem preço contada (não vira US$ 0)", 1, t.get("chamadas_sem_preco"), "exato",
              t.get("chamadas_sem_preco") == 1)
    c.res.add(G, "Chamada sem tokens contada", 1, t.get("sem_tokens"), "exato", t.get("sem_tokens") == 1)
    c.res.add(G, "Ferramentas", "3 chamadas", t.get("ferramentas_chamadas"), "exato", t.get("ferramentas_chamadas") == 3)
    ag = [a for a in (r or {}).get("por_agente", []) if a.get("agente") == f"Validador-{c.run}"]
    c.res.add(G, "Custo atribuído ao agente", f"US$ {custo:.6f}", ag[0].get("custo_usd") if ag else "ausente", "1e-9",
              bool(ag) and ag[0].get("custo_usd") is not None and abs(ag[0]["custo_usd"] - custo) < 1e-9,
              "a chamada ao modelo não traz o nome do agente; ele vem do span invoke_agent do mesmo trace")


def caso_replay(c, trace):
    r = c.painel.get(f"/api/ia/execucoes/{trace}")
    passos = r.get("passos", [])
    c.res.add(G, "Replay: passos na ordem", 9, len(passos), "exato", len(passos) == 9)
    rep = r.get("repeticoes", [])
    c.res.add(G, "Replay: loop de ferramenta apontado", "clima ×3", f"{rep[0]['ferramenta']} ×{rep[0]['vezes']}" if rep else "nenhum",
              "exato", bool(rep) and rep[0]["ferramenta"] == "clima" and rep[0]["vezes"] == 3)
    origens = sorted({p.get("custo_origem") for p in passos if p.get("operacao") == "chat"})
    c.res.add(G, "Replay: origem do custo por passo", "estimado, sem_preco, sem_tokens", ", ".join(origens), "exato",
              origens == ["estimado", "sem_preco", "sem_tokens"])
    lista = c.painel.get("/api/ia/execucoes", service=c.service, **{"from": _iso(c.t0)}).get("execucoes", [])
    c.res.add(G, "Lista de execuções", f"agente Validador-{c.run}", lista[0].get("agente") if lista else "vazia", "exato",
              bool(lista) and lista[0].get("agente") == f"Validador-{c.run}")


def caso_conteudo(c, trace):
    spans = c.painel.get(f"/api/traces/{trace}").get("spans", [])
    vazou = [k for s in spans for k, v in (s.get("labels") or {}).items()
             if k == "gen_ai.input.messages" or CHAVE_SECRETA in str(v) or CARTAO in str(v)]
    c.res.add(G, "Prompt fora dos rótulos do span", "nenhuma chave de conteúdo", vazou or "nenhuma", "exato", not vazou,
              "spans.labels fica 15 dias e todo leitor vê")
    try:
        ms = c.painel.get(f"/api/ia/execucoes/{trace}/conteudo").get("mensagens", [])
    except RuntimeError as e:
        modo = "desligado" if "404" in str(e) else f"erro: {e}"
        c.res.add(G, "Conteúdo gravado", "conforme REVOADA_GENAI_CONTEUDO", modo, "-", modo == "desligado",
                  "com o modo desligado (padrão) nenhum texto é gravado")
        return
    texto = " ".join(m.get("texto", "") for m in ms)
    redigido = CHAVE_SECRETA not in texto and CARTAO not in texto
    c.res.add(G, "Conteúdo gravado sem a chave e o cartão", "redigido", "redigido" if redigido else "em claro", "-", redigido,
              "se o gateway está em modo completo, isto falha de propósito: o texto foi gravado como chegou")


def caso_amostragem(c):
    """20 traces rápidos e sem erro — o tipo que a amostragem descartaria — têm de chegar todos."""
    traces = []
    for i in range(20):
        t = c.novo_id(32)
        traces.append(t)
        c.enviar([_span(t, c.novo_id(16), "", "chat", time.time() - 5, 20,
                        {"gen_ai.operation.name": "chat", "gen_ai.provider.name": "openai", "gen_ai.request.model": c.modelo,
                         "gen_ai.usage.input_tokens": 1, "gen_ai.usage.output_tokens": 1, "gen_ai.conversation.id": f"amostra-{c.run}"})])
    def contar():
        r = c.painel.get("/api/ia/execucoes", service=c.service, conversa_id=f"amostra-{c.run}", limit=100,
                         **{"from": _iso(c.t0)})
        n = len(r.get("execucoes", []))
        return n if n >= 20 else None
    n = c.esperar(contar) or 0
    c.res.add(G, "Traces de IA não são amostrados", 20, n, "exato", n == 20,
              "custo é soma: amostrar 20% daria um gasto cinco vezes menor")


def caso_muitos_rotulos(c):
    """Span no formato do OpenInference com 90 chaves de mensagem: antes ele estourava o
    teto de 64 rótulos e era recusado inteiro."""
    t = c.novo_id(32)
    a = {"openinference.span.kind": "LLM", "llm.model_name": c.modelo, "llm.system": "openai",
         "llm.token_count.prompt": 10, "llm.token_count.completion": 2}
    for i in range(45):
        a[f"llm.input_messages.{i}.message.role"] = "user"
        a[f"llm.input_messages.{i}.message.content"] = f"mensagem {i}"
    c.enviar([_span(t, c.novo_id(16), "", "ChatCompletion", time.time() - 5, 30, a)])
    ok = c.esperar(lambda: c.painel.get(f"/api/ia/execucoes/{t}"), timeout=40)
    c.res.add(G, "Span com 90+ atributos de mensagem aceito", "aceito", "aceito" if ok else "recusado", "-", bool(ok))


def caso_serie_de_alerta(c, timeout):
    """O runner fecha cada minuto com 5 min de atraso e grava llm.chamadas em metrics."""
    def soma():
        r = c.painel.req("POST", "/api/query", {"metric": "llm.chamadas", "filters": {"service": c.service},
                                               "from": _iso(c.t0), "to": _iso(time.time()), "step": 60, "agg": "sum"})
        total = sum(v or 0 for s in (r.get("series") or []) for v in (s.get("values") or []))
        return total if total >= 25 else None
    total = c.esperar(soma, timeout=timeout) or 0
    c.res.add(G, "Série llm.chamadas para alertas", "≥ 25 (5 + 20)", total, "chamadas do teste", total >= 25,
              "gravada com 5 min de atraso; se o tempo do teste foi curto, o veredito é este mesmo número")


def preparar_preco(c):
    p = c.painel.req("POST", "/api/ia/precos", {"provedor": "openai", "modelo": c.modelo, "entrada_por_1m": PRECO_ENTRADA,
                                                "saida_por_1m": PRECO_SAIDA, "cache_leitura_por_1m": PRECO_CACHE,
                                                "vigente_desde": _iso(time.time() - 86400)})
    c.preco_id = p["id"]


def limpar(c):
    if c.preco_id:
        try:
            c.painel.req("DELETE", f"/api/ia/precos/{c.preco_id}")
        except RuntimeError:
            pass


def executar(res, painel, args, run):
    print("\n== ia ==", flush=True)
    c = Ctx(res, painel, args, run)
    try:
        preparar_preco(c)
        trace, spans, custo = _cenario(c)
        c.enviar(spans)
        for caso, a in ((caso_resumo_e_custo, (trace, custo)), (caso_replay, (trace,)), (caso_conteudo, (trace,)),
                        (caso_amostragem, ()), (caso_muitos_rotulos, ()), (caso_serie_de_alerta, (args.duracao_ia,))):
            try:
                caso(c, *a)
            except Exception as e:  # noqa: BLE001 — um caso quebrado não derruba os outros
                res.add(G, caso.__name__, "executar", f"erro: {e}", "-", False)
    except (urllib.error.URLError, RuntimeError, KeyError) as e:
        res.add(G, "preparar", "gateway e painel no ar", f"erro: {e}", "-", False)
    finally:
        limpar(c)

