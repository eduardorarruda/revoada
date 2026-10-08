import os
from opentelemetry import trace
from opentelemetry.sdk.resources import Resource
from opentelemetry.sdk.trace import TracerProvider
from opentelemetry.sdk.trace.export import SimpleSpanProcessor
from opentelemetry.exporter.otlp.proto.http.trace_exporter import OTLPSpanExporter

def iniciar(servico):
    tp = TracerProvider(resource=Resource.create({"service.name": servico, "host.name": "fixture-host"}))
    tp.add_span_processor(SimpleSpanProcessor(OTLPSpanExporter(endpoint="http://127.0.0.1:14318/v1/traces")))
    trace.set_tracer_provider(tp)
    return tp

OPENAI = dict(base_url="http://127.0.0.1:18080/v1", api_key="sk-teste-falso")
TOOLS = [{"type": "function", "function": {"name": "clima", "description": "Clima atual",
          "parameters": {"type": "object", "properties": {"cidade": {"type": "string"}}}}}]
def rodar_agente_openai(client, model="gpt-4o-mini"):
    msgs = [{"role": "system", "content": "Você é um assistente."},
            {"role": "user", "content": "Como está o clima em Recife? Meu cartão é 4111 1111 1111 1111."}]
    r = client.chat.completions.create(model=model, messages=msgs, tools=TOOLS)
    call = r.choices[0].message.tool_calls[0]
    msgs.append(r.choices[0].message.model_dump(exclude_none=True))
    msgs.append({"role": "tool", "tool_call_id": call.id, "content": '{"graus": 29}'})
    return client.chat.completions.create(model=model, messages=msgs, tools=TOOLS)
