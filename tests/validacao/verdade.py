"""Verdade de referência lida direto do kernel (/proc, /sys, statvfs).

Nada aqui reaproveita código do agente: é uma segunda implementação, independente,
das mesmas medidas — se as duas concordam, o número do painel é a medida real.

O Amostrador grava um instantâneo a cada `passo` segundos numa thread. As
comparações usam a JANELA de amostras em volta do instante que o painel informa
(o `ts` da API tem resolução de 1 s), e a tolerância inclui a variação da própria
verdade nessa janela: se a RAM oscilou 40 MiB naquele segundo, exigir 1 MiB de
acerto seria cobrar do agente uma precisão que o relógio não dá.
"""
import bisect
import os
import threading
import time

# Filesystems que são disco de verdade (o resto é pseudo/efêmero: tmpfs, overlay...).
FS_DE_DISCO = {"ext2", "ext3", "ext4", "xfs", "btrfs", "vfat", "exfat", "ntfs", "ntfs3", "fuseblk",
               "f2fs", "zfs", "jfs", "reiserfs", "bcachefs", "hfsplus", "apfs"}


def ler_stat_cpu():
    """(ocupado, total) em jiffies, somando núcleos. Mesma semântica do `top`:
    ocioso = idle + iowait; guest já está contido em user (por isso não soma)."""
    with open("/proc/stat") as f:
        campos = f.readline().split()[1:]
    v = [int(x) for x in campos[:8]] + [0] * max(0, 8 - len(campos))
    user, nice, system, idle, iowait, irq, softirq, steal = v[:8]
    total = user + nice + system + idle + iowait + irq + softirq + steal
    return total - idle - iowait, total, steal


def ler_meminfo():
    out = {}
    with open("/proc/meminfo") as f:
        for linha in f:
            k, _, resto = linha.partition(":")
            partes = resto.split()
            if partes:
                out[k] = int(partes[0]) * (1024 if len(partes) > 1 else 1)
    return out


def interfaces_fisicas():
    """Interfaces com dispositivo de hardware por trás (/sys/class/net/X/device).
    Critério independente do agente, que exclui por prefixo de nome."""
    base = "/sys/class/net"
    try:
        return sorted(n for n in os.listdir(base) if os.path.exists(os.path.join(base, n, "device")))
    except OSError:
        return []


def ler_net(ifaces):
    rx = tx = 0
    with open("/proc/net/dev") as f:
        for linha in f.readlines()[2:]:
            nome, _, dados = linha.partition(":")
            if nome.strip() in ifaces:
                d = dados.split()
                rx += int(d[0])
                tx += int(d[8])
    return rx, tx


def contar_pids():
    return sum(1 for n in os.listdir("/proc") if n.isdigit())


def montagens_de_disco():
    """[(origem, ponto, fstype)] das montagens de disco real, de /proc/self/mounts."""
    out = []
    with open("/proc/self/mounts") as f:
        for linha in f:
            p = linha.split()
            if len(p) < 3:
                continue
            origem, ponto, fstype = p[0], p[1].replace("\\040", " "), p[2]
            if fstype in FS_DE_DISCO and not origem.startswith("/dev/loop"):
                out.append((origem, ponto, fstype))
    return out


def montagens_esperadas():
    """Uma montagem por dispositivo (a de caminho mais curto) — subvolume btrfs e
    bind mount do mesmo device são o MESMO disco e não podem ser somados duas vezes."""
    por_dev = {}
    for origem, ponto, _ in montagens_de_disco():
        atual = por_dev.get(origem)
        if atual is None or (len(ponto), ponto) < (len(atual), atual):
            por_dev[origem] = ponto
    return por_dev


def statvfs(ponto):
    s = os.statvfs(ponto)
    total = s.f_blocks * s.f_frsize
    usado = (s.f_blocks - s.f_bfree) * s.f_frsize
    disp = s.f_bavail * s.f_frsize
    pct = usado / (usado + disp) * 100 if usado + disp else 0.0
    return {"total": total, "usado": usado, "disponivel": disp, "pct": pct}


class Amostrador:
    def __init__(self, passo=0.25, pontos=None):
        self.passo = passo
        self.pontos = list(pontos or [])
        self.ifaces = interfaces_fisicas()
        self.amostras = []  # (t, dict)
        self._mu = threading.Lock()
        self._parar = threading.Event()
        self._th = None

    def _instantaneo(self):
        t = time.time()
        ocupado, total, steal = ler_stat_cpu()
        mem = ler_meminfo()
        with open("/proc/loadavg") as f:
            la = [float(x) for x in f.read().split()[:3]]
        with open("/proc/uptime") as f:
            up = float(f.read().split()[0])
        rx, tx = ler_net(self.ifaces)
        d = {"cpu_ocupado": ocupado, "cpu_total": total, "cpu_steal": steal, "mem": mem, "load": la,
             "uptime": up, "rx": rx, "tx": tx, "pids": contar_pids(), "fs": {}}
        for p in self.pontos:
            try:
                d["fs"][p] = statvfs(p)
            except OSError:
                pass
        return (t + time.time()) / 2, d

    def _laco(self):
        while not self._parar.is_set():
            try:
                a = self._instantaneo()
                with self._mu:
                    self.amostras.append(a)
                    if len(self.amostras) > 20000:
                        del self.amostras[:5000]
            except Exception as e:  # noqa: BLE001 — a amostragem não pode matar a validação
                print("amostrador:", e, flush=True)
            self._parar.wait(self.passo)

    def iniciar(self):
        self._th = threading.Thread(target=self._laco, daemon=True)
        self._th.start()
        time.sleep(self.passo * 3)
        return self

    def parar(self):
        self._parar.set()
        if self._th:
            self._th.join(5)

    def janela(self, t0, t1, folga_max=0.75):
        """Amostras em [t0, t1]. Devolve [] se a janela não estiver COBERTA (o
        amostrador ficou sem rodar — máquina saturada — e a verdade daquele trecho
        é desconhecida): sem isso, um buraco na verdade viraria "o painel errou"."""
        with self._mu:
            ts = [a[0] for a in self.amostras]
            i, j = bisect.bisect_left(ts, t0), bisect.bisect_right(ts, t1)
            dentro = self.amostras[i:j]
        if not dentro:
            return []
        pontos = [t0] + [a[0] for a in dentro] + [t1]
        if max(b - a for a, b in zip(pontos, pontos[1:])) > folga_max:
            return []
        return dentro

    def mais_proxima(self, t):
        with self._mu:
            if not self.amostras:
                return None
            ts = [a[0] for a in self.amostras]
            i = bisect.bisect_left(ts, t)
            cands = [self.amostras[k] for k in (i - 1, i) if 0 <= k < len(self.amostras)]
            return min(cands, key=lambda a: abs(a[0] - t))

    def faixa(self, t0, t1, f):
        """(mín, máx) de f(amostra) na janela; None se não houver amostra."""
        vs = [f(d) for _, d in self.janela(t0, t1)]
        return (min(vs), max(vs)) if vs else None

    def cpu_entre(self, ta, tb):
        """% de CPU ocupada entre os instantes ta e tb (amostras mais próximas)."""
        a, b = self.mais_proxima(ta), self.mais_proxima(tb)
        if not a or not b or b[1]["cpu_total"] <= a[1]["cpu_total"]:
            return None
        dt = b[1]["cpu_total"] - a[1]["cpu_total"]
        return 100.0 * (b[1]["cpu_ocupado"] - a[1]["cpu_ocupado"]) / dt
