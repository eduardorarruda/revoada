"""Checklist de sites e jornadas contra um servidor de teste que registra tudo.

Para cada check criado pela API (a mesma do formulário da tela Websites) prova:
  - o painel bateu na URL configurada (caminho + query), com GET e o User-Agent
    da sonda, no intervalo do tier (60 s no `critico`; 30 s no reteste de falha);
  - o status/diagnóstico/latência gravados batem com o que o servidor fez;
  - transições UP → SUSPEITO → DOWN → UP e os alertas (canal webhook temporário).
Jornadas: passos na ordem, com método, corpo de formulário e cookies certos, e o
passo quebrado é o reportado.
"""
import threading
import time
import urllib.parse
import urllib.request

G = "sites"
GJ = "jornadas"
UA_CENTRAL = "Revoada-Next/synthetic"
UA_SONDA = "Revoada-Next/sonda"
# A cadência é de início a início: o tier promete 60 s, o reteste 30 s. ±3 s cobre o
# jitter do agendador e ainda pega o desvio sistemático de +5 s (degrau do tick).
TOL_INTERVALO = 3


def _ts(s):
    from datetime import datetime
    return datetime.fromisoformat(s.replace("Z", "+00:00")).timestamp()


class Execucao:
    def __init__(self, res, painel, srv, run, args):
        self.res, self.painel, self.srv, self.run, self.args = res, painel, srv, run, args
        self.checks = {}  # nome curto -> (id, spec)
        self.journeys = {}
        self.canal = None
        self.estados_flip = []

    def nome(self, curto):
        return f"val-{self.run}-{curto}"

    def criar_check(self, curto, url, **extra):
        corpo = {"name": self.nome(curto), "url": url, "tier": "critico", "group_name": "Validação", **extra}
        r = self.painel.req("POST", "/api/site-checks", corpo)
        self.checks[curto] = (r["id"], corpo)

    def listar(self):
        return {c["name"]: c for c in self.painel.get("/api/site-checks")["checks"]}

    def historico(self, curto):
        cid = self.checks[curto][0]
        return sorted(self.painel.get(f"/api/site-checks/{cid}/history")["history"], key=lambda h: _ts(h["ts"]))

    def controle(self, flip):
        urllib.request.urlopen(self.srv.url("controle", query=f"flip={flip}"), timeout=5).read()

    def flipar(self):
        """UP → (servidor passa a dar 500) → DOWN → (volta 200) → UP, anotando estados."""
        nome = self.nome("flip")
        fim = time.time() + 60
        while time.time() < fim and self.listar().get(nome, {}).get("state") != "UP":
            time.sleep(2)
        self.controle(500)
        t_quebra = time.time()
        fim = time.time() + 180
        while time.time() < fim:
            st = self.listar().get(nome, {}).get("state")
            if not self.estados_flip or self.estados_flip[-1][1] != st:
                self.estados_flip.append((time.time() - t_quebra, st))
            if st == "DOWN":
                break
            time.sleep(2)
        self.controle(200)
        t_volta = time.time()
        fim = time.time() + 120
        while time.time() < fim:
            st = self.listar().get(nome, {}).get("state")
            if self.estados_flip[-1][1] != st:
                self.estados_flip.append((time.time() - t_quebra, st))
            if st == "UP":
                break
            time.sleep(2)
        self.t_quebra, self.t_volta = t_quebra, t_volta


def _intervalos(ts):
    return [round(b - a, 1) for a, b in zip(ts, ts[1:])]


