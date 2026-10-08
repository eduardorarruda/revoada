# OpenLLMetry (traceloop): instrumentações de OpenAI e Anthropic.
from comum import iniciar, OPENAI, rodar_agente_openai
tp = iniciar("agente-openllmetry")
from opentelemetry.instrumentation.openai import OpenAIInstrumentor
from opentelemetry.instrumentation.anthropic import AnthropicInstrumentor
OpenAIInstrumentor().instrument(); AnthropicInstrumentor().instrument()
import openai, anthropic
rodar_agente_openai(openai.OpenAI(**OPENAI))
a = anthropic.Anthropic(base_url="http://127.0.0.1:18080", api_key="sk-ant-falso")
a.messages.create(model="claude-sonnet-4-5", max_tokens=200, messages=[{"role": "user", "content": "Clima em Recife?"}],
                  tools=[{"name": "clima", "description": "Clima", "input_schema": {"type": "object", "properties": {"cidade": {"type": "string"}}}}])
tp.shutdown()
