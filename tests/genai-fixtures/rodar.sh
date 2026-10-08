#!/usr/bin/env bash
# Regrava as fixtures de spans de IA usadas em gateway/internal/otlp/testdata/genai.
# Roda dentro de um container python:3.12-slim (ver README.md); cada biblioteca de
# instrumentação fica num venv próprio porque elas brigam entre si por versão.
set -euo pipefail
mkdir -p out
python servidores.py out &
sleep 1

instalar() { pip install -q "$@" 2>&1 | grep -v notice || true; }

# 1) Instrumentação oficial do OpenTelemetry. O modo de conteúdo agora é um enum:
#    SPAN_ONLY põe o conteúdo no span; EVENT_ONLY manda como LOG OTLP (não chega no
#    trace — é por isso que a fixture otel-eventos não traz mensagens).
python -m venv /tmp/v1 && . /tmp/v1/bin/activate
instalar "openai>=2.8,<3" opentelemetry-sdk opentelemetry-exporter-otlp-proto-http \
  "opentelemetry-util-genai==0.4b0" opentelemetry-instrumentation-openai-v2
export OTEL_SEMCONV_STABILITY_OPT_IN=gen_ai_latest_experimental
echo otel-oficial > out/.atual
OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=SPAN_ONLY python otel_oficial.py
echo otel-eventos > out/.atual
OTEL_INSTRUMENTATION_GENAI_CAPTURE_MESSAGE_CONTENT=EVENT_ONLY python otel_oficial.py
pip freeze | grep -iE "opentelemetry-instrumentation|^openai" > out/versoes-otel.txt
deactivate

# 2) OpenLLMetry (Traceloop): OpenAI e Anthropic.
python -m venv /tmp/v2 && . /tmp/v2/bin/activate
instalar "openai>=2.8,<3" anthropic opentelemetry-sdk opentelemetry-exporter-otlp-proto-http \
  opentelemetry-instrumentation-openai opentelemetry-instrumentation-anthropic
echo openllmetry > out/.atual
TRACELOOP_TRACE_CONTENT=true python openllmetry.py
pip freeze | grep -iE "opentelemetry-instrumentation|^openai|^anthropic" > out/versoes-openllmetry.txt
deactivate

# 3) OpenInference (Arize/Phoenix): OpenAI.
python -m venv /tmp/v3 && . /tmp/v3/bin/activate
instalar "openai>=2.8,<3" opentelemetry-sdk opentelemetry-exporter-otlp-proto-http \
  openinference-instrumentation-openai
echo openinference > out/.atual
python oi_openai.py
pip freeze | grep -iE "openinference|opentelemetry-instrumentation|^openai" > out/versoes-openinference.txt
deactivate

ls -la out
