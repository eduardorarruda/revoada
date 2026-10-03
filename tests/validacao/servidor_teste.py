"""Servidor HTTP(S) de teste que REGISTRA cada requisição recebida.

É o alvo das verificações de site e das jornadas: o registro (método, caminho,
query, cabeçalhos, corpo, instante) é a prova independente de que o painel bateu
no endpoint certo, com o método e o corpo certos, no intervalo certo.

Rotas (todas sob /v/<prefixo>/, para isolar execuções):
  ok              200, corpo com a palavra PALAVRA-OK
  erro500         500
  nao-existe      404
  redir1          301 -> redir2 -> 302 -> ok
  lento3          200 depois de 3 s
  lento25         200 depois de 25 s (acima do timeout padrão de 20 s)
  sem-palavra     200 sem a palavra-chave
  laco            302 para si mesma (laço de redirect)
  flip            200 ou 500 conforme /controle?flip=200|500
  webhook         recebe POSTs de alerta (canal webhook do painel)
  j/inicio, j/login, j/painel, j/login-quebrado   — roteiro de jornada
"""
import json
import os
import shutil
import ssl
import subprocess
import threading
import time
import urllib.parse
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


class Registro:
    def __init__(self):
        self.reqs = []
        self.webhooks = []
        self.flip = 200
        self._mu = threading.Lock()

    def add(self, item):
        with self._mu:
            self.reqs.append(item)

    def de(self, caminho_sufixo):
        with self._mu:
            return [r for r in self.reqs if r["caminho"].endswith(caminho_sufixo)]


def _manipulador(reg, prefixo, esquema):
    base = f"/v/{prefixo}/"

    class H(BaseHTTPRequestHandler):
        protocol_version = "HTTP/1.1"

        def log_message(self, *a):
            pass

        def _corpo(self):
            n = int(self.headers.get("Content-Length") or 0)
            return self.rfile.read(n).decode("utf-8", "replace") if n else ""

        def _resp(self, status, corpo="", cab=None):
            dados = corpo.encode()
            self.send_response(status)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(dados)))
            for k, v in (cab or {}).items():
                self.send_header(k, v)
            self.end_headers()
            try:
                self.wfile.write(dados)
            except (BrokenPipeError, ConnectionResetError):
                pass

        def _tratar(self, metodo):
            t = time.time()
            u = urllib.parse.urlsplit(self.path)
            corpo = self._corpo()
            reg.add({"t": t, "esquema": esquema, "metodo": metodo, "caminho": u.path, "query": u.query,
                     "cab": {k.lower(): v for k, v in self.headers.items()}, "corpo": corpo})
            if not u.path.startswith(base):
                return self._resp(404, "fora do prefixo")
            rota = u.path[len(base):]
            if rota == "ok":
                return self._resp(200, "<html><body>tudo certo PALAVRA-OK</body></html>")
            if rota == "erro500":
                return self._resp(500, "erro interno simulado")
            if rota == "nao-existe":
                return self._resp(404, "não existe")
            if rota == "redir1":
                return self._resp(301, "", {"Location": base + "redir2"})
            if rota == "redir2":
                return self._resp(302, "", {"Location": base + "ok"})
            if rota == "lento3":
                time.sleep(3)
                return self._resp(200, "lento mas certo PALAVRA-OK")
            if rota == "lento25":
                time.sleep(25)
                return self._resp(200, "tarde demais")
            if rota == "sem-palavra":
                return self._resp(200, "<html>página sem a palavra</html>")
            if rota == "laco":
                return self._resp(302, "", {"Location": base + "laco"})
            if rota == "flip":
                return self._resp(reg.flip, "flip PALAVRA-OK" if reg.flip == 200 else "flip falhando")
            if rota == "controle":
                q = urllib.parse.parse_qs(u.query)
                if "flip" in q:
                    reg.flip = int(q["flip"][0])
                return self._resp(200, "ok")
            if rota == "webhook":
                try:
                    reg.webhooks.append({"t": t, "msg": json.loads(corpo)})
                except json.JSONDecodeError:
                    reg.webhooks.append({"t": t, "msg": corpo})
                return self._resp(200, "{}")
            if rota == "j/inicio":
                return self._resp(200, "<form>Bem-vindo, entre</form>", {"Set-Cookie": "sessao=s1; Path=/"})
            if rota == "j/login":
                f = urllib.parse.parse_qs(corpo)
                tem_cookie = "sessao=s1" in (self.headers.get("Cookie") or "")
                if metodo == "POST" and f.get("usuario") == ["ana"] and f.get("senha") == ["s3"] and tem_cookie:
                    return self._resp(302, "", {"Location": base + "j/painel", "Set-Cookie": "logado=ana; Path=/"})
                return self._resp(401, "credenciais erradas")
            if rota == "j/login-quebrado":
                return self._resp(500, "login fora do ar")
            if rota == "j/painel":
                if "logado=ana" in (self.headers.get("Cookie") or ""):
                    return self._resp(200, "<h1>Olá, ana</h1>")
                return self._resp(403, "sem sessão")
            return self._resp(404, "rota desconhecida")

        def do_GET(self):
            self._tratar("GET")

        def do_POST(self):
            self._tratar("POST")

        def do_HEAD(self):
            self._tratar("HEAD")

    return H


