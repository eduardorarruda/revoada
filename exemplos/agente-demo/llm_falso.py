"""LLM falso, compatível com a rota /v1/chat/completions da OpenAI.

Serve para rodar a demonstração sem chave de API e sem gastar nada. Ele imita o
comportamento que importa para o painel:

- com ferramentas na requisição e nenhum resultado de ferramenta ainda, pede a
  ferramenta `clima` para a cidade da pergunta;
- se o último resultado da ferramenta diz "tente novamente", pede de novo (é assim
  que um modelo de verdade entra em loop — e o replay do Revoada destaca isso);
- senão, responde em texto;
- qualquer modelo que comece com "modelo-" devolve 500 (erro simulado).

Tokens são uma conta aproximada (4 caracteres ≈ 1 token) para o custo variar de uma
chamada para outra, com uma parte "em cache" quando o histórico é longo — como a API
real informa em usage.prompt_tokens_details.cached_tokens.

Adaptado de tests/genai-fixtures/servidores.py (copiado de propósito: o exemplo tem
que funcionar sozinho, fora do repositório).
"""
import json
import random
import re
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

LATENCIA_S = (0.2, 0.9)


def _tokens(texto):
    return max(1, len(texto) // 4)


def _cidade(mensagens):
    for m in reversed(mensagens):
        if m.get("role") == "user":
            achou = re.search(r"\bem ([A-ZÀ-Ú][\wÀ-ú ]+?)\?", m.get("content") or "")
            if achou:
                return achou.group(1)
    return "São Paulo"


def _ultimo_resultado(mensagens):
    """Texto do último resultado de ferramenta, ou None se ainda não houve nenhum
    depois da última pergunta do usuário."""
    for m in reversed(mensagens):
        if m.get("role") == "user":
            return None
        if m.get("role") == "tool":
            return m.get("content") or ""
    return None


def _resposta(req):
    mensagens = req.get("messages", [])
    resultado = _ultimo_resultado(mensagens)
    n_ferramentas = sum(1 for m in mensagens if m.get("role") == "tool")
    if req.get("tools") and (resultado is None or "tente novamente" in resultado):
        msg = {"role": "assistant", "content": None, "tool_calls": [{
            "id": f"call_{n_ferramentas + 1}", "type": "function",
            "function": {"name": "clima", "arguments": json.dumps({"cidade": _cidade(mensagens)}, ensure_ascii=False)},
        }]}
        return msg, "tool_calls"
    if resultado and "erro" in resultado:
        texto = "Não consegui consultar a estação agora. Tente de novo em alguns minutos."
    elif resultado:
        dados = json.loads(resultado)
        texto = f"Agora faz {dados['graus']} °C em {dados['cidade']}, {dados['ceu']}."
    else:
        texto = "Pode me dizer a cidade?"
    return {"role": "assistant", "content": texto}, "stop"


class LLMFalso(BaseHTTPRequestHandler):
    def log_message(self, *args):
        pass

    def _json(self, codigo, corpo):
        b = json.dumps(corpo, ensure_ascii=False).encode()
        self.send_response(codigo)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b)))
        self.end_headers()
        self.wfile.write(b)

    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0))
        req = json.loads(self.rfile.read(n) or b"{}")
        time.sleep(random.uniform(*LATENCIA_S))
        modelo = req.get("model", "gpt-4o-mini")
        if modelo.startswith("modelo-"):
            return self._json(500, {"error": {"message": "falha simulada do provedor", "type": "server_error"}})
        if not self.path.endswith("/chat/completions"):
            return self._json(404, {"error": {"message": "rota desconhecida " + self.path}})
        msg, fim = _resposta(req)
        entrada = _tokens(json.dumps(req.get("messages", []), ensure_ascii=False) + json.dumps(req.get("tools", [])))
        saida = _tokens(json.dumps(msg, ensure_ascii=False))
        cache = (entrada // 128) * 64  # a API real cacheia em blocos; aqui, metade arredondada
        return self._json(200, {
            "id": f"chatcmpl-{random.randrange(10**8)}", "object": "chat.completion",
            "created": int(time.time()), "model": modelo + "-2024-07-18",
            "choices": [{"index": 0, "message": msg, "finish_reason": fim}],
            "usage": {"prompt_tokens": entrada, "completion_tokens": saida, "total_tokens": entrada + saida,
                      "prompt_tokens_details": {"cached_tokens": cache}},
        })


def iniciar(porta=0):
    """Sobe o LLM falso numa thread e devolve a base_url para o cliente da OpenAI.

    Porta 0 = o sistema escolhe uma livre. Porta fixa colidia com qualquer serviço que
    já estivesse nela (rodando com --network host, um painel de teste na 18080 bastou).
    """
    servidor = ThreadingHTTPServer(("127.0.0.1", porta), LLMFalso)
    threading.Thread(target=servidor.serve_forever, daemon=True).start()
    return f"http://127.0.0.1:{servidor.server_address[1]}/v1"


if __name__ == "__main__":
    print("LLM falso em", iniciar(), "(Ctrl+C para sair)")
    threading.Event().wait()
