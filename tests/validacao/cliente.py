"""Cliente HTTP do painel Revoada para a validação (sessão admin com 2FA).

Credenciais NUNCA ficam no repositório: vêm de variáveis de ambiente
(RV_EMAIL, RV_SENHA, RV_TOTP) ou de arquivos indicados na linha de comando
(--senha-arquivo, --totp-arquivo, ou o diretório --dev com senha-admin.txt e
totp-admin.txt, o layout do ambiente de desenvolvimento).

Também tem um cliente mínimo do MCP (Streamable HTTP em JSON, o /mcp do painel).
"""
import base64
import hashlib
import hmac
import http.cookiejar
import json
import os
import struct
import threading
import time
import urllib.error
import urllib.parse
import urllib.request

PERIODO_TOTP = 30
# Rotas em que um 401 NÃO dispara um novo login: são o próprio login, ou um 401 nelas
# quer dizer "código/senha não confere" — logar de novo não resolve e gastaria código.
ROTAS_SEM_RELOGIN = frozenset({"/api/auth/login", "/api/auth/mfa/verificar", "/api/auth/reautenticar",
                               "/api/auth/mfa/iniciar", "/api/auth/mfa/confirmar"})
# Rotas atrás do balde POR IP do painel (httpapi.go: newIPRateLimiter(10, time.Minute),
# janela fixa de 60 s, compartilhada entre login, 2ª etapa, reautenticação, ativação do 2FA,
# refresh e cadastro). Num 429 a requisição não chegou ao handler: espera a janela virar.
ROTAS_LIMITADAS_POR_IP = frozenset({"/api/auth/login", "/api/auth/mfa/verificar", "/api/auth/reautenticar",
                                    "/api/auth/mfa/confirmar", "/api/auth/refresh", "/api/auth/register"})
ESPERA_LIMITE_IP = 61
TENTATIVAS_LIMITE_IP = 2


