// Grava spans reais do Vercel AI SDK (generateText com ferramenta) contra um LLM falso
// compatível com a API da OpenAI, e salva cada exportação OTLP em ../out/vercel-NN.pb.
import http from "node:http";
import fs from "node:fs";
import { NodeTracerProvider } from "@opentelemetry/sdk-trace-node";
import { SimpleSpanProcessor } from "@opentelemetry/sdk-trace-base";
import { OTLPTraceExporter } from "@opentelemetry/exporter-trace-otlp-proto";
import { resourceFromAttributes } from "@opentelemetry/resources";
import { generateText, tool, stepCountIs, registerTelemetry } from "ai";
import { OpenTelemetry, LegacyOpenTelemetry } from "@ai-sdk/otel";
import { createOpenAI } from "@ai-sdk/openai";
import { z } from "zod";

// VARIANTE=legado grava a integração antiga (atributos ai.*); o padrão, a nova.
const variante = process.env.VARIANTE === "legado" ? "vercel-legado" : "vercel";
let n = 0;
const coletor = http.createServer((req, res) => {
  const partes = [];
  req.on("data", (c) => partes.push(c));
  req.on("end", () => {
    fs.writeFileSync(`../out/${variante}-${String(n++).padStart(2, "0")}.pb`, Buffer.concat(partes));
    res.writeHead(200, { "Content-Type": "application/x-protobuf" });
    res.end();
  });
});
const llm = http.createServer((req, res) => {
  const partes = [];
  req.on("data", (c) => partes.push(c));
  req.on("end", () => {
    const corpo = JSON.parse(Buffer.concat(partes).toString() || "{}");
    const msgs = corpo.messages || [];
    const jaTemResultado = msgs.some((m) => m.role === "tool");
    const message = corpo.tools && !jaTemResultado
      ? { role: "assistant", content: null, tool_calls: [{ id: "call_1", type: "function",
          function: { name: corpo.tools[0].function.name, arguments: JSON.stringify({ cidade: "Recife" }) } }] }
      : { role: "assistant", content: "Faz 29 graus em Recife." };
    res.writeHead(200, { "Content-Type": "application/json" });
    res.end(JSON.stringify({ id: "chatcmpl-1", object: "chat.completion", created: 1760000000,
      model: "gpt-4o-mini-2024-07-18",
      choices: [{ index: 0, message, finish_reason: message.tool_calls ? "tool_calls" : "stop" }],
      usage: { prompt_tokens: 120, completion_tokens: 30, total_tokens: 150, prompt_tokens_details: { cached_tokens: 100 } } }));
  });
});
await new Promise((r) => coletor.listen(14319, r));
await new Promise((r) => llm.listen(18081, r));

const provider = new NodeTracerProvider({
  resource: resourceFromAttributes({ "service.name": "agente-vercel", "host.name": "fixture-host" }),
  spanProcessors: [new SimpleSpanProcessor(new OTLPTraceExporter({ url: "http://127.0.0.1:14319/v1/traces" }))],
});
provider.register();
registerTelemetry(variante === "vercel" ? new OpenTelemetry() : new LegacyOpenTelemetry());

const openai = createOpenAI({ baseURL: "http://127.0.0.1:18081/v1", apiKey: "sk-teste-falso" });
const r = await generateText({
  model: openai.chat("gpt-4o-mini"),
  prompt: "Como está o clima em Recife?",
  tools: { clima: tool({ description: "Clima atual", inputSchema: z.object({ cidade: z.string() }),
    execute: async ({ cidade }) => ({ cidade, graus: 29 }) }) },
  stopWhen: stepCountIs(3),
  telemetry: { functionId: "Meteorologista" },
});
console.log("resposta:", r.text);
await provider.shutdown();
coletor.close();
llm.close();