def avaliar_check(e, curto, rota, esperado):
    """esperado: dict com state, diag, status, intervalo (s), e opcionais."""
    nome = e.nome(curto)
    _, spec = e.checks[curto]
    hist = e.historico(curto)
    lista = e.listar().get(nome, {})
    url = urllib.parse.urlsplit(spec["url"])
    if esperado.get("tls_porta"):
        reqs = [r for r in e.srv.reg.reqs if r["caminho"] == "<tls-recusado>" and r.get("porta") == esperado["tls_porta"]]
    elif rota is None:
        reqs = []
    else:
        reqs = [r for r in e.srv.reg.de("/" + rota) if r["query"] == url.query]
    n_exec = len(hist)
    titulo = f"{curto} ({esperado['descricao']})"
    if rota is not None:
        e.res.add(G, f"{titulo}: cada execução = 1 requisição no endpoint", f"{n_exec} req(s) para {n_exec} execução(ões)",
                  f"{len(reqs)} req(s)", "+1 se uma sondagem estava em voo", n_exec >= 2 and 0 <= len(reqs) - n_exec <= 1)
    if reqs and not esperado.get("tls_porta"):
        metodos = {r["metodo"] for r in reqs}
        uas = {r["cab"].get("user-agent") for r in reqs}
        e.res.add(G, f"{titulo}: método/UA/caminho/query", f"GET, {UA_CENTRAL}, {url.path}?{url.query}",
                  f"{','.join(metodos)}, {','.join(map(str, uas))}, {reqs[0]['caminho']}?{reqs[0]['query']}", "exato",
                  metodos == {"GET"} and uas == {UA_CENTRAL} and reqs[0]["caminho"] == url.path)
    if len(reqs) >= 2 and esperado.get("intervalo"):
        gaps = _intervalos([r["t"] for r in reqs])
        alvo = esperado["intervalo"]
        if isinstance(alvo, tuple):  # (primeiro gap, demais): ex. UP 60 s e depois reteste 30 s
            ok = all(abs(g - alvo[1]) <= TOL_INTERVALO for g in gaps[1:]) and abs(gaps[0] - alvo[0]) <= TOL_INTERVALO
            txt = f"{alvo[0]} s e depois {alvo[1]} s"
        else:
            ok = all(abs(g - alvo) <= TOL_INTERVALO for g in gaps)
            txt = f"{alvo} s"
        e.res.add(G, f"{titulo}: intervalo entre sondagens", txt, gaps, f"±{TOL_INTERVALO} s (início a início)", ok)
    if hist:
        status = {h["status"] for h in hist}
        diags = {h["diagnosis"] for h in hist}
        oks = {h["ok"] for h in hist}
        e.res.add(G, f"{titulo}: resultado gravado", f"status={esperado['status']} diag='{esperado['diag']}' ok={esperado['ok']}",
                  f"status={sorted(status)} diag={sorted(diags)} ok={sorted(oks)}", "exato",
                  status == {esperado["status"]} and diags == {esperado["diag"]} and oks == {esperado["ok"]})
        if "latencia" in esperado:
            lo, hi = esperado["latencia"]
            tot = [h["total_ms"] for h in hist]
            e.res.add(G, f"{titulo}: latência total_ms", f"{lo}–{hi} ms", f"{min(tot):.0f}–{max(tot):.0f} ms", "faixa",
                      all(lo <= t <= hi for t in tot))
    e.res.add(G, f"{titulo}: estado na tela", esperado["state"], f"{lista.get('state')} ({lista.get('last_diagnosis')!r})",
              "exato", lista.get("state") == esperado["state"])
    spark = lista.get("sparkline") or []
    if hist and spark:
        ult = [round(h["total_ms"], 3) for h in hist]
        sp = [round(x, 3) for x in spark]
        # uma sondagem pode ter terminado entre as duas leituras: aceita o deslocamento de 1
        igual = sp == ult[-len(sp):] or sp[:-1] == ult[-(len(sp) - 1):]
        e.res.add(G, f"{titulo}: mini-gráfico da lista = latências gravadas", ult[-3:], sp[-3:], "exato", igual)


