"""Grupo ia: chamadas de agentes de IA (tela Agentes de IA).

Envia ao gateway, por OTLP/JSON, spans de IA montados à mão com números conhecidos e
confere o que as MESMAS rotas da tela devolvem:

  - visão geral          -> GET /api/ia/resumo
  - lista de execuções   -> GET /api/ia/execucoes
  - replay               -> GET /api/ia/execucoes/{trace}
  - conteúdo             -> GET /api/ia/execucoes/{trace}/conteudo (permissão CRÍTICA: 2FA + reautenticação)
  - waterfall (rótulos)  -> GET /api/traces/{trace}
  - séries de alerta     -> POST /api/query (llm.chamadas, gravada pelo runner com 5 min de atraso)
  - papéis               -> usuários descartáveis leitor/operador criados e apagados pela rodada
  - MCP                  -> POST /mcp com tokens descartáveis (escopos leitura e ia_conteudo)
  - troca de preço       -> segunda linha de preço com vigência posterior (o passado não muda)
  - purge (LGPD)         -> POST /api/ia/purge por trace e por conversa (nunca todo_conteudo)

O preço usado é uma linha criada pelo próprio teste (modelo exclusivo da rodada, preço
redondo), então o custo esperado é conta de padaria — e a linha é apagada no fim.
A verdade aqui é o que foi ENVIADO: cada token, cada erro e cada ferramenta.

Senhas, segredos TOTP e tokens MCP dos descartáveis são gerados em tempo de execução,
ficam só na memória deste processo e nunca são impressos nem gravados.
"""
import json
import secrets
import time
import urllib.error
import urllib.parse
import urllib.request
from datetime import datetime, timezone

import cliente

G = "ia"
PRECO_ENTRADA, PRECO_SAIDA, PRECO_CACHE = 2.0, 8.0, 0.5  # US$ por 1 mi de tokens
PRECO_NOVO_ENTRADA, PRECO_NOVO_SAIDA, PRECO_NOVO_CACHE = 3.0, 12.0, 1.0  # a "troca de preço"
TOKENS_TROCA = (1000, 100)  # entrada, saída da chamada feita depois da troca
CHAVE_SECRETA = "sk-teste-validacao-0123456789"
CARTAO = "4111 1111 1111 1111"
FERRAMENTAS_IA = ("ia_resumo", "listar_execucoes", "ver_execucao", "ler_conteudo_execucao")
TELEFONE_FICTICIO = "(00) 0000-0000"  # DDD 00 não existe; o painel exige celular no cadastro
DOMINIO_DESCARTAVEL = "validacao.invalid"  # TLD reservado (RFC 2606): nenhum e-mail sai daqui
PRECO_INEXISTENTE = 9_000_000_000_000_000  # id para o DELETE negado: se a guarda falhar, nada some
MARGEM_REAUTH = 45  # s antes do fim da janela de 5 min em que o admin reautentica de novo
TIMEOUT_PURGE = 60  # s: as mutations do ClickHouse são assíncronas
TOL_CUSTO = 1e-9


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


def _custo(entrada, saida, cache, pe, ps, pc):
    """Mesma conta de core/genai.Custo: tokens de entrada já incluem os de cache."""
    return ((entrada - cache) * pe + cache * pc + saida * ps) / 1e6


def _codigo(r):
    return r.get("codigo") if isinstance(r, dict) else None


