"""Métricas de host: RAM, swap, CPU (inclusive carga conhecida), disco, rede,
processos, uptime e load — comparadas com a verdade do kernel.

O que é validado é o que a tela mostra:
  - cartões do topo de HostDetail/Hosts  -> GET /api/host-metrics
  - gráficos de HostDetail               -> POST /api/query (step 60, agg avg/rate)
"""
import os
import subprocess
import sys
import threading
import time
from datetime import datetime, timezone

import verdade

G = "host"
MIB = 1 << 20


def _iso(t):
    return datetime.fromtimestamp(t, timezone.utc).strftime("%Y-%m-%dT%H:%M:%S.000Z")


class ColetorPainel:
    """Faz polling de /api/host-metrics (1 s) e guarda cada amostra NOVA do host."""

    def __init__(self, painel, host):
        self.painel, self.host = painel, host
        self.amostras = []  # dicts do host, com 'cpu','mem',... (só quando ts muda)
        self._parar = threading.Event()
        self._th = None
        self._ultimo = None

    def _laco(self):
        while not self._parar.is_set():
            try:
                hs = self.painel.get("/api/host-metrics")["hosts"]
                h = next((x for x in hs if x["host"] == self.host), None)
                if h and h.get("up"):
                    chave = (h["mem"].get("ts"), h["cpu"].get("ts"))
                    if chave != self._ultimo and h["mem"].get("ts"):
                        self._ultimo = chave
                        h["_lido_em"] = time.time()
                        self.amostras.append(h)
            except Exception as e:  # noqa: BLE001
                print("coletor painel:", e, flush=True)
            self._parar.wait(1.0)

    def iniciar(self):
        self._th = threading.Thread(target=self._laco, daemon=True)
        self._th.start()
        return self

    def parar(self):
        self._parar.set()
        if self._th:
            self._th.join(10)

    def desde(self, t):
        return [a for a in self.amostras if a["mem"]["ts"] >= t]

    def esperar_novas(self, n, timeout=60):
        alvo = len(self.amostras) + n
        fim = time.time() + timeout
        while len(self.amostras) < alvo and time.time() < fim:
            time.sleep(0.5)
        return self.amostras[-n:] if len(self.amostras) >= alvo else []


# Onde fica o instante da medida em relação ao `ts` da API: o ts é carimbado no FIM
# do ciclo do agente (depois de host + containers), e a API o trunca no segundo.
# Medido no dev (system.uptime vs btime): o carimbo fica até ~1,4 s depois da
# leitura. Logo a leitura está em [ts − 1,5 s, ts + 1 s).
ANTES, DEPOIS = 1.5, 1.0
DESLOCAMENTOS = (-1.5, -0.75, 0.0, 0.5, 1.0)


def _janela(ts):
    return ts - ANTES, ts + DEPOIS


