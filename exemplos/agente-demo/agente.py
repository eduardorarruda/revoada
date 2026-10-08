"""Meteorologista: um agente de IA pequeno que manda traces para o Revoada.

Cada rodada é uma pergunta de clima. O modelo pede a ferramenta `clima`, a ferramenta
roda, o modelo responde. Algumas rodadas saem do caminho feliz de propósito, para a
tela Agentes de IA ter o que mostrar:

- loop: a estação de "Atlântida" nunca tem leitura e pede "tente novamente"; o modelo
  repete a ferramenta até o limite de passos do agente (destaque de repetição no replay);
- erro de ferramenta: a estação de Manaus estoura o tempo (span de ferramenta com erro);
- erro de modelo: a chamada usa um modelo que não existe (span de chat com erro).

Sem configuração, as chamadas vão para um LLM falso local (llm_falso.py) e nada é
gasto. Com OPENAI_API_KEY e OPENAI_BASE_URL definidas, vão para o provedor de verdade.

A instrumentação é a oficial do OpenTelemetry (opentelemetry-instrumentation-openai-v2)
para as chamadas de modelo, mais spans manuais de agente e de ferramenta — a oficial
não cria esses dois. Ver docs/instrumentacao-ia.md.
"""
import json
import os
import random
import socket
import sys
import time
from datetime import datetime

# Precisam estar no ambiente ANTES de a instrumentação ser carregada. SPAN_ONLY põe o
# conteúdo no próprio span (o que o replay do Revoada lê); EVENT_ONLY mandaria como log
# OTLP, fora do trace. Gravar ou não o conteúdo é decisão do gateway
# (REVOADA_GENAI_CONTEUDO): com o padrão "desligado", ele é descartado na chegada.
os.environ.setdefault("OTEL_SEMCONV_STABILITY_OPT_IN", "gen_ai_latest_experimental")
os.environ.setdefault("OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT", "SPAN_ONLY")

import openai  # noqa: E402
from opentelemetry import trace  # noqa: E402
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter  # noqa: E402
from opentelemetry.instrumentation.openai_v2 import OpenAIInstrumentor  # noqa: E402
from opentelemetry.sdk.resources import Resource  # noqa: E402
from opentelemetry.sdk.trace import TracerProvider  # noqa: E402
from opentelemetry.sdk.trace.export import BatchSpanProcessor, SpanExporter, SpanExportResult  # noqa: E402
from opentelemetry.trace import Status, StatusCode  # noqa: E402

import llm_falso  # noqa: E402

AGENTE = "Meteorologista"
MAX_PASSOS = 6  # chamadas de ferramenta por pergunta antes de o agente desistir
MODELO_QUEBRADO = "modelo-inexistente"
CAPTURAR_CONTEUDO = os.environ["OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT"].upper() in ("SPAN_ONLY", "SPAN_AND_EVENT")

# (cenário, cidade). Com RODADAS=8 passa por todos; acima disso, repete o ciclo.
CENARIOS = [
    ("normal", "Recife"), ("loop", "Atlântida"), ("normal", "Porto Alegre"),
    ("erro de ferramenta", "Manaus"), ("normal", "Curitiba"), ("normal", "Salvador"),
    ("erro de modelo", "Belém"), ("normal", "Natal"),
]

FERRAMENTAS = [{"type": "function", "function": {
    "name": "clima", "description": "Temperatura e céu agora numa cidade brasileira.",
    "parameters": {"type": "object", "properties": {"cidade": {"type": "string"}}, "required": ["cidade"]},
}}]


class LoopDeFerramentas(Exception):
    """O modelo pediu ferramenta mais vezes do que o agente permite."""


def clima(cidade):
    """A ferramenta. Finge consultar uma estação meteorológica."""
    time.sleep(random.uniform(0.05, 0.3))
    if cidade.startswith("Atl"):
        return {"erro": "estação sem leitura, tente novamente"}
    if cidade == "Manaus":
        time.sleep(0.5)
        raise TimeoutError("a estação não respondeu em 0,5 s")
    return {"cidade": cidade, "graus": random.randint(17, 34), "ceu": random.choice(["céu limpo", "nublado", "chuvoso"])}


def configurar_traces(gateway, chave):
    """Exportador OTLP/HTTP apontado para o gateway do Revoada."""
    recurso = Resource.create({
        # service.name vira a coluna "service" no painel.
        "service.name": os.environ.get("OTEL_SERVICE_NAME", "agente-demo"),
        "host.name": os.environ.get("HOST_NAME", socket.gethostname()),
    })
    exportador = ExportadorContado(OTLPSpanExporter(
        endpoint=gateway.rstrip("/") + "/v1/traces",
        headers={"X-Revoada-Key": chave},
    ))
    provedor = TracerProvider(resource=recurso)
    provedor.add_span_processor(BatchSpanProcessor(exportador))
    trace.set_tracer_provider(provedor)
    return provedor, exportador


class ExportadorContado(SpanExporter):
    """Repassa ao exportador OTLP e conta o resultado, para o resumo final dizer se o
    gateway aceitou os spans (o SDK só registra a falha num warning fácil de perder)."""

    def __init__(self, interno):
        self.interno, self.spans_ok, self.lotes_falhos = interno, 0, 0

    def export(self, spans):
        resultado = self.interno.export(spans)
        if resultado == SpanExportResult.SUCCESS:
            self.spans_ok += len(spans)
        else:
            self.lotes_falhos += 1
        return resultado

    def shutdown(self):
        self.interno.shutdown()

    def force_flush(self, timeout_millis=30000):
        return self.interno.force_flush(timeout_millis)