class Ctx:
    def __init__(self, res, painel, args, run):
        self.res, self.painel, self.run = res, painel, run
        self.base = args.painel
        self.gateway = args.gateway.rstrip("/")
        self.chave = args.chave_ingestao
        self.host = args.host
        self.service = f"validacao-ia-{run}"
        self.modelo = f"val-modelo-{run}"
        self.t0 = time.time() - 60
        self.preco_id = None
        self.precos_extra = []
        self.seq = 0
        # ids únicos por rodada: só a hora (HHMMSS) repetiria noutro dia, e o replay não tem
        # filtro de tempo — o trace de ontem às mesmas horas entraria nos 9 passos.
        self.prefixo = f"{run}{secrets.token_hex(3)}"
        self.reauth_ate = 0.0
        self.chamadas_modelo = []  # (ts, entrada, saída, cache) de cada chamada a self.modelo; entrada None = sem tokens
        self.traces_amostra = []
        self.spans_cenario = 0
        self.conteudo_modo = None  # "gravado" | "desligado" | None (não deu para saber)

    def novo_id(self, n):
        self.seq += 1
        return (f"{self.prefixo}{self.seq:04d}" + "a" * 32)[:n]

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

    def critico_bruto(self, metodo, caminho, corpo=None):
        """Rota com permissão crítica (auth.Exige: 2FA + reautenticação nos últimos 5 min)
        chamada pelo admin. Reautentica quando a janela está no fim e, se o servidor ainda
        pedir, reautentica e tenta UMA vez mais. Devolve (status, corpo)."""
        if time.time() > self.reauth_ate - MARGEM_REAUTH:
            self._reautenticar()
        st, r = self.painel.bruto(metodo, caminho, corpo)
        if st == 403 and _codigo(r) == "reautenticacao_necessaria":  # ex.: um 401 forçou novo login
            self._reautenticar()
            st, r = self.painel.bruto(metodo, caminho, corpo)
        if st == 403 and _codigo(r) == "mfa_necessario":
            raise RuntimeError("a sessão do admin não tem 2FA: as rotas críticas exigem 2FA ativo na conta "
                               "(ative em Minha conta e passe o segredo em RV_TOTP/--totp-arquivo)")
        return st, r

    def critico(self, metodo, caminho, corpo=None, ok=(200, 201, 202, 204)):
        st, r = self.critico_bruto(metodo, caminho, corpo)
        if st not in ok:
            raise RuntimeError(f"{metodo} {caminho} -> {st}: {str(r)[:300]}")
        return r

    def _reautenticar(self):
        ate = self.painel.reautenticar()
        self.reauth_ate = float(ate) if ate else time.time() + 290


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
    chamadas = []
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
        chamadas.append((agora + 1 + i * 2, ent, sai, cache))
        spans.append(_span(trace, c.novo_id(16), raiz, "execute_tool clima", agora + 2 + i * 2, 50,
                           {"gen_ai.operation.name": "execute_tool", "gen_ai.tool.name": "clima"}))
    spans.append(_span(trace, c.novo_id(16), raiz, "chat", agora + 8, 300, dict(chat)))  # sem tokens
    chamadas.append((agora + 8, None, None, 0))
    spans.append(_span(trace, c.novo_id(16), raiz, "chat", agora + 8.5, 300,
                       dict(chat, **{"gen_ai.request.model": f"sem-preco-{c.run}", "gen_ai.usage.input_tokens": 10,
                                     "gen_ai.usage.output_tokens": 1})))
    custo = sum(_custo(e, s, ca, PRECO_ENTRADA, PRECO_SAIDA, PRECO_CACHE) for e, s, ca, _ in tokens)
    return trace, spans, custo, chamadas