def checar_ram(res, amostrador, amostras):
    ok_tot = ok_used = ok_pct = ok_sw_t = ok_sw_u = 0
    n = n_sw = 0
    piores = []
    for a in amostras:
        m = a["mem"]
        t0, t1 = _janela(m["ts"])
        jan = amostrador.janela(t0, t1)
        if not jan:
            continue
        n += 1
        tot = jan[0][1]["mem"]["MemTotal"]
        if m.get("total_bytes") == tot:
            ok_tot += 1
        usados = [d["mem"]["MemTotal"] - d["mem"]["MemAvailable"] for _, d in jan]
        lo, hi = min(usados) - 8 * MIB, max(usados) + 8 * MIB
        if lo <= m.get("used_bytes", -1) <= hi:
            ok_used += 1
        else:
            piores.append(f"ts={m['ts']} painel={m.get('used_bytes')} verdade=[{min(usados)},{max(usados)}]")
        pct_ref = [u / tot * 100 for u in usados]
        if min(pct_ref) - 0.1 <= m["pct"] <= max(pct_ref) + 0.1:
            ok_pct += 1
        sw = a.get("swap")
        if sw:
            n_sw += 1
            if sw.get("total_bytes") == jan[0][1]["mem"]["SwapTotal"]:
                ok_sw_t += 1
            su = [d["mem"]["SwapTotal"] - d["mem"]["SwapFree"] for _, d in jan]
            if min(su) - 8 * MIB <= sw.get("used_bytes", -1) <= max(su) + 8 * MIB:
                ok_sw_u += 1
    if n == 0:
        res.add(G, "RAM: amostras do painel", ">0", 0, "-", None, "nenhuma amostra pareada com a verdade")
        return
    res.add(G, "RAM total = MemTotal (/proc/meminfo)", f"{n}/{n} iguais", f"{ok_tot}/{n}", "0 B", ok_tot == n)
    res.add(G, "RAM usada = MemTotal − MemAvailable", f"{n}/{n} na faixa", f"{ok_used}/{n}",
            "variação da verdade em ±1 s + 8 MiB", ok_used == n, "; ".join(piores[:3]))
    res.add(G, "RAM % = usada/total", f"{n}/{n} na faixa", f"{ok_pct}/{n}", "±0,1 p.p. + variação", ok_pct == n)
    if n_sw:
        res.add(G, "Swap total = SwapTotal", f"{n_sw}/{n_sw}", f"{ok_sw_t}/{n_sw}", "0 B", ok_sw_t == n_sw)
        res.add(G, "Swap usada = SwapTotal − SwapFree", f"{n_sw}/{n_sw}", f"{ok_sw_u}/{n_sw}",
                "variação + 8 MiB", ok_sw_u == n_sw)
    else:
        swt = verdade.ler_meminfo().get("SwapTotal", 0)
        res.add(G, "Swap ausente no painel ⇔ máquina sem swap", "swap=0" if swt == 0 else "swap>0",
                f"SwapTotal={swt}", "-", swt == 0)
    # O rótulo "usada" do painel bate com a coluna do `free`?
    try:
        saida = subprocess.run(["free", "-b"], capture_output=True, text=True, timeout=5,
                               env={**os.environ, "LC_ALL": "C"}).stdout.split("\n")
        mem = saida[1].split()
        total, usado, disp = int(mem[1]), int(mem[2]), int(mem[6])
        res.add(G, "Definição de 'usada' = coluna used do free(1)", "used = total − available",
                f"free: total−available−used = {total - disp - usado} B", "≤ 64 MiB (instantes distintos)",
                abs(total - disp - usado) <= 64 * MIB)
    except (OSError, ValueError, IndexError) as e:
        res.add(G, "Definição de 'usada' vs free(1)", "free disponível", str(e), "-", None)


def checar_cpu(res, amostrador, amostras, rotulo="CPU % (repouso)"):
    n = ok = 0
    piores = []
    for ant, a in zip(amostras, amostras[1:]):
        tp, t = ant["cpu"].get("ts"), a["cpu"].get("ts")
        if not tp or not t or not (5 <= t - tp <= 20):
            continue
        refs = [amostrador.cpu_entre(tp + da, t + db) for da in DESLOCAMENTOS for db in DESLOCAMENTOS]
        refs = [r for r in refs if r is not None]
        if not refs:
            continue
        n += 1
        lo, hi = min(refs) - 3, max(refs) + 3
        v = a["cpu"]["pct"]
        if lo <= v <= hi:
            ok += 1
        else:
            piores.append(f"ts={t} painel={v:.1f} verdade=[{min(refs):.1f},{max(refs):.1f}]")
    if n == 0:
        res.add(G, rotulo, "pares consecutivos", 0, "-", None, "sem pares de amostras consecutivas")
        return
    res.add(G, rotulo + " vs /proc/stat no mesmo intervalo", f"{n}/{n} na faixa", f"{ok}/{n}",
            "±3 p.p. + incerteza do instante (−1,5…+1 s) nas bordas", ok >= n - (1 if n >= 8 else 0), "; ".join(piores[:3]))


def _girar(n):
    cod = "import time\nt=time.time()\nwhile time.time()-t<300: pass\n"
    return [subprocess.Popen([sys.executable, "-c", cod]) for _ in range(n)]


