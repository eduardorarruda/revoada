// Modal "Instrumentar PHP (cPanel)" — admin. Gera um revoada-tracer.php pronto
// (instrumentação sem tocar no código do site, via auto_prepend_file) e mostra o
// passo a passo do cPanel. A lista de chaves de ingestão é admin-only, por isso o
// botão que abre este modal só aparece para admin.
import { useCallback, useEffect, useState } from "react";
import { AdvancedSection, Button, FormField, Modal, useToast } from "../components";
import { listAgents, revealAgentKey, type AgentKey } from "../api";

function errMsg(e: unknown): string {
  return e instanceof Error ? e.message : String(e);
}

// Template VERBATIM do revoada-tracer.php. Guardado como String.raw para que nada
// do conteúdo ($_SERVER, $span, etc.) seja reprocessado como template literal.
// A substituição dos 4 tokens é feita depois com split/join (sem interpolação).
const PHP_TEMPLATE = String.raw`<?php
/**
 * revoada-tracer.php - Tracer de requisicoes do Revoada (sem alterar o codigo do site).
 * Instale via cPanel (MultiPHP INI Editor):
 *   auto_prepend_file = /home/SEU_USUARIO/revoada-tracer.php
 * Coloque este arquivo FORA do public_html. Ele NUNCA deve derrubar o site:
 * qualquer problema e engolido em silencio.
 */

if (!defined('REVOADA_TRACER_GATEWAY')) define('REVOADA_TRACER_GATEWAY', '__GATEWAY__');
if (!defined('REVOADA_TRACER_KEY'))     define('REVOADA_TRACER_KEY', '__KEY__');
if (!defined('REVOADA_TRACER_SERVICE')) define('REVOADA_TRACER_SERVICE', '__SERVICE__');
if (!defined('REVOADA_TRACER_SAMPLE'))  define('REVOADA_TRACER_SAMPLE', __SAMPLE__);

(function () {
    if (PHP_SAPI === 'cli') return;
    if (!function_exists('curl_init') || !function_exists('random_bytes')) return;
    if (REVOADA_TRACER_SAMPLE < 1.0 && (mt_rand() / mt_getrandmax()) > REVOADA_TRACER_SAMPLE) return;

    $startNs = (int) (($_SERVER['REQUEST_TIME_FLOAT'] ?? microtime(true)) * 1e9);

    $send = function () use ($startNs) {
        try {
            $endNs  = (int) (microtime(true) * 1e9);
            $status = http_response_code() ?: 200;
            $method = $_SERVER['REQUEST_METHOD'] ?? 'GET';
            $path   = $_SERVER['REQUEST_URI'] ?? '/';
            if (($q = strpos($path, '?')) !== false) $path = substr($path, 0, $q);

            $span = array(
                'traceId' => bin2hex(random_bytes(16)),
                'spanId'  => bin2hex(random_bytes(8)),
                'name'    => $method . ' ' . $path,
                'kind'    => 2,
                'startTimeUnixNano' => (string) $startNs,
                'endTimeUnixNano'   => (string) $endNs,
                'attributes' => array(
                    array('key' => 'http.request.method',       'value' => array('stringValue' => $method)),
                    array('key' => 'url.path',                   'value' => array('stringValue' => $path)),
                    array('key' => 'http.response.status_code',  'value' => array('intValue' => (string) $status)),
                    array('key' => 'server.address',             'value' => array('stringValue' => $_SERVER['HTTP_HOST'] ?? '')),
                ),
                'status' => array('code' => $status >= 500 ? 2 : 1),
            );

            $payload = array('resourceSpans' => array(array(
                'resource' => array('attributes' => array(
                    array('key' => 'service.name', 'value' => array('stringValue' => REVOADA_TRACER_SERVICE)),
                    array('key' => 'host.name',    'value' => array('stringValue' => $_SERVER['SERVER_NAME'] ?? gethostname())),
                )),
                'scopeSpans' => array(array('spans' => array($span))),
            )));

            $ch = curl_init(rtrim(REVOADA_TRACER_GATEWAY, '/') . '/v1/traces');
            curl_setopt_array($ch, array(
                CURLOPT_POST => true,
                CURLOPT_POSTFIELDS => json_encode($payload, JSON_UNESCAPED_SLASHES | JSON_UNESCAPED_UNICODE),
                CURLOPT_HTTPHEADER => array('Content-Type: application/json', 'X-Revoada-Key: ' . REVOADA_TRACER_KEY),
                CURLOPT_RETURNTRANSFER => true,
                CURLOPT_TIMEOUT_MS => 800,
                CURLOPT_CONNECTTIMEOUT_MS => 500,
                CURLOPT_NOSIGNAL => true,
            ));
            curl_exec($ch);
            curl_close($ch);
        } catch (\Throwable $e) {
            // Silencio: telemetria nunca pode afetar o site.
        }
    };

    register_shutdown_function(function () use ($send) {
        if (function_exists('fastcgi_finish_request')) @fastcgi_finish_request();
        $send();
    });
})();
`;