def avaliar_jornadas(e):
    js = {j["name"]: j for j in e.painel.get("/api/journeys")["journeys"]}
    reqs = [r for r in e.srv.reg.reqs if "/j/" in r["caminho"]]

    # As jornadas rodam em paralelo: as requisições das duas se intercalam no
    # registro. Cada rota pertence a UMA jornada (inicio se distingue pela query),
    # então separamos por rota e cortamos as execuções em cada "inicio".
    rotas = {"ok": {"login", "painel"}, "q": {"login-quebrado"}}

    def execucoes(tag):
        out, atual = [], None
        for r in reqs:
            rota = r["caminho"].split("/j/")[1]
            if rota == "inicio":
                if r["query"] == f"j={tag}":
                    atual = [r]
                    out.append(atual)
            elif rota in rotas[tag] and atual is not None:
                atual.append(r)
        return out

    ok_execs = execucoes("ok")
    seq_esp = [("GET", "inicio"), ("POST", "login"), ("GET", "painel"), ("GET", "painel")]
    boas = 0
    det = ""
    for ex in ok_execs:
        seq = [(r["metodo"], r["caminho"].split("/j/")[1]) for r in ex]
        login = ex[1] if len(ex) > 1 else None
        form = urllib.parse.parse_qs(login["corpo"]) if login else {}
        cond = (seq == seq_esp and form == {"usuario": ["ana"], "senha": ["s3"]}
                and login["cab"].get("content-type") == "application/x-www-form-urlencoded"
                and "sessao=s1" in login["cab"].get("cookie", "")
                and all("logado=ana" in r["cab"].get("cookie", "") for r in ex[2:]))
        boas += cond
        if not cond and not det:
            det = f"sequência={seq} form={form} cookies={[r['cab'].get('cookie') for r in ex]}"
    e.res.add(GJ, "Jornada OK: passos na ordem, método, formulário e cookies", f"{len(ok_execs)}× GET inicio → POST login(usuario,senha) → GET painel (redirect) → GET painel",
              f"{boas}/{len(ok_execs)} execuções conformes", "exato", len(ok_execs) >= 2 and boas == len(ok_execs), det)
    j = js.get(e.nome("jok"), {})
    e.res.add(GJ, "Jornada OK: estado na tela", "UP, sem diagnóstico", f"{j.get('state')} {j.get('last_diagnosis')!r}",
              "exato", j.get("state") == "UP" and not j.get("last_diagnosis"))
    if len(ok_execs) >= 2:
        gaps = _intervalos([ex[0]["t"] for ex in ok_execs])
        e.res.add(GJ, "Jornada OK: intervalo", "30 s", gaps, f"±{TOL_INTERVALO} s", all(abs(g - 30) <= TOL_INTERVALO for g in gaps))
    q_execs = execucoes("q")
    seqs_q = [[(r["metodo"], r["caminho"].split("/j/")[1]) for r in ex] for ex in q_execs]
    ok_q = all(s == [("GET", "inicio"), ("POST", "login-quebrado")] for s in seqs_q)
    e.res.add(GJ, "Jornada quebrada: para no passo que falhou", "GET inicio → POST login-quebrado (500) e nada depois",
              f"{len(q_execs)} execuções; {seqs_q[0] if seqs_q else '-'}", "exato", len(q_execs) >= 2 and ok_q)
    jq = js.get(e.nome("jq"), {})
    diag_esp = "passo 2 (login): status 500"
    e.res.add(GJ, "Jornada quebrada: diagnóstico aponta o passo", diag_esp, jq.get("last_diagnosis"), "exato",
              jq.get("last_diagnosis") == diag_esp)
    e.res.add(GJ, "Jornada quebrada: SUSPEITO → DOWN após 2 falhas", "DOWN", jq.get("state"), "exato", jq.get("state") == "DOWN")
    if len(q_execs) >= 2:
        gaps = _intervalos([ex[0]["t"] for ex in q_execs])
        e.res.add(GJ, "Jornada quebrada: reteste", "30 s", gaps, f"±{TOL_INTERVALO} s", all(abs(g - 30) <= TOL_INTERVALO for g in gaps))
    alertas = [w for w in e.srv.reg.webhooks if isinstance(w["msg"], dict)
               and any(a.get("labels", {}).get("journey") == e.nome("jq") for a in w["msg"].get("alerts", []))]
    estados = [w["msg"].get("state") for w in alertas]
    e.res.add(GJ, "Jornada quebrada: alerta entregue no webhook", "1 firing", estados, "exato", estados == ["firing"])


def avaliar_flip(e):
    nome = e.nome("flip")
    seq = [s for _, s in e.estados_flip]
    e.res.add(G, "flip: transições observadas na tela", "UP → SUSPEITO → DOWN → UP",
              " → ".join(["UP"] + seq), "exato", seq == ["UP", "SUSPEITO", "DOWN", "UP"] or seq == ["SUSPEITO", "DOWN", "UP"])
    alertas = [w for w in e.srv.reg.webhooks if isinstance(w["msg"], dict)
               and any(a.get("labels", {}).get("site") == nome for a in w["msg"].get("alerts", []))]
    estados = [w["msg"].get("state") for w in alertas]
    e.res.add(G, "flip: alertas no webhook", "firing (no DOWN), depois resolved", estados, "exato", estados == ["firing", "resolved"])
    if len(alertas) == 2:
        t_fire = alertas[0]["t"] - e.t_quebra
        reqs = [r for r in e.srv.reg.de("/flip") if r["t"] >= e.t_quebra]
        e.res.add(G, "flip: DOWN só na 2ª falha seguida (tier crítico)", "firing após a 2ª req. com 500",
                  f"firing {t_fire:.0f} s após quebrar; req. falhas antes do firing: {sum(1 for r in reqs if r['t'] < alertas[0]['t'])}",
                  "exato", sum(1 for r in reqs if r["t"] < alertas[0]["t"]) == 2)
        dur = alertas[1]["msg"]["alerts"][0].get("duration")
        e.res.add(G, "flip: 'resolved' informa a duração da queda", "duração presente", dur, "-", bool(dur))


