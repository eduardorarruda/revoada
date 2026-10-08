# Instrumentação oficial do OpenTelemetry (opentelemetry-instrumentation-openai-v2) +
# spans manuais de agente e ferramenta seguindo a semconv GenAI.
import os, sys
from comum import iniciar, OPENAI, rodar_agente_openai
tp = iniciar("agente-otel")
from opentelemetry.instrumentation.openai_v2 import OpenAIInstrumentor
OpenAIInstrumentor().instrument()
from opentelemetry import trace
import openai
t = trace.get_tracer("demo")
c = openai.OpenAI(**OPENAI)
with t.start_as_current_span("invoke_agent Meteorologista", kind=trace.SpanKind.INTERNAL) as ag:
    ag.set_attribute("gen_ai.operation.name", "invoke_agent")
    ag.set_attribute("gen_ai.provider.name", "openai")
    ag.set_attribute("gen_ai.agent.name", "Meteorologista")
    ag.set_attribute("gen_ai.agent.id", "agente-42")
    ag.set_attribute("gen_ai.conversation.id", "conversa-abc")
    msgs = [{"role": "user", "content": "Clima em Recife?"}]
    from comum import TOOLS
    r = c.chat.completions.create(model="gpt-4o-mini", messages=msgs, tools=TOOLS)
    call = r.choices[0].message.tool_calls[0]
    with t.start_as_current_span("execute_tool clima") as ft:
        ft.set_attribute("gen_ai.operation.name", "execute_tool")
        ft.set_attribute("gen_ai.tool.name", "clima")
        ft.set_attribute("gen_ai.tool.call.id", call.id)
        ft.set_attribute("gen_ai.tool.type", "function")
    msgs.append(r.choices[0].message.model_dump(exclude_none=True))
    msgs.append({"role": "tool", "tool_call_id": call.id, "content": '{"graus": 29}'})
    c.chat.completions.create(model="gpt-4o-mini", messages=msgs, tools=TOOLS)
try:
    openai.OpenAI(max_retries=0, **OPENAI).chat.completions.create(model="modelo-quebrado", messages=[{"role": "user", "content": "oi"}])
except Exception as e:
    print("erro esperado:", type(e).__name__)
tp.shutdown()