def checar_carga_conhecida(res, amostrador, coletor, segundos=40):
    k = os.cpu_count() or 1
    base = coletor.esperar_novas(3, timeout=60)
    if len(base) < 3:
        res.add(G, "CPU sob carga conhecida", "amostras de base", 0, "-", None, "painel sem amostras")
        return
    base_pct = sum(a["cpu"]["pct"] for a in base[1:]) / (len(base) - 1)
    n = max(1, k // 2)
    esperado_sub = n / k * 100
    procs = _girar(n)
    t_ini = time.time()
    try:
        time.sleep(segundos)
    finally:
        for p in procs:
            p.kill()
        for p in procs:
            p.wait()
    t_fim = time.time()
    durante = [a for a in coletor.amostras if t_ini + 12 <= a["cpu"]["ts"] <= t_fim]
    checar_cpu(res, amostrador, [a for a in coletor.amostras if t_ini <= a["cpu"]["ts"] <= t_fim + 1],
               rotulo=f"CPU % sob carga ({n} de {k} núcleos)")
    if not durante:
        res.add(G, "CPU sobe com a carga", "amostras", 0, "-", None)
        return
    med = sum(a["cpu"]["pct"] for a in durante) / len(durante)
    folga = 100 - base_pct
    if base_pct + esperado_sub > 95:
        res.add(G, f"CPU sobe ≈ {esperado_sub:.0f} p.p. com {n}/{k} núcleos ocupados",
                f"+{esperado_sub:.0f} p.p.", f"base {base_pct:.1f}% → {med:.1f}%", "±10 p.p.", None,
                f"máquina sem folga (base {base_pct:.1f}%): a subida não cabe; a precisão é provada pela "
                "comparação com /proc/stat acima")
    else:
        sub = med - base_pct
        res.add(G, f"CPU sobe ≈ {esperado_sub:.0f} p.p. com {n}/{k} núcleos ocupados",
                f"+{esperado_sub:.0f} p.p. (folga {folga:.0f})", f"+{sub:.1f} p.p. ({base_pct:.1f}%→{med:.1f}%)",
                "±10 p.p.", abs(sub - esperado_sub) <= 10)
    depois = coletor.esperar_novas(3, timeout=60)
    if depois:
        pos = depois[-1]["cpu"]["pct"]
        res.add(G, "CPU volta depois da carga", f"≈ base {base_pct:.1f}%", f"{pos:.1f}%",
                "±15 p.p. (ruído da máquina)", abs(pos - base_pct) <= 15 if base_pct < 80 else None)


def checar_disco(res, amostrador, amostra):
    esperadas = verdade.montagens_esperadas()
    painel = {d["mount"]: d for d in amostra["disks"]}
    exp_set = set(esperadas.values())
    res.add(G, "Disco: montagens exibidas = 1 por dispositivo real", sorted(exp_set), sorted(painel),
            "conjunto igual", set(painel) == exp_set,
            "subvolumes/bind mounts do mesmo device devem aparecer uma vez só")
    todas = verdade.montagens_de_disco()
    devs_painel = {}
    for origem, ponto, _ in todas:
        if ponto in painel:
            devs_painel.setdefault(origem, []).append(ponto)
    duplas = {k: v for k, v in devs_painel.items() if len(v) > 1}
    res.add(G, "Disco: sem dupla contagem (btrfs/bind)", "0 devices repetidos", f"{len(duplas)} {duplas or ''}",
            "0", not duplas)
    for ponto, d in sorted(painel.items()):
        t0, t1 = _janela(d.get("ts") or amostra["mem"]["ts"])
        jan = [x for _, x in amostrador.janela(t0, t1) if ponto in x["fs"]]
        if not jan:
            ref = verdade.statvfs(ponto)
            jan = [{"fs": {ponto: ref}}]
        fs = [x["fs"][ponto] for x in jan]
        res.add(G, f"Disco {ponto}: total = statvfs", fs[0]["total"], d.get("total_bytes"), "0 B",
                d.get("total_bytes") == fs[0]["total"])
        us = [f["usado"] for f in fs]
        res.add(G, f"Disco {ponto}: usado = (blocks−bfree)·frsize", f"[{min(us)}, {max(us)}]", d.get("used_bytes"),
                "±32 MiB", min(us) - 32 * MIB <= (d.get("used_bytes") or -1) <= max(us) + 32 * MIB)
        ps = [f["pct"] for f in fs]
        res.add(G, f"Disco {ponto}: % = usado/(usado+disponível) (df)", f"{min(ps):.3f}–{max(ps):.3f}",
                f"{d['pct']:.3f}", "±0,05 p.p.", min(ps) - 0.05 <= d["pct"] <= max(ps) + 0.05)


def _montagem_de(caminho):
    """Ponto de montagem (e origem) de um caminho: o prefixo mais longo em /proc/self/mounts."""
    caminho = os.path.realpath(caminho)
    melhor = ("", "")
    with open("/proc/self/mounts") as f:
        for linha in f:
            p = linha.split()
            ponto = p[1].replace("\\040", " ")
            if (caminho == ponto or caminho.startswith(ponto.rstrip("/") + "/")) and len(ponto) > len(melhor[1]):
                melhor = (p[0], ponto)
    return melhor


def checar_disco_delta(res, amostrador, coletor, diretorio, tamanho=2 << 30):
    os.makedirs(diretorio, exist_ok=True)
    origem, _ = _montagem_de(diretorio)
    rep = verdade.montagens_esperadas().get(origem)
    if not rep:
        res.add(G, "Disco: escrita conhecida move 'usado'", "diretório num disco monitorado", diretorio, "-", None,
                f"{diretorio} está em {origem} (não é disco exibido; ex.: tmpfs)")
        return
    if rep not in amostrador.pontos:
        amostrador.pontos.append(rep)
    arq = os.path.join(diretorio, f"validacao-{int(time.time())}.bin")

    def usado_painel_e_verdade():
        a = coletor.esperar_novas(1, timeout=40)
        if not a:
            return None
        d = next((x for x in a[-1]["disks"] if x["mount"] == rep), None)
        if not d:
            return None
        t0, t1 = _janela(d["ts"])
        jan = [x["fs"][rep]["usado"] for _, x in amostrador.janela(t0, t1) if rep in x["fs"]]
        return d["used_bytes"], (sum(jan) / len(jan) if jan else verdade.statvfs(rep)["usado"])

    antes = usado_painel_e_verdade()
    try:
        fd = os.open(arq, os.O_CREAT | os.O_WRONLY, 0o600)
        try:
            os.posix_fallocate(fd, 0, tamanho)
            os.fsync(fd)
        finally:
            os.close(fd)
        os.sync()
        time.sleep(35)  # commit do btrfs (até 30 s) + 1 ciclo do agente
        os.sync()
        durante = usado_painel_e_verdade()
    finally:
        try:
            os.remove(arq)
        except OSError:
            pass
    os.sync()
    time.sleep(35)
    os.sync()
    depois = usado_painel_e_verdade()
    if not (antes and durante and depois):
        res.add(G, "Disco: escrita conhecida", "amostras", "faltaram", "-", None)
        return
    dp, dv = durante[0] - antes[0], durante[1] - antes[1]
    gib = tamanho / (1 << 30)
    res.add(G, f"Disco {rep}: +{gib:.0f} GiB (fallocate) aparece em 'usado'", f"+{tamanho} B (verdade: +{dv:.0f})",
            f"+{dp} B", "±10% do arquivo; ±64 MiB da verdade",
            abs(dp - tamanho) <= tamanho * 0.10 and abs(dp - dv) <= 64 * MIB,
            "ruído de escrita da própria máquina entra na verdade e no painel igualmente")
    rp, rv = depois[0] - antes[0], depois[1] - antes[1]
    res.add(G, f"Disco {rep}: apagar o arquivo devolve o espaço", f"≈ verdade ({rv:+.0f} B)", f"{rp:+d} B",
            "±64 MiB da verdade", abs(rp - rv) <= 64 * MIB)


def checar_procs_uptime(res, amostrador, amostras):
    n = ok_p = ok_u = 0
    piores = []
    for a in amostras:
        t0, t1 = _janela(a["mem"]["ts"])
        jan = amostrador.janela(t0, t1)
        if not jan or a.get("procs") is None:
            continue
        n += 1
        ps = [d["pids"] for _, d in jan]
        if min(ps) - 10 <= a["procs"] <= max(ps) + 10:
            ok_p += 1
        else:
            piores.append(f"painel={a['procs']} verdade=[{min(ps)},{max(ps)}]")
        ups = [d["uptime"] for _, d in jan]
        if min(ups) - 2 <= a["uptime_secs"] <= max(ups) + 2:
            ok_u += 1
    if n:
        res.add(G, "Processos = nº de PIDs em /proc", f"{n}/{n}", f"{ok_p}/{n}", "±10 (processos nascem e morrem)",
                ok_p >= n - 1, "; ".join(piores[:3]))
        res.add(G, "Uptime = /proc/uptime", f"{n}/{n}", f"{ok_u}/{n}", "±2 s", ok_u == n)


def _query(painel, metric, host, desde, ate, step, agg):
    return painel.req("POST", "/api/query", {"metric": metric, "filters": {"host": host}, "from": _iso(desde),
                                             "to": _iso(ate), "step": step, "agg": agg})


def checar_graficos(res, painel, host, amostras):
    """Os gráficos (rollup de 1 min, agg avg) têm de ser a média EXATA das amostras
    que o cartão mostrou naquele minuto."""
    if len(amostras) < 8:
        res.add(G, "Gráficos = média das amostras", "≥8 amostras", len(amostras), "-", None)
        return
    ate = time.time()
    desde = ate - 3600  # janela padrão da tela (1 h → step 60)
    alvos = [("system.memory.utilization", lambda a: a["mem"]["pct"], lambda a: a["mem"]["ts"]),
             ("system.cpu.utilization.total", lambda a: a["cpu"]["pct"], lambda a: a["cpu"]["ts"])]
    for metric, val, ts in alvos:
        r = _query(painel, metric, host, desde, ate, 60, "avg")
        if not r["series"]:
            res.add(G, f"Gráfico {metric}", "série", "vazia", "-", False)
            continue
        serie = dict(zip(r["ts"], r["series"][0]["values"]))
        por_balde = {}
        for a in amostras:
            por_balde.setdefault(int(ts(a)) // 60 * 60, []).append(val(a))
        primeiro, ultimo = min(por_balde), max(por_balde)
        n = ok = 0
        det = []
        for b, vs in por_balde.items():
            if b in (primeiro, ultimo) or len(vs) < 5 or serie.get(b) is None:
                continue  # baldes nas bordas da coleta estão incompletos
            n += 1
            esp = sum(vs) / len(vs)
            if abs(serie[b] - esp) <= 1e-6 * max(1, abs(esp)):
                ok += 1
            else:
                det.append(f"balde {b}: gráfico={serie[b]:.4f} média={esp:.4f} (n={len(vs)})")
        res.add(G, f"Gráfico {metric} (1 min, avg) = média das amostras do cartão", f"{n}/{n} baldes",
                f"{ok}/{n}", "1e-6", n > 0 and ok == n, "; ".join(det[:3]) or (f"tabela {r.get('table')}"))
    if amostras[-1]["disks"]:
        r = _query(painel, "system.filesystem.utilization", host, desde, ate, 60, "avg")
        montagens = sorted({s["labels"].get("mount") for s in r["series"]})
        esperadas = sorted(d["mount"] for d in amostras[-1]["disks"])
        res.add(G, "Gráfico de disco: uma série por montagem exibida", esperadas, montagens, "igual",
                montagens == esperadas)


def checar_rede(res, painel, amostrador, host, amostras):
    if len(amostras) < 12:
        res.add(G, "Rede", "≥12 amostras", len(amostras), "-", None)
        return
    ifs = amostrador.ifaces
    ate, desde = time.time(), amostras[0]["mem"]["ts"] - 120
    # 1) contador cru (tabela crua, step 10 → 1 amostra por balde) = /proc/net/dev
    n = ok = 0
    det = []
    for metric, campo in (("system.network.io.bytes_recv", "rx"), ("system.network.io.bytes_sent", "tx")):
        r = _query(painel, metric, host, desde, ate, 10, "max")
        if not r["series"]:
            continue
        serie = dict(zip(r["ts"], r["series"][0]["values"]))
        for a in amostras:
            t = a["mem"]["ts"]
            v = serie.get(t // 10 * 10)
            if v is None:
                continue
            jan = amostrador.janela(*_janela(t))
            if not jan:
                continue
            vs = [d[campo] for _, d in jan]
            n += 1
            margem = 64 << 10
            if min(vs) - margem <= v <= max(vs) + margem:
                ok += 1
            else:
                det.append(f"{campo} ts={t}: painel={v:.0f} verdade=[{min(vs)},{max(vs)}]")
    res.add(G, f"Rede: contador = soma de /proc/net/dev em {ifs}", f"{n}/{n}", f"{ok}/{n}",
            "variação na janela do instante + 64 KiB", n > 0 and ok >= n - 1, "; ".join(det[:3]))
    # 2) taxa do gráfico (agg=rate, step 60) = Δcontador da verdade entre as últimas
    #    amostras de baldes vizinhos
    ts_amostras = sorted(a["mem"]["ts"] for a in amostras)
    ultimo_do_balde = {}
    for t in ts_amostras:
        ultimo_do_balde[t // 60 * 60] = t
    n = ok = 0
    det = []
    for metric, campo in (("system.network.io.bytes_recv", "rx"), ("system.network.io.bytes_sent", "tx")):
        r = _query(painel, metric, host, ate - 3600, ate, 60, "rate")
        if not r["series"]:
            continue
        serie = dict(zip(r["ts"], r["series"][0]["values"]))
        baldes = sorted(ultimo_do_balde)
        for b_ant, b in zip(baldes, baldes[1:]):
            if b - b_ant != 60 or b == baldes[-1] or serie.get(b) is None:
                continue
            ta, tb = ultimo_do_balde[b_ant], ultimo_do_balde[b]
            fa = amostrador.faixa(*_janela(ta), lambda d: d[campo])
            fb = amostrador.faixa(*_janela(tb), lambda d: d[campo])
            if not fa or not fb:
                continue
            lo, hi = (fb[0] - fa[1]) / 60, (fb[1] - fa[0]) / 60
            n += 1
            if lo * 0.98 - 1024 <= serie[b] <= hi * 1.02 + 1024:
                ok += 1
            else:
                det.append(f"{campo} balde {b}: gráfico={serie[b]:.0f} B/s verdade=[{lo:.0f},{hi:.0f}] B/s")
    res.add(G, "Rede: gráfico de taxa (B/s) = Δ/proc/net/dev", f"{n}/{n}", f"{ok}/{n}",
            "faixa do instante ±2% + 1 KiB/s",
            n > 0 and ok >= n - 1 if n else None, "; ".join(det[:3]))
    # 3) cartão "Rede, recebendo" = taxa média dos últimos 5 min
    ult = amostras[-1]
    hist = amostrador.janela(ult["mem"]["ts"] - 300, ult["mem"]["ts"] + 1)
    if hist and hist[-1][0] - hist[0][0] >= 280 and ult["net"].get("rx_bps") is not None:
        esp = (hist[-1][1]["rx"] - hist[0][1]["rx"]) / (hist[-1][0] - hist[0][0])
        v = ult["net"]["rx_bps"]
        res.add(G, "Rede: cartão 'recebendo' = taxa média de 5 min", f"{esp:.0f} B/s", f"{v:.0f} B/s",
                "±10% + 2 KiB/s", abs(v - esp) <= 0.10 * esp + 2048)
    else:
        res.add(G, "Rede: cartão 'recebendo' = taxa média de 5 min", "≥5 min de verdade amostrada",
                "histórico curto", "-", None, "rode com --duracao-host ≥ 300 para cobrir")


def checar_load(res, painel, amostrador, host, amostras):
    ate = time.time()
    desde = amostras[0]["mem"]["ts"] - 30
    r = _query(painel, "system.cpu.load_average.1m", host, desde, ate, 10, "max")
    if not r["series"]:
        res.add(G, "Load average 1m", "série", "vazia", "-", False)
        return
    serie = dict(zip(r["ts"], r["series"][0]["values"]))
    n = ok = 0
    for a in amostras:
        t = a["mem"]["ts"]
        v = serie.get(t // 10 * 10)
        jan = amostrador.janela(*_janela(t))
        if v is None or not jan:
            continue
        ls = [d["load"][0] for _, d in jan]
        n += 1
        if min(ls) - 0.05 <= v <= max(ls) + 0.05:
            ok += 1
    res.add(G, "Load average 1m = /proc/loadavg", f"{n}/{n}", f"{ok}/{n}", "±0,05", n > 0 and ok == n)


def executar(res, painel, host, args):
    print("\n== host: amostrando verdade e painel ==", flush=True)
    pontos = sorted(set(verdade.montagens_esperadas().values()))
    amostrador = verdade.Amostrador(pontos=pontos).iniciar()
    coletor = ColetorPainel(painel, host).iniciar()
    t_ini = time.time()
    try:
        time.sleep(max(60, args.duracao_host // 2))
        repouso = coletor.desde(t_ini)
        checar_ram(res, amostrador, repouso)
        checar_cpu(res, amostrador, repouso)
        checar_procs_uptime(res, amostrador, repouso)
        if repouso:
            checar_disco(res, amostrador, repouso[-1])
        if not args.sem_carga:
            checar_carga_conhecida(res, amostrador, coletor)
        if not args.sem_disco:
            checar_disco_delta(res, amostrador, coletor, args.dir_disco)
        restante = args.duracao_host - (time.time() - t_ini)
        if restante > 0:
            time.sleep(restante)
        todas = coletor.desde(t_ini)
        checar_graficos(res, painel, host, todas)
        checar_rede(res, painel, amostrador, host, todas)
        checar_load(res, painel, amostrador, host, todas)
    finally:
        coletor.parar()
        amostrador.parar()