def executar(res, painel, srv, args, run):
    print("\n== sites e jornadas ==", flush=True)
    e = Execucao(res, painel, srv, run, args)
    try:
        r = painel.req("POST", "/api/notify/channels", {"name": f"val-{run}-webhook", "type": "webhook", "enabled": True,
                                                         "config": {"url": srv.url("webhook")}})
        e.canal = r.get("id") if isinstance(r, dict) else None
    except RuntimeError as ex:
        res.add(G, "canal webhook temporário", "criado", str(ex), "-", None)
    q = "q=1&acao=a%C3%A7%C3%A3o"
    e.criar_check("ok", srv.url("ok", query=q), keyword="PALAVRA-OK")
    e.criar_check("erro500", srv.url("erro500", query=q))
    e.criar_check("404", srv.url("nao-existe", query=q))
    e.criar_check("404esperado", srv.url("nao-existe", query="esperado=1"), expect_status=404)
    e.criar_check("redir", srv.url("redir1", query=q))
    e.criar_check("lento3", srv.url("lento3", query=q), max_latency_ms=1000)
    e.criar_check("lento25", srv.url("lento25", query=q))
    e.criar_check("timeout5", srv.url("lento25", query="timeout=5"), timeout_ms=5000)
    e.criar_check("sempalavra", srv.url("sem-palavra", query=q), keyword="PALAVRA-OK")
    e.criar_check("laco", srv.url("laco", query=q))
    e.criar_check("flip", srv.url("flip", query=q), keyword="PALAVRA-OK")
    e.criar_check("recusado", "http://127.0.0.1:9/fechado")
    e.criar_check("dns", "http://nao-existe.invalid/")
    if srv.https_auto:
        e.criar_check("tls-auto", srv.url("ok", srv.https_auto))
    if srv.https_exp:
        e.criar_check("tls-exp", srv.url("ok", srv.https_exp))
    if args.sonda:
        e.criar_check("sonda", srv.url("ok", query="sonda=1"), probe_locations=[args.sonda])
    base = srv.url("j/inicio").rsplit("/j/", 1)[0]
    for curto, login in (("jok", "login"), ("jq", "login-quebrado")):
        tag = "ok" if curto == "jok" else "q"
        r = painel.req("POST", "/api/journeys", {"name": e.nome(curto), "interval_seconds": 30, "steps": [
            {"name": "início", "method": "GET", "url": f"{base}/j/inicio?j={tag}", "assert_contains": "Bem-vindo"},
            {"name": "login", "method": "POST", "url": f"{base}/j/{login}", "form": {"usuario": "ana", "senha": "s3"},
             "assert_contains": "Olá, ana"},
            {"name": "painel", "method": "GET", "url": f"{base}/j/painel", "assert_status": 200, "assert_contains": "Olá"},
        ]})
        e.journeys[curto] = r["id"]
    th = threading.Thread(target=e.flipar, daemon=True)
    th.start()
    t0 = time.time()
    time.sleep(max(0, args.duracao_sites - (time.time() - t0)))
    th.join(timeout=240)
    try:
        auto_porta = srv.https_auto.server_address[1] if srv.https_auto else None
        exp_porta = srv.https_exp.server_address[1] if srv.https_exp else None
        espec = [
            ("ok", "ok", dict(descricao="200 + palavra", state="UP", diag="", status=200, ok=True, intervalo=60, latencia=(0, 1000))),
            ("erro500", "erro500", dict(descricao="500", state="DOWN", diag="http_5xx", status=500, ok=False, intervalo=30)),
            ("404", "nao-existe", dict(descricao="404 com 200 esperado", state="DOWN", diag="http_status", status=404, ok=False, intervalo=30)),
            ("404esperado", "nao-existe", dict(descricao="404 esperado", state="UP", diag="", status=404, ok=True, intervalo=60)),
            ("redir", "redir1", dict(descricao="301→302→200", state="UP", diag="", status=200, ok=True, intervalo=60)),
            ("lento3", "lento3", dict(descricao="3 s com limite 1 s", state="DEGRADADO", diag="slow", status=200, ok=True, intervalo=60, latencia=(3000, 4500))),
            ("lento25", "lento25", dict(descricao="25 s, timeout padrão 20 s", state="DOWN", diag="sem_resposta", status=0, ok=False, latencia=(19500, 21500))),
            ("timeout5", "lento25", dict(descricao="timeout_ms=5000 configurado", state="DOWN", diag="sem_resposta", status=0, ok=False, intervalo=30, latencia=(4500, 6500))),
            ("sempalavra", "sem-palavra", dict(descricao="palavra ausente", state="DOWN", diag="keyword_missing", status=200, ok=False, intervalo=30)),
            ("laco", "laco", dict(descricao="laço de redirect", state="DOWN", diag="redirect_loop", status=0, ok=False)),
            ("recusado", None, dict(descricao="porta fechada", state="DOWN", diag="connect_refused", status=0, ok=False)),
            ("dns", None, dict(descricao="DNS inexistente", state="DOWN", diag="dns_error", status=0, ok=False)),
        ]
        if auto_porta:
            espec.append(("tls-auto", "ok", dict(descricao="TLS autoassinado", state="DOWN", diag="tls", status=0, ok=False, tls_porta=auto_porta, intervalo=30)))
        if exp_porta:
            espec.append(("tls-exp", "ok", dict(descricao="TLS expirado", state="DOWN", diag="tls", status=0, ok=False, tls_porta=exp_porta, intervalo=30)))
        for curto, rota, esp in espec:
            try:
                avaliar_check(e, curto, rota, esp)
            except Exception as ex:  # noqa: BLE001
                res.add(G, curto, "avaliar", f"erro: {ex}", "-", False)
        # Redirect: cada execução percorre redir1 → redir2 → ok, nessa ordem.
        r1, r2 = e.srv.reg.de("/redir1"), e.srv.reg.de("/redir2")
        e.res.add(G, "redir: cadeia seguida até o fim", "redir1 → redir2 → ok por execução", f"{len(r1)}× redir1, {len(r2)}× redir2",
                  "iguais", len(r1) >= 2 and len(r1) == len(r2))
        laco = e.srv.reg.de("/laco")
        nexec = len(e.historico("laco"))
        e.res.add(G, "laco: desiste após 10 saltos", f"{nexec * 10} req. ({nexec} execuções × 10)", len(laco), "+10 se em voo",
                  nexec > 0 and 0 <= len(laco) - nexec * 10 <= 10)
        e.res.add(G, "timeout_ms do check aparece na API", 5000, e.listar().get(e.nome("timeout5"), {}).get("timeout_ms"), "exato",
                  e.listar().get(e.nome("timeout5"), {}).get("timeout_ms") == 5000)
        avaliar_flip(e)
        avaliar_jornadas(e)
        if args.sonda:
            reqs = [r for r in e.srv.reg.de("/ok") if r["query"] == "sonda=1" and r["cab"].get("user-agent") == UA_SONDA]
            pr = painel.get("/api/site-checks/probes", url=e.checks["sonda"][1]["url"])
            e.res.add(G, f"sonda '{args.sonda}': agente sondou o endpoint", "≥1 req. com UA da sonda", len(reqs), "-", len(reqs) >= 1)
            e.res.add(G, f"sonda '{args.sonda}': resultado reportado ao painel", "up=true", pr, "-",
                      any(p.get("location") == args.sonda and p.get("up") for p in (pr.get("probes") or [])))
    finally:
        for curto, (cid, _) in e.checks.items():
            try:
                painel.req("DELETE", f"/api/site-checks/{cid}")
            except RuntimeError:
                pass
        for jid in e.journeys.values():
            try:
                painel.req("DELETE", f"/api/journeys/{jid}")
            except RuntimeError:
                pass
        if e.canal:
            try:
                painel.req("DELETE", f"/api/notify/channels/{e.canal}")
            except RuntimeError:
                pass