def totp(segredo, instante=None):
    """Código TOTP (RFC 6238, SHA-1, 30 s, 6 dígitos)."""
    chave = base64.b32decode(segredo.strip().upper() + "=" * (-len(segredo.strip()) % 8))
    contador = int((instante or time.time()) // PERIODO_TOTP)
    h = hmac.new(chave, struct.pack(">Q", contador), hashlib.sha1).digest()
    o = h[-1] & 15
    return "%06d" % ((struct.unpack(">I", h[o:o + 4])[0] & 0x7FFFFFFF) % 1000000)


def _ler(caminho):
    with open(caminho) as f:
        return f.read().strip()


def _rota(caminho):
    return caminho.split("?", 1)[0]


def _decodificar(txt):
    try:
        return json.loads(txt) if txt.strip() else None
    except json.JSONDecodeError:
        return txt


class Credenciais:
    # Um lock para o processo todo: os grupos rodam em threads e não podem gastar o
    # mesmo código (o painel recusa reuso do passo TOTP).
    _mu = threading.Lock()

    def __init__(self, email, senha, segredo_totp, arquivo_ultimo_codigo):
        self.email = email
        self.senha = senha
        self.segredo_totp = segredo_totp
        # None = guardar o último passo só na memória (usuário descartável da validação).
        self.arquivo_ultimo_codigo = arquivo_ultimo_codigo
        self._ultimo_passo = 0

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
        with Credenciais._mu:
            if self.arquivo_ultimo_codigo is None:
                return self._codigo_em_memoria()
            c = totp(self.segredo_totp)
            anterior = ""
            if os.path.exists(self.arquivo_ultimo_codigo):
                anterior = _ler(self.arquivo_ultimo_codigo)
            if anterior == c:
                time.sleep(PERIODO_TOTP + 1 - time.time() % PERIODO_TOTP)
                c = totp(self.segredo_totp)
            with open(self.arquivo_ultimo_codigo, "w") as f:
                f.write(c)
            return c

    def _codigo_em_memoria(self):
        """Guarda o último PASSO usado e usa o seguinte enquanto ele couber na janela de
        ±1 passo que o servidor aceita (core/seguranca/totp: Janela = 1). Assim ativar o
        2FA e reautenticar logo depois não exige esperar 30 s."""
        while True:
            agora = int(time.time() // PERIODO_TOTP)
            passo = max(agora, self._ultimo_passo + 1)
            if passo <= agora + 1:
                self._ultimo_passo = passo
                return totp(self.segredo_totp, passo * PERIODO_TOTP)
            time.sleep(PERIODO_TOTP + 1 - time.time() % PERIODO_TOTP)


class Painel:
    """Chama as MESMAS rotas que o front (web/src/api.ts) chama."""

    def __init__(self, base, cred):
        self.base = base.rstrip("/")
        self.cred = cred
        self.jar = http.cookiejar.CookieJar()
        self.op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(self.jar))
        self.token = None

    def _enviar(self, metodo, caminho, corpo, timeout):
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
        return st, txt

    def _com_limite(self, metodo, caminho, corpo, timeout):
        """corpo pode ser uma função: ela é chamada a cada tentativa (um código TOTP
        gerado antes de um 429 já estaria velho na tentativa seguinte)."""
        for tentativa in range(TENTATIVAS_LIMITE_IP + 1):
            st, txt = self._enviar(metodo, caminho, corpo() if callable(corpo) else corpo, timeout)
            if st != 429 or _rota(caminho) not in ROTAS_LIMITADAS_POR_IP or tentativa == TENTATIVAS_LIMITE_IP:
                return st, txt
            print(f"  … {_rota(caminho)}: limite de tentativas por IP do painel (10/min); esperando {ESPERA_LIMITE_IP} s",
                  flush=True)
            time.sleep(ESPERA_LIMITE_IP)
        return st, txt

    def bruto(self, metodo, caminho, corpo=None, timeout=60, relogar=True):
        """Faz a chamada e devolve (status, corpo decodificado) SEM levantar erro por status
        — para os casos que esperam 403/404. Num 401 (token vencido) entra de novo UMA vez."""
        st, txt = self._com_limite(metodo, caminho, corpo, timeout)
        if st == 401 and relogar and _rota(caminho) not in ROTAS_SEM_RELOGIN:
            self.entrar()
            st, txt = self._com_limite(metodo, caminho, corpo, timeout)
        return st, _decodificar(txt)

    def req(self, metodo, caminho, corpo=None, ok=(200, 201, 202, 204), timeout=60):
        st, valor = self.bruto(metodo, caminho, corpo, timeout)
        if st not in ok:
            raise RuntimeError(f"{metodo} {caminho} -> {st}: {str(valor)[:300]}")
        return valor

    def get(self, caminho, **params):
        if params:
            caminho += "?" + urllib.parse.urlencode({k: v for k, v in params.items() if v is not None})
        return self.req("GET", caminho)

    def entrar(self):
        r = self.req("POST", "/api/auth/login", {"username": self.cred.email, "password": self.cred.senha})
        if isinstance(r, dict) and r.get("mfa_necessario"):
            desafio = r["desafio"]
            self.req("POST", "/api/auth/mfa/verificar", lambda: {"desafio": desafio, "codigo": self.cred.codigo()})
        return self

    def reautenticar(self):
        """POST /api/auth/reautenticar: libera as ações críticas (auth.Exige) por 5 min.
        Com 2FA ativo o servidor confere o código do app; sem 2FA, a senha (mfa.go,
        Reautenticar). O novo token chega em X-Access-Token. Devolve valido_ate (unix)."""
        def corpo():
            c = {"senha": self.cred.senha}
            if self.cred.segredo_totp:
                c["codigo"] = self.cred.codigo()
            return c
        st, r = self.bruto("POST", "/api/auth/reautenticar", corpo, relogar=False)
        if st == 401 and "confere" not in str(r):
            self.entrar()  # 401 do RequireAuth (token vencido), não do código
            st, r = self.bruto("POST", "/api/auth/reautenticar", corpo, relogar=False)
        elif st == 401 and self.cred.segredo_totp:
            # o servidor já viu este passo (outro login na mesma janela): UMA tentativa a mais
            # no passo seguinte — cada erro conta para o bloqueio da conta.
            time.sleep(PERIODO_TOTP + 1 - time.time() % PERIODO_TOTP)
            st, r = self.bruto("POST", "/api/auth/reautenticar", corpo, relogar=False)
        if st != 200:
            raise RuntimeError(f"POST /api/auth/reautenticar -> {st}: {str(r)[:200]}")
        return r.get("valido_ate") if isinstance(r, dict) else None


class ErroMCP(RuntimeError):
    def __init__(self, msg, http_status=None):
        super().__init__(msg)
        self.http_status = http_status


class MCP:
    """Cliente mínimo do MCP Streamable HTTP, como o painel o serve (go-sdk v1.8,
    mcpsrv/servidor.go: JSONResponse=true, sessão com estado, Bearer com token MCP).

    O go-sdk exige Content-Type: application/json e Accept com application/json E
    text/event-stream; devolve Mcp-Session-Id no initialize, que vai em toda chamada
    seguinte junto com Mcp-Protocol-Version. Antes de tools/* é preciso mandar a
    notificação notifications/initialized (senão: "invalid during session initialization").
    """
    VERSAO = "2025-06-18"  # servidor com estado não aceita a 2026-07-28 (só stateless)

    def __init__(self, base, token, timeout=60):
        self.url = base.rstrip("/") + "/mcp"
        self.token = token
        self.timeout = timeout
        self.sessao = None
        self.versao = None
        self.seq = 0

    def _cabecalhos(self, r):
        r.add_header("Authorization", "Bearer " + self.token)
        if self.sessao:
            r.add_header("Mcp-Session-Id", self.sessao)
        if self.versao:
            r.add_header("Mcp-Protocol-Version", self.versao)

    def _post(self, msg):
        r = urllib.request.Request(self.url, data=json.dumps(msg).encode(), method="POST")
        r.add_header("Content-Type", "application/json")
        r.add_header("Accept", "application/json, text/event-stream")
        self._cabecalhos(r)
        try:
            with urllib.request.urlopen(r, timeout=self.timeout) as resp:
                return resp.status, resp.headers, resp.read().decode()
        except urllib.error.HTTPError as e:
            return e.code, e.headers, e.read().decode()

    @staticmethod
    def _resposta(hdr, txt, id_):
        tipo = (hdr.get("Content-Type") or "").split(";")[0].strip()
        if tipo != "text/event-stream":
            return json.loads(txt)
        for bloco in txt.replace("\r\n", "\n").split("\n\n"):  # SSE, se o servidor mudar de ideia
            dados = "\n".join(l[5:].lstrip() for l in bloco.splitlines() if l.startswith("data:"))
            if dados:
                m = json.loads(dados)
                if m.get("id") == id_:
                    return m
        raise ErroMCP("resposta SSE sem a mensagem pedida")

    def chamar(self, metodo, params=None):
        self.seq += 1
        st, hdr, txt = self._post({"jsonrpc": "2.0", "id": self.seq, "method": metodo, "params": params or {}})
        if st != 200:
            raise ErroMCP(f"{metodo}: HTTP {st}: {txt[:200]}", st)
        if metodo == "initialize" and hdr.get("Mcp-Session-Id"):
            self.sessao = hdr["Mcp-Session-Id"]
        m = self._resposta(hdr, txt, self.seq)
        if m.get("error"):
            e = m["error"]
            raise ErroMCP(f"{metodo}: erro JSON-RPC {e.get('code')}: {e.get('message')}")
        return m.get("result") or {}

    def notificar(self, metodo):
        st, _, txt = self._post({"jsonrpc": "2.0", "method": metodo})
        if st not in (200, 202, 204):
            raise ErroMCP(f"{metodo}: HTTP {st}: {txt[:200]}", st)

    def iniciar(self):
        r = self.chamar("initialize", {"protocolVersion": self.VERSAO, "capabilities": {},
                                       "clientInfo": {"name": "revoada-validacao", "version": "1.0"}})
        self.versao = r.get("protocolVersion") or self.VERSAO
        self.notificar("notifications/initialized")
        return r

    def ferramentas(self):
        nomes, cursor = [], None
        for _ in range(20):  # paginação (nextCursor); 20 páginas é muito mais que o painel tem
            r = self.chamar("tools/list", {"cursor": cursor} if cursor else {})
            nomes += [t.get("name") for t in r.get("tools") or []]
            cursor = r.get("nextCursor")
            if not cursor:
                break
        return nomes

    def ferramenta(self, nome, argumentos=None):
        """tools/call -> (ok, dados, texto). Erro da ferramenta (escopo, limite) volta como
        resultado com isError=true — o go-sdk embrulha o erro do handler —, não como erro
        JSON-RPC. dados = structuredContent (ou o texto JSON, se só vier ele)."""
        r = self.chamar("tools/call", {"name": nome, "arguments": argumentos or {}})
        texto = " ".join(c.get("text", "") for c in r.get("content") or [] if c.get("type") == "text")
        dados = r.get("structuredContent")
        if dados is None and texto and not r.get("isError"):
            dados = _decodificar(texto)
        return not r.get("isError"), dados, texto

    def encerrar(self):
        if not self.sessao:
            return
        r = urllib.request.Request(self.url, method="DELETE")
        self._cabecalhos(r)
        try:
            with urllib.request.urlopen(r, timeout=10) as resp:
                resp.read()
        except (urllib.error.URLError, OSError):
            pass  # a sessão expira sozinha (SessionTimeout de 30 min)
        self.sessao = None
