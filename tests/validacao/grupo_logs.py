"""Leitura de logs: escreve linhas conhecidas num arquivo que o agente segue
(log_paths) e confere, pela MESMA busca da tela de Logs (/api/logs/search e
/api/logs/histogram), que cada linha chegou uma vez, em ordem, com host/source/
file/serviço/horário certos — e cobre multiline, linha gigante, UTF-8, rotação,
truncamento, rajada, taxa constante e redaction de segredos.
"""
import os
import time
from datetime import datetime, timezone

G = "logs"
TRUNC_MARK = " …(linha truncada)"
MAX_LINHA = 256 << 10
REDIGIDO = "«redigido»"


def _iso(t):
    return datetime.fromtimestamp(t, timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")


class Ctx:
    def __init__(self, res, painel, host, dir_logs, run):
        self.res, self.painel, self.host, self.dir, self.run = res, painel, host, dir_logs, run
        self.t0 = time.time() - 5
        self.arquivos = []

    def arq(self, nome):
        p = os.path.join(self.dir, f"validacao-{self.run}-{nome}.log")
        self.arquivos.append(p)
        return p

    def escrever(self, caminho, linhas, modo="a"):
        with open(caminho, modo, encoding="utf-8") as f:
            for l in linhas:
                f.write(l + "\n")
            f.flush()
            os.fsync(f.fileno())

    def buscar(self, q, limite=1000):
        r = self.painel.get("/api/logs/search", q=q, host=self.host, limit=limite, **{"from": _iso(self.t0)})
        return sorted(r.get("rows") or [], key=lambda x: int(x["t"]))

    def contar(self, q):
        r = self.painel.get("/api/logs/histogram", q=q, host=self.host, step=86400, **{"from": _iso(self.t0)})
        return sum(int(b["c"]) for b in (r.get("buckets") or []))

    def esperar(self, q, n, timeout=45, contar=False):
        """Espera até n linhas casarem q (ou o tempo acabar) e mais um ciclo para pegar duplicatas."""
        fim = time.time() + timeout
        obtido = 0
        while time.time() < fim:
            obtido = self.contar(q) if contar else len(self.buscar(q))
            if obtido >= n:
                break
            time.sleep(2)
        time.sleep(11 if obtido >= n else 0)  # um ciclo a mais: duplicata apareceria aqui
        return self.contar(q) if contar else self.buscar(q)


def caso_basico(c):
    m = f"VAL{c.run}A"
    caminho = c.arq("basico")
    t_esc = time.time()
    linhas = [f"{m} seq={i:04d} linha comum de teste" for i in range(50)]
    c.escrever(caminho, linhas)
    rows = c.esperar(m, 50)
    seqs = [r["body"].split("seq=")[1][:4] for r in rows if "seq=" in r["body"]]
    c.res.add(G, "Linhas chegam exatamente uma vez", "50 linhas, 50 únicas", f"{len(rows)} linhas, {len(set(seqs))} únicas",
              "0", len(rows) == 50 and len(set(seqs)) == 50)
    c.res.add(G, "Ordem preservada (ts crescente = ordem de escrita)", "seq 0000..0049 em ordem",
              "em ordem" if seqs == sorted(seqs) else f"fora de ordem: {seqs[:8]}…", "0", seqs == sorted(seqs) and len(seqs) == 50)
    if rows:
        lab = rows[0]["labels"]
        servico = os.path.splitext(os.path.basename(caminho))[0]
        ok_lab = all(r["labels"].get("host") == c.host and r["labels"].get("source") == "file"
                     and r["labels"].get("file") == caminho and r["service"] == servico for r in rows)
        c.res.add(G, "Rótulos host/source/file/serviço", f"host={c.host} source=file file=<arquivo> service={servico}",
                  f"host={lab.get('host')} source={lab.get('source')} file={'=' if lab.get('file') == caminho else lab.get('file')} service={rows[0]['service']}",
                  "exato", ok_lab)
        atrasos = [int(r["t"]) / 1000 - t_esc for r in rows]
        c.res.add(G, "Horário = momento da coleta (≤ 1 ciclo de 10 s + envio)", "0 s ≤ atraso ≤ 25 s",
                  f"{min(atrasos):.1f}–{max(atrasos):.1f} s", "25 s", min(atrasos) >= -1 and max(atrasos) <= 25,
                  "o agente não envia o horário da linha: o gateway carimba a chegada (logs_receiver.go)")
    m2 = f"VAL{c.run}S"
    c.escrever(caminho, [f"{m2} ERROR falha simulada no pagamento", f"{m2} WARN disco quase cheio", f"{m2} nada a declarar"])
    rows = c.esperar(m2, 3)
    sev = {r["body"].split()[1]: r["severity"] for r in rows}
    c.res.add(G, "Severidade por palavra (ERROR/WARN/sem palavra)", "ERROR, WARN, UNKNOWN",
              f"{sev.get('ERROR')}, {sev.get('WARN')}, {sev.get('nada')}", "exato",
              sev.get("ERROR") == "ERROR" and sev.get("WARN") == "WARN" and sev.get("nada") == "UNKNOWN")


def caso_multiline(c):
    m = f"VAL{c.run}B"
    caminho = c.arq("multiline")
    java = [f"{m}J Exception in thread \"main\" java.lang.IllegalStateException: boom",
            "\tat com.exemplo.Pagamento.processar(Pagamento.java:42)",
            "\tat com.exemplo.Main.main(Main.java:7)",
            "Caused by: java.io.IOException: disco cheio",
            "\t... 2 more"]
    py = [f"{m}P ERROR falha ao processar pedido",
          "Traceback (most recent call last):",
          "  File \"app.py\", line 10, in <module>",
          "    processar()",
          "ValueError: valor inválido"]
    c.escrever(caminho, java + py + [f"{m}X linha seguinte independente"])
    rows = c.esperar(m, 3)
    por = {r["body"].split()[0]: r for r in rows}
    j, p = por.get(m + "J"), por.get(m + "P")
    c.res.add(G, "Stack trace Java vira UM registro", "\\n".join(["5 linhas"]), f"{len(rows)} registros; java={'1 com 5 linhas' if j and j['body'] == chr(10).join(java) else 'quebrado'}",
              "exato", bool(j) and j["body"] == "\n".join(java) and len(rows) == 3)
    c.res.add(G, "Traceback Python vira UM registro", "5 linhas num registro",
              "ok" if p and p["body"] == "\n".join(py) else (p["body"][:80] if p else "ausente"), "exato",
              bool(p) and p["body"] == "\n".join(py))
    c.res.add(G, "Severidade do stack trace", "ERROR", f"{j and j['severity']}/{p and p['severity']}", "exato",
              bool(j and p) and j["severity"] == "ERROR" and p["severity"] == "ERROR")


def caso_linhas_grandes_utf8(c):
    m = f"VAL{c.run}C"
    caminho = c.arq("grandes")
    media = f"{m}M " + "b" * (100 << 10)
    gigante = f"{m}G " + "a" * (300 << 10)
    utf = f"{m}U ação çãõ 日本語 🚀 «aspas» naïve"
    c.escrever(caminho, [media, gigante, utf])
    rows = c.esperar(m, 3)
    por = {r["body"][: len(m) + 1]: r for r in rows}
    rm, rg, ru = por.get(m + "M"), por.get(m + "G"), por.get(m + "U")
    c.res.add(G, "Linha de 100 KiB chega íntegra", f"{len(media)} bytes", len(rm["body"]) if rm else "ausente", "exato",
              bool(rm) and rm["body"] == media)
    esperado = gigante.encode()[:MAX_LINHA].decode("utf-8", "ignore") + TRUNC_MARK
    c.res.add(G, "Linha de 300 KiB é truncada em 256 KiB COM marcador", f"{len(esperado.encode())} bytes terminando em '{TRUNC_MARK.strip()}'",
              f"{len(rg['body'].encode())} bytes" if rg else "ausente", "exato", bool(rg) and rg["body"] == esperado)
    c.res.add(G, "UTF-8 (ação, 日本語, emoji) preservado", utf[len(m) + 2:], ru["body"][len(m) + 2:] if ru else "ausente",
              "byte a byte", bool(ru) and ru["body"] == utf)


def caso_rotacao(c):
    m = f"VAL{c.run}R"
    caminho = c.arq("rotacao")
    c.escrever(caminho, [f"{m} A{i}" for i in range(5)])
    c.esperar(f"{m} A", 5)
    # Rotação no estilo logrotate `create`: a aplicação escreveu B e, antes do
    # próximo ciclo do agente, o arquivo foi renomeado e um novo criado.
    c.escrever(caminho, [f"{m} B{i}" for i in range(5)])
    os.rename(caminho, caminho + ".1")
    c.arquivos.append(caminho + ".1")
    c.escrever(caminho, [f"{m} C{i}" for i in range(5)], modo="w")
    rows = c.esperar(f"{m} C", 5)
    todos = c.buscar(m)
    corpos = [r["body"] for r in todos]
    for lote in "ABC":
        n = sum(1 for b in corpos if b.startswith(f"{m} {lote}"))
        c.res.add(G, f"Rotação por rename: lote {lote} chega uma vez", "5", n, "exato", n == 5,
                  {"A": "antes da rotação", "B": "escrito no arquivo antigo pouco antes do rename",
                   "C": "arquivo novo"}[lote])
    aviso = c.buscar(f"rotação de log detectada em {caminho}")
    c.res.add(G, "Rotação gera aviso no stream", "≥1 aviso", len(aviso), "-", len(aviso) >= 1)
    del rows


def caso_truncamento(c):
    m = f"VAL{c.run}T"
    caminho = c.arq("trunc")
    c.escrever(caminho, [f"{m} T1-{i} " + "x" * 40 for i in range(10)])
    c.esperar(f"{m} T1", 10)
    with open(caminho, "w"):
        pass  # copytruncate: mesmo inode, tamanho 0
    c.escrever(caminho, [f"{m} T2-{i}" for i in range(3)])
    c.esperar(f"{m} T2", 3)
    corpos = [r["body"] for r in c.buscar(m)]
    n1 = sum(1 for b in corpos if b.startswith(f"{m} T1"))
    n2 = sum(1 for b in corpos if b.startswith(f"{m} T2"))
    c.res.add(G, "Truncamento (copytruncate, menor): sem perda nem duplicata", "T1=10, T2=3", f"T1={n1}, T2={n2}",
              "exato", n1 == 10 and n2 == 3)
    # Caso difícil: trunca e reescreve MAIS do que o offset antigo antes do ciclo.
    c.escrever(caminho, [f"{m} T3-{i}" for i in range(3)])
    c.esperar(f"{m} T3", 3)
    with open(caminho, "w"):
        pass
    c.escrever(caminho, [f"{m} T4-{i:02d} " + "y" * 60 for i in range(30)])
    c.esperar(f"{m} T4", 30, timeout=30)
    corpos = [r["body"] for r in c.buscar(m)]
    t4 = [b for b in corpos if f"{m} T4" in b]
    integras = sum(1 for b in t4 if b.startswith(f"{m} T4-") and b.endswith("y" * 60))
    c.res.add(G, "Truncamento seguido de escrita MAIOR que o offset antigo", "30 linhas íntegras",
              f"{integras} íntegras de {len(t4)} recebidas", "exato", integras == 30 and len(t4) == 30,
              "mesmo inode e tamanho maior: só o conteúdo denuncia o truncamento")


def caso_volume(c):
    m = f"VAL{c.run}V"
    caminho = c.arq("rajada")
    c.escrever(caminho, [f"{m} rajada {i:05d} payload-abcdefghij" for i in range(20000)])
    n = c.esperar(m, 20000, timeout=60, contar=True)
    c.res.add(G, "Rajada de 20 mil linhas num ciclo (2000/s, abaixo do teto de 5000/s)", "20000", n, "0", n == 20000)
    m2 = f"VAL{c.run}K"
    caminho = c.arq("constante")
    taxa, dur = 1500, 30
    escritas = 0
    t_ini = time.time()
    with open(caminho, "a", buffering=1) as f:
        while time.time() - t_ini < dur:
            alvo = int((time.time() - t_ini) * taxa)
            while escritas < alvo:
                f.write(f"{m2} constante {escritas:06d} x\n")
                escritas += 1
            time.sleep(0.01)
    n = c.esperar(m2, escritas, timeout=60, contar=True)
    c.res.add(G, f"Taxa constante {taxa} linhas/s por {dur} s (30% do teto)", escritas, n, "0", n == escritas,
              "o teto documentado é 5000 linhas/s e 1 MiB/s")
    avisos = [r for r in c.buscar("rate limit de logs") if int(r["t"]) / 1000 >= t_ini - 60]
    c.res.add(G, "Sem descarte por rate limit dentro da taxa", "0 avisos", len(avisos), "0", not avisos,
              avisos[0]["body"][:140] if avisos else "")


def caso_redaction(c):
    m = f"VAL{c.run}I"
    caminho = c.arq("segredos")
    casos = [
        ("senha chave=valor", f"{m}1 conectando com password=hunter2 ok", "hunter2", True),
        ("Bearer JWT", f"{m}2 Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTYifQ.c2lnbmF0dXJhYWJj", "eyJzdWIi", True),
        ("Bearer longo opaco", f"{m}3 Authorization: Bearer AbCdEf0123456789xyz", "AbCdEf0123456789xyz", True),
        ("Bearer curto (3 letras)", f"{m}4 Authorization: Bearer xyz", None, False),
        ("URL com senha", f"{m}5 DATABASE_URL=postgres://app:Sup3rSecreta@db:5432/x", "Sup3rSecreta", True),
        ("curl -u", f"{m}6 curl -u admin:S3nhaF0rte https://api", "S3nhaF0rte", True),
        ("AWS access key", f"{m}7 chave AKIAIOSFODNN7EXAMPLE em uso", "AKIAIOSFODNN7EXAMPLE", True),
        ("JSON api_key", f'{m}8 {{"api_key":"abcdef123456","user":"ana"}}', "abcdef123456", True),
        ("cartão de crédito (bandeira + Luhn)", f"{m}9 cobrança no cartão 4111 1111 1111 1111 aprovada", "4111 1111 1111 1111", True),
    ]
    c.escrever(caminho, [l for _, l, _, _ in casos])
    rows = c.esperar(m, len(casos))
    por = {r["body"].split()[0]: r["body"] for r in rows}
    for i, (nome, linha, segredo, prometido) in enumerate(casos, 1):
        corpo = por.get(f"{m}{i}")
        if corpo is None:
            c.res.add(G, f"Redaction: {nome}", "linha presente", "ausente", "-", False)
            continue
        if prometido is True:
            ok = segredo not in corpo and REDIGIDO in corpo
            c.res.add(G, f"Redaction: {nome}", "segredo mascarado", corpo[len(m) + 2:][:90], "segredo ausente", ok)
        elif prometido is False:
            c.res.add(G, f"Redaction: {nome}", "NÃO mascara (redact.go exige ≥8 caracteres no token)",
                      corpo[len(m) + 2:][:90], "conforme prometido", corpo == linha,
                      "conservador por design: valores curtos não são tratados como segredo")
        else:
            c.res.add(G, f"Redaction: {nome}", "não prometido por redact.go", corpo[len(m) + 2:][:90], "-", None,
                      "o redator não tem regra para este formato; fica visível no painel")
    vaz = c.buscar("hunter2")
    c.res.add(G, "Segredo não é pesquisável no painel", "busca 'hunter2' = 0", len(vaz), "0", not vaz)


def limpar(c):
    for p in c.arquivos:
        try:
            os.remove(p)
        except OSError:
            pass


def executar(res, painel, host, args, run):
    print("\n== logs ==", flush=True)
    c = Ctx(res, painel, host, args.dir_logs, run)
    try:
        for caso in (caso_basico, caso_multiline, caso_linhas_grandes_utf8, caso_redaction, caso_rotacao,
                     caso_truncamento, caso_volume):
            try:
                caso(c)
            except Exception as e:  # noqa: BLE001 — um caso quebrado não derruba os outros
                res.add(G, caso.__name__, "executar", f"erro: {e}", "-", False)
    finally:
        limpar(c)
