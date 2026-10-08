"""LLM falso (OpenAI + Anthropic) e coletor OTLP que grava cada POST /v1/traces em disco."""
import json, os, sys, threading, itertools
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer

OUT = sys.argv[1] if len(sys.argv) > 1 else "out"
seq = itertools.count()

def ultimo_papel_tool(msgs):
    return any(m.get("role") == "tool" for m in msgs)

class LLM(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def _json(self, code, obj):
        b = json.dumps(obj).encode()
        self.send_response(code); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0)); req = json.loads(self.rfile.read(n) or b"{}")
        if req.get("model") == "modelo-quebrado":
            return self._json(500, {"error": {"message": "falha simulada", "type": "server_error"}})
        if self.path.endswith("/chat/completions"):
            msgs = req.get("messages", [])
            tools = req.get("tools") or []
            if tools and not ultimo_papel_tool(msgs):
                nome = tools[0]["function"]["name"]
                msg = {"role": "assistant", "content": None, "tool_calls": [{"id": "call_1", "type": "function",
                       "function": {"name": nome, "arguments": json.dumps({"cidade": "Recife"})}}]}
                fim = "tool_calls"
            else:
                msg = {"role": "assistant", "content": "Faz 29 graus em Recife. Chave de teste sk-proj-ABCDEFGHIJKLMNOPQRSTUV."}
                fim = "stop"
            return self._json(200, {"id": "chatcmpl-1", "object": "chat.completion", "created": 1760000000,
                "model": req.get("model", "gpt-4o-mini") + "-2024-07-18", "choices": [{"index": 0, "message": msg, "finish_reason": fim}],
                "usage": {"prompt_tokens": 120, "completion_tokens": 30, "total_tokens": 150,
                          "prompt_tokens_details": {"cached_tokens": 100}}})
        if self.path.endswith("/messages"):
            msgs = req.get("messages", [])
            tools = req.get("tools") or []
            tem_resultado = any(isinstance(m.get("content"), list) and any(c.get("type") == "tool_result" for c in m["content"]) for m in msgs)
            if tools and not tem_resultado:
                content = [{"type": "tool_use", "id": "toolu_1", "name": tools[0]["name"], "input": {"cidade": "Recife"}}]; stop = "tool_use"
            else:
                content = [{"type": "text", "text": "Faz 29 graus em Recife."}]; stop = "end_turn"
            return self._json(200, {"id": "msg_1", "type": "message", "role": "assistant", "model": req.get("model"),
                "content": content, "stop_reason": stop, "stop_sequence": None,
                "usage": {"input_tokens": 20, "output_tokens": 30, "cache_read_input_tokens": 100, "cache_creation_input_tokens": 50}})
        self._json(404, {"error": "rota desconhecida " + self.path})

class Coletor(BaseHTTPRequestHandler):
    def log_message(self, *a): pass
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0)); body = self.rfile.read(n)
        nome = open(os.path.join(OUT, ".atual")).read().strip()
        with open(os.path.join(OUT, f"{nome}-{next(seq):02d}.pb"), "wb") as f: f.write(body)
        self.send_response(200); self.send_header("Content-Type", "application/x-protobuf"); self.end_headers()

if __name__ == "__main__":
    threading.Thread(target=ThreadingHTTPServer(("127.0.0.1", 18080), LLM).serve_forever, daemon=True).start()
    ThreadingHTTPServer(("127.0.0.1", 14318), Coletor).serve_forever()
