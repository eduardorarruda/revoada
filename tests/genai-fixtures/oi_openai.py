# OpenInference (Arize/Phoenix): instrumentação de OpenAI.
from comum import iniciar, OPENAI, rodar_agente_openai
tp = iniciar("agente-openinference")
from openinference.instrumentation.openai import OpenAIInstrumentor
OpenAIInstrumentor().instrument()
import openai
rodar_agente_openai(openai.OpenAI(**OPENAI))
tp.shutdown()