// Sanitiza o nome do serviço para caber com segurança dentro de aspas simples no
// PHP: escapa \ e ' (a barra invertida precisa vir primeiro). Ex.: it's → it\'s.
function sanitizeService(name: string): string {
  return name.replace(/\\/g, "\\\\").replace(/'/g, "\\'");
}

// Formata a amostragem como literal float PHP (1 → "1.0", 0.2 → "0.2"), sempre com
// ponto decimal para casar com a comparação `< 1.0` do template.
function sampleLiteral(n: number): string {
  if (!Number.isFinite(n)) return "1.0";
  const clamped = Math.min(1, Math.max(0, n));
  return Number.isInteger(clamped) ? `${clamped}.0` : String(clamped);
}

// Monta o conteúdo final do arquivo trocando os 4 tokens do template. Usa
// split/join (nunca interpolação) para não reprocessar os $ do PHP.
function buildPhp(gateway: string, key: string, service: string, sample: number): string {
  return PHP_TEMPLATE
    .split("__GATEWAY__").join(gateway)
    .split("__KEY__").join(key)
    .split("__SERVICE__").join(sanitizeService(service))
    .split("__SAMPLE__").join(sampleLiteral(sample));
}

export function PhpTracerModal({ open, onClose }: { open: boolean; onClose: () => void }) {
  const toast = useToast();
  const [agents, setAgents] = useState<AgentKey[] | null>(null);
  // Guarda o ID PÚBLICO do agente, não a chave: a listagem já não traz a chave em
  // claro, e o arquivo só precisa dela no momento de ser gerado.
  const [agentId, setAgentId] = useState("");
  const [service, setService] = useState("meu-site-php");
  const [sample, setSample] = useState(1);
  const [gerando, setGerando] = useState(false);

  const load = useCallback(async () => {
    setAgents(null);
    try {
      const r = await listAgents();
      const active = r.agents.filter((a) => a.revoked === false);
      setAgents(active);
      // Pré-seleciona a primeira chave ativa para o caminho feliz.
      setAgentId((prev) => (prev && active.some((a) => a.id === prev) ? prev : active[0]?.id ?? ""));
    } catch (e) {
      setAgents([]);
      toast.error(`Erro ao carregar chaves: ${errMsg(e)}`);
    }
  }, [toast]);

  useEffect(() => {
    if (!open) return;
    void load();
  }, [open, load]);

  const gateway = window.location.origin;
  const canGenerate = agentId !== "" && service.trim() !== "";

  // Aqui a chave em claro É o produto: o revoada-tracer.php só autentica no gateway
  // se levar a serverkey dentro. Por isso ela é buscada no CLIQUE (uma chave, uma
  // revelação registrada na Auditoria) e nunca fica pendurada no estado da tela —
  // montamos o arquivo, entregamos, e a variável morre no fim da função.
  const comConteudo = useCallback(
    async (usar: (php: string) => void) => {
      if (!canGenerate) return;
      setGerando(true);
      try {
        const { serverkey } = await revealAgentKey(agentId);
        usar(buildPhp(gateway, serverkey, service.trim(), sample));
      } catch (e) {
        toast.error(`Não foi possível obter a chave deste servidor: ${errMsg(e)}`);
      } finally {
        setGerando(false);
      }
    },
    [agentId, canGenerate, gateway, sample, service, toast],
  );

  function download() {
    void comConteudo((php) => {
      const blob = new Blob([php], { type: "application/x-php" });
      const url = URL.createObjectURL(blob);
      const a = document.createElement("a");
      a.href = url;
      a.download = "revoada-tracer.php";
      document.body.appendChild(a);
      a.click();
      a.remove();
      URL.revokeObjectURL(url);
      toast.success("revoada-tracer.php gerado. Confira o passo a passo abaixo.");
    });
  }

  function copy() {
    void comConteudo((php) => {
      navigator.clipboard.writeText(php).then(
        () => toast.success("Conteúdo copiado. Ele contém a chave deste servidor, trate como senha."),
        () => toast.error("Não foi possível copiar."),
      );
    });
  }

  const hasKeys = agents !== null && agents.length > 0;

  return (
    <Modal
      open={open}
      onClose={onClose}
      wide
      title="Instrumentar PHP (cPanel)"
      footer={<Button onClick={onClose}>Fechar</Button>}
    >
      <p style={{ color: "var(--text-2)", fontSize: "var(--fs-13)", marginTop: 0 }}>
        Gere um arquivo <code>revoada-tracer.php</code> pronto e ligue a instrumentação{" "}
        <strong>sem tocar no código do seu site</strong>: no cPanel, uma linha no MultiPHP INI Editor
        (<code>auto_prepend_file</code>) faz cada requisição virar um trace aqui em Traces.
      </p>

      {agents === null ? (
        <p style={{ color: "var(--text-2)" }}>Carregando chaves…</p>
      ) : !hasKeys ? (
        <div
          role="note"
          style={{
            display: "flex",
            alignItems: "flex-start",
            gap: "var(--sp-2)",
            padding: "var(--sp-3)",
            borderRadius: "var(--radius-sm)",
            background: "var(--bg-2)",
            border: "1px solid var(--border)",
            color: "var(--text-2)",
            fontSize: "var(--fs-14)",
          }}
        >
          <span aria-hidden="true">ℹ</span>
          <span>
            Nenhuma chave de ingestão. Crie um servidor/chave primeiro em{" "}
            <strong>Infraestrutura → Chaves de agente</strong>.
          </span>
        </div>
      ) : (
        <>
          <FormField
            label="Servidor / chave de ingestão"
            help="A chave (serverkey) autentica o envio no gateway. Use a do servidor onde o site PHP roda. Ela é buscada só na hora de gerar o arquivo e entra dentro dele, por isso o revoada-tracer.php deve ser tratado como senha."
          >
            <select className="field" value={agentId} onChange={(e) => setAgentId(e.target.value)}>
              {agents.map((a) => (
                <option key={a.id} value={a.id}>
                  {a.hostname || a.serverkey}
                </option>
              ))}
            </select>
          </FormField>

          <FormField
            label="Nome do serviço"
            hint="É o service.name que vai aparecer na lista de Traces. Ex.: loja-virtual, blog-institucional."
          >
            <input
              className="field"
              value={service}
              onChange={(e) => setService(e.target.value)}
              placeholder="meu-site-php"
            />
          </FormField>

          <AdvancedSection label="Opções avançadas">
            <FormField
              label="Amostragem"
              hint="Fração das requisições que geram trace: 1 = todas, 0.2 = ~20%. Baixe se o site tiver tráfego muito alto."
            >
              <input
                className="field"
                type="number"
                min={0}
                max={1}
                step={0.1}
                value={sample}
                onChange={(e) => setSample(Number(e.target.value))}
                style={{ maxWidth: 140 }}
              />
            </FormField>
          </AdvancedSection>

          <div style={{ display: "flex", gap: "var(--sp-2)", flexWrap: "wrap", margin: "var(--sp-3) 0" }}>
            <Button variant="primary" onClick={download} disabled={!canGenerate || gerando}>
              {gerando ? "Gerando…" : "Gerar arquivo"}
            </Button>
            <Button variant="ghost" onClick={copy} disabled={!canGenerate || gerando}>
              Copiar conteúdo
            </Button>
          </div>

          <div
            role="note"
            style={{
              display: "flex",
              alignItems: "flex-start",
              gap: "var(--sp-2)",
              padding: "var(--sp-2) var(--sp-3)",
              borderRadius: 8,
              background: "var(--bg-2)",
              border: "1px solid var(--border)",
              color: "var(--text-2)",
              fontSize: "var(--fs-13)",
              margin: "var(--sp-3) 0",
            }}
          >
            <span aria-hidden="true">ℹ</span>
            <span>
              Este modo registra <strong>1 trace por requisição</strong> (rota, duração, status HTTP), ótimo
              para achar página lenta ou erro 500. Ele <strong>não</strong> detalha queries SQL/chamadas
              internas (isso exigiria a extensão OpenTelemetry no PHP). O tracer é à prova de falha:{" "}
              <strong>nunca derruba o site e não soma latência</strong> (envia depois de responder).
            </span>
          </div>

          <h3 style={{ fontSize: "var(--fs-14)", margin: "var(--sp-4) 0 var(--sp-2)" }}>
            Passo a passo no cPanel
          </h3>
          <ol style={{ paddingLeft: "var(--sp-4)", color: "var(--text-2)", fontSize: "var(--fs-13)", lineHeight: 1.6, margin: 0 }}>
            <li>Baixe o <code>revoada-tracer.php</code> (botão “Gerar arquivo” acima).</li>
            <li>
              No cPanel → <strong>File Manager</strong>, suba o arquivo na sua <strong>HOME</strong> (FORA do{" "}
              <code>public_html</code>, para não ficar acessível pela web).
            </li>
            <li>
              No cPanel → <strong>MultiPHP INI Editor</strong>, selecione o domínio e adicione:{" "}
              <code>auto_prepend_file = /home/SEU_USUARIO/revoada-tracer.php</code> (troque{" "}
              <code>SEU_USUARIO</code> pelo seu usuário cPanel). Alternativa sem o editor: crie um{" "}
              <code>.user.ini</code> na pasta do site com essa mesma linha.
            </li>
            <li>
              Acesse o site normalmente e depois abra a aba <strong>Traces</strong>, as requisições vão
              aparecer em segundos.
            </li>
          </ol>
        </>
      )}
    </Modal>
  );
}
