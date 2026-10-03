"""Cliente HTTP do painel Revoada para a validação (sessão admin com 2FA).

Credenciais NUNCA ficam no repositório: vêm de variáveis de ambiente
(RV_EMAIL, RV_SENHA, RV_TOTP) ou de arquivos indicados na linha de comando
(--senha-arquivo, --totp-arquivo, ou o diretório --dev com senha-admin.txt e
totp-admin.txt, o layout do ambiente de desenvolvimento).
"""
import base64
import hashlib
import hmac
import http.cookiejar
import json
import os
import struct
import time
import urllib.error
import urllib.parse
import urllib.request


def totp(segredo, instante=None):
    """Código TOTP (RFC 6238, SHA-1, 30 s, 6 dígitos)."""
    chave = base64.b32decode(segredo.strip().upper() + "=" * (-len(segredo.strip()) % 8))
    contador = int((instante or time.time()) // 30)
    h = hmac.new(chave, struct.pack(">Q", contador), hashlib.sha1).digest()
    o = h[-1] & 15
    return "%06d" % ((struct.unpack(">I", h[o:o + 4])[0] & 0x7FFFFFFF) % 1000000)


def _ler(caminho):
    with open(caminho) as f:
        return f.read().strip()


class Credenciais:
    def __init__(self, email, senha, segredo_totp, arquivo_ultimo_codigo):
        self.email = email
        self.senha = senha
        self.segredo_totp = segredo_totp
        self.arquivo_ultimo_codigo = arquivo_ultimo_codigo

    @classmethod
    def de_args(cls, args):
        dev = args.dev
        email = os.environ.get("RV_EMAIL") or args.email
        senha = os.environ.get("RV_SENHA")
        if not senha:
            arq = args.senha_arquivo or (dev and os.path.join(dev, "senha-admin.txt"))
            senha = _ler(arq) if arq else None
        segredo = os.environ.get("RV_TOTP")
        if not segredo:
            arq = args.totp_arquivo or (dev and os.path.join(dev, "totp-admin.txt"))
            segredo = _ler(arq) if arq and os.path.exists(arq) else None
        if not senha:
            raise SystemExit("sem senha: defina RV_SENHA, --senha-arquivo ou --dev")
        ultimo = os.path.join(dev, ".ultimo-totp") if dev else os.path.join(args.saida_dir, ".ultimo-totp")
        return cls(email, senha, segredo, ultimo)

    def codigo(self):
        """Próximo código TOTP que ainda não foi usado (o painel recusa reuso)."""
        if not self.segredo_totp:
            raise RuntimeError("o painel pediu 2FA e não há segredo TOTP (RV_TOTP/--totp-arquivo)")
        c = totp(self.segredo_totp)
        anterior = ""
        if os.path.exists(self.arquivo_ultimo_codigo):
            anterior = _ler(self.arquivo_ultimo_codigo)
        if anterior == c:
            time.sleep(31 - time.time() % 30)
            c = totp(self.segredo_totp)
        with open(self.arquivo_ultimo_codigo, "w") as f:
            f.write(c)
        return c


class Painel:
    """Chama as MESMAS rotas que o front (web/src/api.ts) chama."""

    def __init__(self, base, cred):
        self.base = base.rstrip("/")
        self.cred = cred
        self.jar = http.cookiejar.CookieJar()
        self.op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))
        self.token = None

    def req(self, metodo, caminho, corpo=None, ok=(200, 201, 202, 204), timeout=60):
        dados = json.dumps(corpo).encode() if corpo is not None else None
        r = urllib.request.Request(self.base + caminho, data=dados, method=metodo)
        r.add_header("Content-Type", "application/json")
        if self.token:
            r.add_header("Authorization", "Bearer " + self.token)
        try:
            resp = self.op.open(r, timeout=timeout)
            st, txt, hdr = resp.status, resp.read().decode(), resp.headers
        except urllib.error.HTTPError as e:
            st, txt, hdr = e.code, e.read().decode(), e.headers
        if hdr.get("X-Access-Token"):
            self.token = hdr["X-Access-Token"]
        if st == 401 and caminho != "/api/auth/login" and not getattr(self, "_reentrando", False):
            self._reentrando = True
            try:
                self.entrar()
            finally:
                self._reentrando = False
            return self.req(metodo, caminho, corpo, ok, timeout)
        if st not in ok:
            raise RuntimeError(f"{metodo} {caminho} -> {st}: {txt[:300]}")
        try:
            return json.loads(txt) if txt.strip() else None
        except json.JSONDecodeError:
            return txt

    def get(self, caminho, **params):
        if params:
            caminho += "?" + urllib.parse.urlencode({k: v for k, v in params.items() if v is not None})
        return self.req("GET", caminho)

    def entrar(self):
        r = self.req("POST", "/api/auth/login", {"username": self.cred.email, "password": self.cred.senha})
        if r and r.get("mfa_necessario"):
            self.req("POST", "/api/auth/mfa/verificar", {"desafio": r["desafio"], "codigo": self.cred.codigo()})
        return self