def caso_resumo_e_custo(c, trace, custo):
    r = c.esperar(lambda: (lambda x: x if x["totais"]["chamadas"] >= 5 else None)(c.resumo()))
    t = (r or {}).get("totais", {})
    c.res.add(G, "Chamadas de modelo", 5, t.get("chamadas"), "exato", t.get("chamadas") == 5)
    c.res.add(G, "Chamadas com erro", 1, t.get("erros"), "exato", t.get("erros") == 1)
    c.res.add(G, "Tokens de entrada (cache incluso)", 4510, t.get("tokens_entrada"), "exato", t.get("tokens_entrada") == 4510)
    c.res.add(G, "Tokens de saída", 351, t.get("tokens_saida"), "exato", t.get("tokens_saida") == 351)
    obs = t.get("custo_usd")
    c.res.add(G, "Custo estimado (preço da rodada)", f"US$ {custo:.6f}", f"US$ {obs}" if obs is not None else "null",
              "1e-9", obs is not None and abs(obs - custo) < TOL_CUSTO)
    c.res.add(G, "Custo marcado como parcial", True, t.get("custo_parcial"), "exato", t.get("custo_parcial") is True,
              "há uma chamada sem tokens e uma sem preço: o total não pode se dizer completo")
    c.res.add(G, "Chamada sem preço contada (não vira US$ 0)", 1, t.get("chamadas_sem_preco"), "exato",
              t.get("chamadas_sem_preco") == 1)
    c.res.add(G, "Chamada sem tokens contada", 1, t.get("sem_tokens"), "exato", t.get("sem_tokens") == 1)
    c.res.add(G, "Ferramentas", "3 chamadas", t.get("ferramentas_chamadas"), "exato", t.get("ferramentas_chamadas") == 3)
    ag = [a for a in (r or {}).get("por_agente", []) if a.get("agente") == f"Validador-{c.run}"]
    c.res.add(G, "Custo atribuído ao agente", f"US$ {custo:.6f}", ag[0].get("custo_usd") if ag else "ausente", "1e-9",
              bool(ag) and ag[0].get("custo_usd") is not None and abs(ag[0]["custo_usd"] - custo) < TOL_CUSTO,
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
    # ver_conteudo_ia é permissão CRÍTICA (auth/papeis.go): até o admin precisa de 2FA e
    # de reautenticação recente — sem ela a rota responde 403 reautenticacao_necessaria.
    st, r = c.critico_bruto("GET", f"/api/ia/execucoes/{trace}/conteudo")
    if st == 404:
        c.conteudo_modo = "desligado"
        c.res.add(G, "Conteúdo gravado", "conforme REVOADA_GENAI_CONTEUDO", "desligado", "-", True,
                  "com o modo desligado (padrão) nenhum texto é gravado")
        return
    if st != 200 or not isinstance(r, dict):
        c.res.add(G, "Conteúdo gravado", "200 ou 404", f"{st}: {str(r)[:160]}", "-", False)
        return
    c.conteudo_modo = "gravado"
    texto = " ".join(m.get("texto", "") for m in r.get("mensagens", []))
    redigido = CHAVE_SECRETA not in texto and CARTAO not in texto
    c.res.add(G, "Conteúdo gravado sem a chave e o cartão", "redigido", "redigido" if redigido else "em claro", "-", redigido,
              "se o gateway está em modo completo, isto falha de propósito: o texto foi gravado como chegou")


def caso_amostragem(c):
    """20 traces rápidos e sem erro — o tipo que a amostragem descartaria — têm de chegar todos."""
    for i in range(20):
        t, ts = c.novo_id(32), time.time() - 5
        c.enviar([_span(t, c.novo_id(16), "", "chat", ts, 20,
                        {"gen_ai.operation.name": "chat", "gen_ai.provider.name": "openai", "gen_ai.request.model": c.modelo,
                         "gen_ai.usage.input_tokens": 1, "gen_ai.usage.output_tokens": 1, "gen_ai.conversation.id": f"amostra-{c.run}"})])
        c.traces_amostra.append(t)
        c.chamadas_modelo.append((ts, 1, 1, 0))

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
    teto de 64 rótulos e era recusado inteiro. Além de aceito, tem de ser LIDO: modelo em
    llm.model_name e tokens em llm.token_count.* (core/genai/genai.go)."""
    t, ts = c.novo_id(32), time.time() - 5
    a = {"openinference.span.kind": "LLM", "llm.model_name": c.modelo, "llm.system": "openai",
         "llm.token_count.prompt": 10, "llm.token_count.completion": 2}
    for i in range(45):
        a[f"llm.input_messages.{i}.message.role"] = "user"
        a[f"llm.input_messages.{i}.message.content"] = f"mensagem {i}"
    c.enviar([_span(t, c.novo_id(16), "", "ChatCompletion", ts, 30, a)])
    c.chamadas_modelo.append((ts, 10, 2, 0))
    rep = c.esperar(lambda: c.painel.get(f"/api/ia/execucoes/{t}"), timeout=40)
    c.res.add(G, "Span com 90+ atributos de mensagem aceito", "aceito", "aceito" if rep else "recusado", "-", bool(rep))
    passos = (rep or {}).get("passos") or []
    c.res.add(G, "OpenInference: replay com passos", "≥ 1", len(passos), "-", len(passos) >= 1,
              "aceito e não lido seria pior que recusado: a chamada some do custo sem aviso")
    p = passos[0] if passos else {}
    c.res.add(G, "OpenInference: operação (span.kind=LLM)", "chat", p.get("operacao"), "exato", p.get("operacao") == "chat")
    c.res.add(G, "OpenInference: modelo de llm.model_name", c.modelo, p.get("modelo"), "exato", p.get("modelo") == c.modelo)
    c.res.add(G, "OpenInference: tokens_entrada de llm.token_count.prompt", 10, p.get("tokens_entrada"), "exato",
              p.get("tokens_entrada") == 10)
    c.res.add(G, "OpenInference: tokens_saida de llm.token_count.completion", 2, p.get("tokens_saida"), "exato",
              p.get("tokens_saida") == 2)


# ------------------------------------------------------------------ papéis (usuários descartáveis)

def _rotas_de_admin(c):
    """Rotas de escrita de IA que só o admin pode chamar (httpapi.go: wrapper admin()).
    Os alvos são inócuos: se a guarda falhar, o DELETE mira um id que não existe e o purge
    um trace aleatório — o erro aparece no status sem apagar nada de ninguém."""
    preco = {"provedor": "openai", "modelo": f"val-negado-{c.run}", "entrada_por_1m": 1.0, "saida_por_1m": 1.0}
    return [("POST /api/ia/precos", "POST", "/api/ia/precos", preco),
            ("DELETE /api/ia/precos/{id}", "DELETE", f"/api/ia/precos/{PRECO_INEXISTENTE}", None),
            ("POST /api/ia/purge", "POST", "/api/ia/purge", {"alvo": "trace", "valor": secrets.token_hex(16)})]


def _negadas(c, papel, sessao):
    for nome, metodo, caminho, corpo in _rotas_de_admin(c):
        st, r = sessao.bruto(metodo, caminho, corpo)
        if st == 201 and isinstance(r, dict) and r.get("id"):
            c.precos_extra.append(r["id"])  # a guarda falhou e criou o preço: some no limpar()
        c.res.add(G, f"Permissões: {papel} → {nome}", 403, st, "exato", st == 403,
                  f"só o admin escreve preço e apaga dado de IA; resposta: {str(r)[:120]}")


def _criar_descartavel(c, papel, host, criados):
    """Cria o usuário pela API do admin (POST /api/users, crítica), dá VER no servidor do
    trace (PUT /api/users/{id}/perms; sem isso o escopo zera tudo) e abre a sessão dele."""
    cred = cliente.Credenciais(f"val-{c.run}-{papel}-{secrets.token_hex(3)}@{DOMINIO_DESCARTAVEL}",
                               secrets.token_urlsafe(24), None, None)  # senha só na memória
    u = c.critico("POST", "/api/users", {"full_name": f"Validação {c.run} ({papel})", "email": cred.email,
                                         "phone": TELEFONE_FICTICIO, "password": cred.senha, "role": papel})
    criados.append((u["id"], cred.email))
    c.res.add(G, f"Permissões: {papel} descartável criado", papel, u.get("role"), "exato", u.get("role") == papel)
    c.critico("PUT", f"/api/users/{u['id']}/perms",
              {"direct": [{"hostname": host, "can_view": True, "can_edit": False, "notify": False}], "groups": []})
    return cliente.Painel(c.base, cred).entrar(), cred


def _leitor(c, trace, leitor):
    st, r = leitor.bruto("GET", f"/api/ia/execucoes/{trace}/conteudo")
    c.res.add(G, "Permissões: leitor → conteúdo da execução", "403 sem_permissao", f"{st} {_codigo(r) or ''}".strip(),
              "exato", st == 403 and _codigo(r) == "sem_permissao",
              "leitor vê custo, tokens e erro, mas não o prompt (dado pessoal)")
    _negadas(c, "leitor", leitor)
    janela = {"service": c.service, "from": _iso(c.t0), "to": _iso(time.time() + 60)}
    adm = c.painel.get("/api/ia/resumo", **janela).get("totais", {})
    st, r = leitor.bruto("GET", "/api/ia/resumo?" + urllib.parse.urlencode(janela))
    c.res.add(G, "Permissões: leitor lê /api/ia/resumo", 200, st, "exato", st == 200)
    if st == 200 and isinstance(r, dict):
        obs = r.get("totais", {}).get("chamadas")
        c.res.add(G, "Permissões: resumo do leitor = do admin (servidor concedido)", adm.get("chamadas"), obs, "exato",
                  obs == adm.get("chamadas"),
                  "o leitor recebeu VER no servidor do trace; sem isso o escopo vira 1=0 e o resumo zera")


def _operador(c, trace, operador, cred):
    rota = f"/api/ia/execucoes/{trace}/conteudo"
    st, r = operador.bruto("GET", rota)
    c.res.add(G, "Permissões: operador sem 2FA → conteúdo", "403 mfa_necessario", f"{st} {_codigo(r) or ''}".strip(),
              "exato", st == 403 and _codigo(r) == "mfa_necessario", "ver_conteudo_ia é crítica: operador precisa de 2FA")
    st, ini = operador.bruto("POST", "/api/auth/mfa/iniciar")
    if st != 200 or not isinstance(ini, dict) or not ini.get("segredo"):
        c.res.add(G, "Permissões: operador ativa o 2FA", 200, st, "-", None,
                  "sem 2FA não dá para provar o caminho do operador (cofre de segredos indisponível?)")
        _negadas(c, "operador", operador)
        return
    cred.segredo_totp = ini["segredo"]  # como o app do celular: só na memória, nunca impresso
    operador.req("POST", "/api/auth/mfa/confirmar", lambda: {"codigo": cred.codigo()})
    st, r = operador.bruto("GET", rota)
    c.res.add(G, "Permissões: operador com 2FA, sem reautenticar → conteúdo", "403 reautenticacao_necessaria",
              f"{st} {_codigo(r) or ''}".strip(), "exato", st == 403 and _codigo(r) == "reautenticacao_necessaria",
              "2FA na sessão não basta: o prompt pede identidade confirmada nos últimos 5 min")
    operador.reautenticar()
    st, r = operador.bruto("GET", rota)
    aceitos = {"gravado": {200}, "desligado": {404}}.get(c.conteudo_modo, {200, 404})
    modo = "gravado (200)" if st == 200 else "desligado (404)" if st == 404 else f"{st}: {str(r)[:120]}"
    c.res.add(G, "Permissões: operador reautenticado → conteúdo", " ou ".join(map(str, sorted(aceitos))), modo, "exato",
              st in aceitos,
              "404 = o gateway não grava conteúdo (REVOADA_GENAI_CONTEUDO desligado); o admin viu o mesmo modo")
    _negadas(c, "operador", operador)


def _apagar_descartaveis(c, criados):
    falhas = []
    for uid, email in criados:
        try:
            c.critico("DELETE", f"/api/users/{uid}")
        except Exception as e:  # noqa: BLE001 — a limpeza tenta todos e relata cada falha
            falhas.append(f"{email} (id {uid}): {e}")
    try:
        emails = {u.get("email") for u in c.painel.get("/api/users").get("users", [])}
        sobrou = [e for _, e in criados if e in emails]
    except RuntimeError as e:
        sobrou = [f"não deu para listar: {e}"]
    c.res.add(G, "Limpeza: usuários descartáveis apagados", f"{len(criados)} apagados, 0 sobrando",
              f"{len(criados) - len(falhas)} apagados, sobrando: {sobrou or 'nenhum'}", "exato", not falhas and not sobrou,
              "apague à mão em Usuários & Acessos: " + "; ".join(falhas or sobrou))


def caso_permissoes(c, trace):
    """Leitor e operador descartáveis, com o servidor do trace no escopo: o leitor não lê
    conteúdo nem escreve; o operador lê conteúdo só depois de 2FA + reautenticação e
    também não escreve preço nem apaga. Os usuários são apagados no fim, mesmo em erro."""
    host = c.painel.get(f"/api/ia/execucoes/{trace}").get("host") or c.host  # o host que o gateway gravou
    criados = []
    try:
        leitor, _ = _criar_descartavel(c, "leitor", host, criados)
        _leitor(c, trace, leitor)
        operador, cred = _criar_descartavel(c, "operador", host, criados)
        _operador(c, trace, operador, cred)
    finally:
        if criados:
            _apagar_descartaveis(c, criados)


# ------------------------------------------------------------------ MCP

def _por_modelo(resumo, modelo):
    ms = [m for m in (resumo or {}).get("por_modelo") or [] if m.get("modelo") == modelo]
    if not ms:
        return None
    soma = {k: sum(m.get(k) or 0 for m in ms) for k in ("chamadas", "erros", "tokens_entrada", "tokens_saida")}
    custos = [m.get("custo_usd") for m in ms if m.get("custo_usd") is not None]
    soma["custo_usd"] = sum(custos) if custos else None
    soma["preco"] = ms[0].get("preco")
    return soma


def _mcp_resumo_igual_rest(c, sessao):
    ok, dados, texto = sessao.ferramenta("ia_resumo", {"horas": 1})
    agora = time.time()
    rest = c.painel.get("/api/ia/resumo", **{"from": _iso(agora - 3600), "to": _iso(agora)})
    a, b = _por_modelo(dados if ok else None, c.modelo), _por_modelo(rest, c.modelo)
    if not a or not b:
        c.res.add(G, "MCP: ia_resumo = /api/ia/resumo", "modelo da rodada nos dois", f"mcp={bool(a)} rest={bool(b)}", "-",
                  False, texto[:200] if not ok else "")
        return
    for campo in ("chamadas", "erros", "tokens_entrada", "tokens_saida"):
        c.res.add(G, f"MCP: ia_resumo = /api/ia/resumo ({campo})", b[campo], a[campo], "exato", a[campo] == b[campo],
                  "mesma janela (1 h até agora) e mesmo passo de 60 s; comparado no modelo exclusivo da rodada "
                  "porque o ia_resumo do MCP não filtra service e o total da stack mexe entre as duas chamadas")
    ca, cb = a["custo_usd"], b["custo_usd"]
    c.res.add(G, "MCP: ia_resumo = /api/ia/resumo (custo do modelo)", cb, ca, "1e-9",
              ca is not None and cb is not None and abs(ca - cb) < TOL_CUSTO)


def _mcp_casos(c, trace, leitura, conteudo):
    m1 = cliente.MCP(c.base, leitura)
    m1.iniciar()
    nomes = m1.ferramentas()
    faltam = [n for n in FERRAMENTAS_IA if n not in nomes]
    c.res.add(G, "MCP: as 4 ferramentas de IA listadas", ", ".join(FERRAMENTAS_IA), f"faltam: {faltam}" if faltam else "todas",
              "exato", not faltam)
    _mcp_resumo_igual_rest(c, m1)
    ok, dados, texto = m1.ferramenta("ver_execucao", {"trace_id": trace})
    passos = len((dados or {}).get("passos") or []) if ok else f"erro: {texto[:120]}"
    c.res.add(G, "MCP: ver_execucao devolve os passos", 9, passos, "exato", passos == 9)
    ok, _, texto = m1.ferramenta("ler_conteudo_execucao", {"trace_id": trace})
    c.res.add(G, "MCP: ler_conteudo_execucao com token só 'leitura'", "negado (escopo)", "negado" if not ok else "PERMITIDO",
              "exato", not ok and "escopo" in texto, "o texto das conversas pede o escopo ia_conteudo de propósito")
    m1.encerrar()
    m2 = cliente.MCP(c.base, conteudo)
    m2.iniciar()
    ok, dados, texto = m2.ferramenta("ler_conteudo_execucao", {"trace_id": trace})
    total = (dados or {}).get("total") if ok else None
    esperado = {"gravado": "permitido, > 0 mensagens", "desligado": "permitido, 0 mensagens"}.get(c.conteudo_modo, "permitido")
    if not ok:
        bate = False
    elif c.conteudo_modo == "gravado":
        bate = (total or 0) > 0
    elif c.conteudo_modo == "desligado":
        bate = total == 0
    else:
        bate = True  # o admin não conseguiu ver o modo: basta o escopo liberar
    c.res.add(G, "MCP: ler_conteudo_execucao com token 'ia_conteudo'", esperado,
              f"permitido, {total} mensagens" if ok else f"negado: {texto[:120]}", "-", bate,
              "o número de mensagens segue o modo de gravação que a tela mostrou ao admin")
    m2.encerrar()


def caso_mcp(c, trace):
    """Dois tokens MCP descartáveis (leitura; leitura + ia_conteudo), criados e revogados
    pela API do admin (crítica: 2FA + reautenticação). Fala MCP Streamable HTTP no /mcp."""
    tokens = []  # (id, valor) — o valor aparece UMA vez, na criação, e fica só na memória
    try:
        for nome, escopos in (("leitura", ["leitura"]), ("ia-conteudo", ["leitura", "ia_conteudo"])):
            t = c.critico("POST", "/api/mcp/tokens", {"nome": f"val-{c.run}-{nome}", "escopos": escopos, "validade_dias": 1})
            tokens.append((t["id"], t["token"]))
            c.res.add(G, f"MCP: token '{nome}' criado com os escopos pedidos", escopos, t.get("escopos"), "exato",
                      sorted(t.get("escopos") or []) == sorted(escopos))
        _mcp_casos(c, trace, tokens[0][1], tokens[1][1])
    finally:
        revogados = []
        for tid, _ in tokens:
            try:
                c.critico("POST", f"/api/mcp/tokens/{tid}/revogar")
                revogados.append(tid)
            except Exception as e:  # noqa: BLE001
                c.res.add(G, "Limpeza: token MCP revogado", tid, f"erro: {e}", "-", False,
                          "revogue à mão em Configurações → MCP")
        if tokens and tokens[0][0] in revogados:
            try:
                cliente.MCP(c.base, tokens[0][1]).iniciar()
                obs = "ainda aceito"
            except cliente.ErroMCP as e:
                obs = f"HTTP {e.http_status}"
            c.res.add(G, "MCP: token revogado deixa de valer", "HTTP 401", obs, "exato", obs == "HTTP 401",
                      "a revogação vale na próxima chamada (tokens.go)")


# ------------------------------------------------------------------ troca de preço

def _resumo_com(c, janela, n):
    """Espera o modelo da rodada ter ≥ n chamadas no resumo; devolve a ÚLTIMA leitura
    (mesmo se o tempo acabou), para o veredito mostrar o que de fato chegou."""
    ultimo = {}

    def pronto():
        ultimo["r"] = c.painel.get("/api/ia/resumo", service=c.service, **janela)
        return (_por_modelo(ultimo["r"], c.modelo) or {}).get("chamadas", 0) >= n
    c.esperar(pronto)
    return ultimo.get("r")


def _esperar_vigencia(c):
    """O custo do resumo é calculado POR INTERVALO (ia/custo.go: o preço vigente no início
    do balde, l.Quando), não por chamada. Para a conta ser exata a nova vigência tem de cair
    numa virada de minuto posterior a todas as chamadas antigas e anterior à nova."""
    ultimo = max(ts for ts, *_ in c.chamadas_modelo)
    vig = time.time() // 60 * 60
    if vig <= ultimo:
        vig = (ultimo // 60 + 1) * 60
    espera = vig + 2 - time.time()
    if espera > 0:
        time.sleep(espera)
    return vig


def caso_troca_de_preco(c):
    """Uma segunda linha de preço para o MESMO modelo, com vigência posterior: o passado
    continua com o preço antigo e só a chamada nova usa o novo (docs/api-ia.md, Preços)."""
    if not c.chamadas_modelo:
        c.res.add(G, "Troca de preço", "chamadas anteriores do modelo", "nenhuma enviada", "-", None)
        return
    antigo = sum(_custo(e, s, ca, PRECO_ENTRADA, PRECO_SAIDA, PRECO_CACHE) for _, e, s, ca in c.chamadas_modelo if e is not None)
    vig = _esperar_vigencia(c)
    janela = {"from": _iso(c.t0), "to": _iso(vig + 600)}
    n = len(c.chamadas_modelo)
    r = _resumo_com(c, janela, n)
    passo = (r or {}).get("janela", {}).get("passo_s")
    if passo != 60:
        c.res.add(G, "Troca de preço: passo da consulta", 60, passo, "exato", None,
                  "com balde maior que 1 min a vigência não cai na virada do balde e a conta não é exata")
        return
    antes = (_por_modelo(r, c.modelo) or {}).get("custo_usd")
    c.res.add(G, "Troca de preço: custo antes da troca", f"US$ {antigo:.6f}", antes, "1e-9",
              antes is not None and abs(antes - antigo) < TOL_CUSTO, f"{n} chamadas anteriores do modelo, preço antigo")
    novo = c.painel.req("POST", "/api/ia/precos", {"provedor": "openai", "modelo": c.modelo, "entrada_por_1m": PRECO_NOVO_ENTRADA,
                                                   "saida_por_1m": PRECO_NOVO_SAIDA, "cache_leitura_por_1m": PRECO_NOVO_CACHE,
                                                   "vigente_desde": _iso(vig)})
    c.precos_extra.append(novo["id"])
    pm = _por_modelo(c.painel.get("/api/ia/resumo", service=c.service, **janela), c.modelo) or {}
    c.res.add(G, "Troca de preço: o passado não é recalculado", f"US$ {antigo:.6f}", pm.get("custo_usd"), "1e-9",
              pm.get("custo_usd") is not None and abs(pm["custo_usd"] - antigo) < TOL_CUSTO,
              f"linha nova vigente desde {_iso(vig)}; nenhuma chamada antiga é desse minuto em diante")
    ent, sai = TOKENS_TROCA
    t, ts = c.novo_id(32), time.time() - 0.5  # ≥ vig + 1,5 s: cai no minuto da vigência nova
    c.enviar([_span(t, c.novo_id(16), "", "chat", ts, 200,
                    {"gen_ai.operation.name": "chat", "gen_ai.provider.name": "openai", "gen_ai.request.model": c.modelo,
                     "gen_ai.usage.input_tokens": ent, "gen_ai.usage.output_tokens": sai})])
    c.chamadas_modelo.append((ts, ent, sai, 0))
    esperado = antigo + _custo(ent, sai, 0, PRECO_NOVO_ENTRADA, PRECO_NOVO_SAIDA, PRECO_NOVO_CACHE)
    pm = _por_modelo(_resumo_com(c, janela, n + 1), c.modelo) or {}
    obs = pm.get("custo_usd")
    c.res.add(G, "Troca de preço: custo = antigo×anteriores + novo×nova", f"US$ {esperado:.6f}", obs, "1e-9",
              obs is not None and abs(obs - esperado) < TOL_CUSTO,
              f"antigo US$ {antigo:.6f} + {ent}×{PRECO_NOVO_ENTRADA} + {sai}×{PRECO_NOVO_SAIDA} por 1 mi")
    preco = (pm.get("preco") or {}).get("entrada_por_1m")
    c.res.add(G, "Troca de preço: tabela mostra o preço vigente", PRECO_NOVO_ENTRADA, preco, "exato", preco == PRECO_NOVO_ENTRADA)


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


# ------------------------------------------------------------------ purge (roda por ÚLTIMO)

def _status(c, caminho):
    return c.painel.bruto("GET", caminho)[0]


def caso_purge(c, trace):
    """Pedido de titular (LGPD): apagar por trace e por conversa tira as chamadas de IA
    (genai_spans e genai_conteudo), mas não a tabela genérica de traces. todo_conteudo
    NÃO roda aqui: é um TRUNCATE do conteúdo de toda a stack (ver README).

    Purge é permissão crítica (2FA + reautenticação de 5 min, PermApagarDadosIA): este
    caso roda depois da espera da série llm.*, quando a reautenticação de antes já
    venceu, então reautentica aqui. A recusa sem reautenticação é provada nos testes de
    rota (server/internal/httpapi/ia_rotas_test.go)."""
    c.painel.reautenticar()
    st, r = c.painel.bruto("POST", "/api/ia/purge", {"alvo": "trace", "valor": trace})
    c.res.add(G, "Purge por trace aceito", 202, st, "exato", st == 202, str(r)[:160])
    t0 = time.time()
    sumiu = c.esperar(lambda: _status(c, f"/api/ia/execucoes/{trace}") == 404, timeout=TIMEOUT_PURGE)
    c.res.add(G, "Purge por trace: execução some do replay", f"404 em até {TIMEOUT_PURGE} s",
              f"404 em {time.time() - t0:.0f} s" if sumiu else f"ainda {_status(c, f'/api/ia/execucoes/{trace}')}",
              "mutation assíncrona", bool(sumiu))
    st, r = c.painel.bruto("GET", f"/api/traces/{trace}")
    n = len(r.get("spans") or []) if st == 200 and isinstance(r, dict) else 0
    c.res.add(G, "Purge de IA não toca /api/traces", f"{c.spans_cenario} spans", f"{n} spans (HTTP {st})", "exato",
              True if n == c.spans_cenario else (False if n == 0 else None),
              "o purge de IA apaga genai_*; o waterfall genérico (spans, 15 dias) tem rota própria")
    conv = f"amostra-{c.run}"
    if not c.traces_amostra:
        c.res.add(G, "Purge por conversa", "20 execuções da amostragem", "nenhuma enviada", "-", None)
        return
    st, r = c.painel.bruto("POST", "/api/ia/purge", {"alvo": "conversa", "valor": conv})
    c.res.add(G, "Purge por conversa aceito", 202, st, "exato", st == 202, str(r)[:160])

    def visiveis():
        return [t for t in c.traces_amostra if _status(c, f"/api/ia/execucoes/{t}") != 404]
    t0 = time.time()
    c.esperar(lambda: not c.painel.get("/api/ia/execucoes", service=c.service, conversa_id=conv, limit=100,
                                       **{"from": _iso(c.t0)}).get("execucoes"), timeout=TIMEOUT_PURGE)
    sobra = visiveis()
    c.res.add(G, "Purge por conversa: as execuções somem", f"0 de {len(c.traces_amostra)} visíveis",
              f"{len(sobra)} visíveis após {time.time() - t0:.0f} s", "mutation assíncrona", not sobra)


def preparar_preco(c):
    p = c.painel.req("POST", "/api/ia/precos", {"provedor": "openai", "modelo": c.modelo, "entrada_por_1m": PRECO_ENTRADA,
                                                "saida_por_1m": PRECO_SAIDA, "cache_leitura_por_1m": PRECO_CACHE,
                                                "vigente_desde": _iso(time.time() - 86400)})
    c.preco_id = p["id"]


def limpar(c):
    for pid in c.precos_extra + ([c.preco_id] if c.preco_id else []):
        try:
            c.painel.req("DELETE", f"/api/ia/precos/{pid}")
        except (RuntimeError, urllib.error.URLError) as e:
            c.res.add(G, "Limpeza: linha de preço apagada", pid, f"erro: {e}", "-", False, "apague à mão em Agentes de IA → Preços")


def executar(res, painel, args, run):
    print("\n== ia ==", flush=True)
    c = Ctx(res, painel, args, run)
    try:
        preparar_preco(c)
        trace, spans, custo, chamadas = _cenario(c)
        c.enviar(spans)
        c.chamadas_modelo += chamadas
        c.spans_cenario = len(spans)
        casos = [(caso_resumo_e_custo, (trace, custo)), (caso_replay, (trace,)), (caso_conteudo, (trace,)),
                 (caso_amostragem, ()), (caso_muitos_rotulos, ())]
        if not getattr(args, "ia_sem_descartaveis", False):
            casos += [(caso_permissoes, (trace,)), (caso_mcp, (trace,))]
        # troca de preço depois do MCP (nenhuma chamada nova entre o ia_resumo e o REST);
        # a série antes do purge: o runner lê genai_spans com 5 min de atraso (runner.go) e
        # um purge antes disso tiraria as chamadas da série de alerta.
        casos += [(caso_troca_de_preco, ()), (caso_serie_de_alerta, (args.duracao_ia,)), (caso_purge, (trace,))]
        for caso, a in casos:
            try:
                caso(c, *a)
            except Exception as e:  # noqa: BLE001 — um caso quebrado não derruba os outros
                res.add(G, caso.__name__, "executar", f"erro: {e}", "-", False)
    except (urllib.error.URLError, RuntimeError, KeyError) as e:
        res.add(G, "preparar", "gateway e painel no ar", f"erro: {e}", "-", False)
    finally:
        limpar(c)