def executar_ferramenta(tracer, chamada):
    """Span manual de ferramenta, nos nomes da semconv GenAI."""
    nome = chamada.function.name
    with tracer.start_as_current_span(f"execute_tool {nome}", kind=trace.SpanKind.INTERNAL) as span:
        span.set_attribute("gen_ai.operation.name", "execute_tool")
        span.set_attribute("gen_ai.tool.name", nome)
        span.set_attribute("gen_ai.tool.call.id", chamada.id)
        span.set_attribute("gen_ai.tool.type", "function")
        if CAPTURAR_CONTEUDO:
            span.set_attribute("gen_ai.tool.call.arguments", chamada.function.arguments)
        try:
            resultado = json.dumps(clima(**json.loads(chamada.function.arguments)), ensure_ascii=False)
        except Exception as e:  # o erro vira resultado para o modelo, e fica marcado no span
            span.set_status(Status(StatusCode.ERROR, str(e)))
            span.set_attribute("error.type", type(e).__name__)
            resultado = json.dumps({"erro": f"{type(e).__name__}: {e}"}, ensure_ascii=False)
        if CAPTURAR_CONTEUDO:
            span.set_attribute("gen_ai.tool.call.result", resultado)
        return resultado


def atender(tracer, cliente, modelo, pergunta, conversa):
    """Uma execução do agente: o span invoke_agent é a raiz que o painel chama de execução."""
    with tracer.start_as_current_span(f"invoke_agent {AGENTE}", kind=trace.SpanKind.INTERNAL) as span:
        span.set_attribute("gen_ai.operation.name", "invoke_agent")
        span.set_attribute("gen_ai.provider.name", "openai")
        span.set_attribute("gen_ai.agent.name", AGENTE)
        span.set_attribute("gen_ai.agent.id", "meteorologista-v1")
        span.set_attribute("gen_ai.conversation.id", conversa)
        trace_id = format(span.get_span_context().trace_id, "032x")
        mensagens = [
            {"role": "system", "content": "Você é um meteorologista. Use a ferramenta clima e responda curto."},
            {"role": "user", "content": pergunta},
        ]
        try:
            for _ in range(MAX_PASSOS + 1):
                r = cliente.chat.completions.create(model=modelo, messages=mensagens, tools=FERRAMENTAS)
                resposta = r.choices[0].message
                if not resposta.tool_calls:
                    return trace_id, resposta.content
                mensagens.append(resposta.model_dump(exclude_none=True))
                for chamada in resposta.tool_calls:
                    mensagens.append({"role": "tool", "tool_call_id": chamada.id,
                                      "content": executar_ferramenta(tracer, chamada)})
            raise LoopDeFerramentas(f"mais de {MAX_PASSOS} chamadas de ferramenta")
        except Exception as e:
            span.set_attribute("error.type", type(e).__name__)
            span.set_status(Status(StatusCode.ERROR, str(e)))
            return trace_id, f"[erro] {type(e).__name__}: {e}"


def ler_numero(nome, padrao, tipo=int):
    try:
        return tipo(os.environ.get(nome, padrao))
    except ValueError:
        sys.exit(f"{nome} precisa ser um número")


def main():
    gateway = os.environ.get("REVOADA_GATEWAY", "http://localhost:8090")
    chave = os.environ.get("REVOADA_KEY", "")
    if not chave:
        sys.exit("Defina REVOADA_KEY com a chave de ingestão (Infraestrutura → Adicionar servidor).")
    rodadas = ler_numero("RODADAS", 8)
    intervalo = ler_numero("INTERVALO", 1, float)

    provedor, exportador = configurar_traces(gateway, chave)
    OpenAIInstrumentor().instrument()
    tracer = trace.get_tracer("agente-demo")

    if os.environ.get("OPENAI_API_KEY") and os.environ.get("OPENAI_BASE_URL"):
        cliente = openai.OpenAI()  # lê OPENAI_API_KEY e OPENAI_BASE_URL
        print(f"Provedor real em {os.environ['OPENAI_BASE_URL']} — isto gasta tokens.")
    else:
        cliente = openai.OpenAI(base_url=llm_falso.iniciar(), api_key="sk-falsa-so-para-demo", max_retries=0)
        print("LLM falso local (defina OPENAI_API_KEY e OPENAI_BASE_URL para usar um provedor real).")
    modelo = os.environ.get("MODELO", "gpt-4o-mini")
    print(f"Mandando traces para {gateway}/v1/traces · {rodadas} rodadas\n")

    sessao = datetime.now().strftime("%H%M%S")
    for i in range(rodadas):
        cenario, cidade = CENARIOS[i % len(CENARIOS)]
        conversa = f"demo-{sessao}-{i // 2}"  # duas perguntas por conversa
        usar = MODELO_QUEBRADO if cenario == "erro de modelo" else modelo
        trace_id, resposta = atender(tracer, cliente, usar, f"Como está o clima em {cidade}?", conversa)
        print(f"[{i + 1}/{rodadas}] {cidade:<13} {cenario:<19} trace {trace_id}\n      → {resposta}")
        if i + 1 < rodadas:
            time.sleep(intervalo)

    provedor.shutdown()  # descarrega o lote pendente antes de sair
    print(f"\nSpans aceitos pelo gateway: {exportador.spans_ok}", end="")
    if exportador.lotes_falhos:
        print(f" · lotes recusados: {exportador.lotes_falhos} "
              "(401/403 = chave errada ou revogada; conexão recusada = REVOADA_GATEWAY errado)")
        sys.exit(1)
    print("\nAbra Investigar → Agentes de IA → Execuções no painel.")


if __name__ == "__main__":
    main()