def _gerar_cert(diretorio, nome, expirado):
    """Certificado autoassinado via openssl (stdlib não gera X.509)."""
    if not shutil.which("openssl"):
        return None
    crt, key = os.path.join(diretorio, nome + ".crt"), os.path.join(diretorio, nome + ".key")
    cmd = ["openssl", "req", "-x509", "-newkey", "rsa:2048", "-nodes", "-keyout", key, "-out", crt,
           "-subj", "/CN=127.0.0.1", "-addext", "subjectAltName=IP:127.0.0.1"]
    if expirado:
        cmd += ["-not_before", "20200101000000Z", "-not_after", "20200201000000Z"]
    else:
        cmd += ["-days", "30"]
    r = subprocess.run(cmd, capture_output=True, text=True)
    return (crt, key) if r.returncode == 0 else None


class _Srv(ThreadingHTTPServer):
    daemon_threads = True
    reg = None
    esquema = "http"

    def get_request(self):
        try:
            return super().get_request()
        except ssl.SSLError as e:
            # O cliente recusou o certificado: não há requisição HTTP, mas a TENTATIVA
            # é a prova de que a sonda chegou ao endpoint TLS certo.
            self.reg.add({"t": time.time(), "esquema": self.esquema, "metodo": "-", "caminho": "<tls-recusado>",
                          "porta": self.server_address[1], "query": "", "cab": {}, "corpo": str(e)})
            raise

    def handle_error(self, request, client_address):
        pass


class ServidorTeste:
    def __init__(self, prefixo, porta=0, dir_tmp="/tmp"):
        self.reg = Registro()
        self.prefixo = prefixo
        self.dir_tmp = dir_tmp
        self.servidores = []
        self.http = self._subir(porta, None, "http")
        self.https_auto = self.https_exp = None
        c = _gerar_cert(dir_tmp, "autoassinado", False)
        if c:
            self.https_auto = self._subir(0, c, "https")
        c = _gerar_cert(dir_tmp, "expirado", True)
        if c:
            self.https_exp = self._subir(0, c, "https")

    def _subir(self, porta, cert, esquema):
        srv = _Srv(("127.0.0.1", porta), _manipulador(self.reg, self.prefixo, esquema))
        srv.reg, srv.esquema = self.reg, esquema
        if cert:
            ctx = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
            ctx.load_cert_chain(*cert)
            srv.socket = ctx.wrap_socket(srv.socket, server_side=True)
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        self.servidores.append(srv)
        return srv

    def url(self, rota, srv=None, query=""):
        srv = srv or self.http
        esquema = "https" if srv is not self.http else "http"
        return f"{esquema}://127.0.0.1:{srv.server_address[1]}/v/{self.prefixo}/{rota}" + (f"?{query}" if query else "")

    def parar(self):
        for s in self.servidores:
            s.shutdown()
            s.server_close()
        for nome in ("autoassinado", "expirado"):
            for ext in (".crt", ".key"):
                try:
                    os.remove(os.path.join(self.dir_tmp, nome + ext))
                except OSError:
                    pass
