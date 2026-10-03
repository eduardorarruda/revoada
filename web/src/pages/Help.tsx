// Guia do Revoada (UX.3) — manual navegável dentro do app: sumário lateral
// (recolhe no mobile), primeiros passos, uma seção por tela e um glossário.
//
// Cada seção é um bloco RECOLHÍVEL, fechado por padrão. Antes, o Guia despejava
// o manual inteiro numa página só: 20 mil pixels de altura no celular, mais de
// vinte telas de rolagem para chegar ao glossário. Manual longo não é problema —
// manual longo obrigatório é. Fechado, a página vira um índice de uma tela; a
// resposta continua a um toque, e a busca abre sozinha o que casou.
//
// Os "Primeiros passos" ganharam um nível a mais de recolhimento, por cenário
// (Linux, universal, Windows, macOS, sem systemd). Quem instala num Windows não
// tem por que rolar por três receitas de Linux para achar a sua.
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import { PageHeader, HelpPanel, Collapsible } from "../components";
import { help } from "../help";

// Rola até uma seção pelo id (o container de scroll é o conteúdo da shell).
function scrollToSection(id: string) {
  document.getElementById(id)?.scrollIntoView({ behavior: "smooth", block: "start" });
}

function norm(s: string): string {
  return s.toLowerCase();
}

// Passo a passo detalhado de instrumentação de traces, renderizado no lugar do
// `how[]` genérico (que é texto puro) para poder mostrar blocos de comando copiáveis.
function TraceSetupSteps() {
  return (
    <>
      <p className="help-section__what">
        <strong>Antes de tudo:</strong> traces NÃO vêm do agente, vêm da sua aplicação
        instrumentada com OpenTelemetry. O agente coleta CPU/memória/disco do servidor; os traces
        seguem cada requisição por dentro do seu código. Instrumentar é ligar o OpenTelemetry na
        aplicação e apontá-lo para o gateway. Passo a passo:
      </p>
      <ol className="help-section__how">
        <li>
          <strong>Pegue a chave de ingestão</strong> (o header <code>X-Revoada-Key</code>), a mesma
          usada pelos agentes. Se não tiver uma dedicada, use a que já está no{" "}
          <code>agent.yaml</code> de um servidor, ou peça uma nova ao administrador.
        </li>
        <li>
          <strong>Defina estas variáveis de ambiente</strong> na aplicação (valem para qualquer
          linguagem, o SDK acrescenta <code>/v1/traces</code> ao endpoint sozinho):
          <code className="help-code">
            {`OTEL_EXPORTER_OTLP_ENDPOINT=http://<seu-gateway>:8090
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_EXPORTER_OTLP_HEADERS=X-Revoada-Key=<sua-chave>
OTEL_SERVICE_NAME=<nome-do-servico>
OTEL_RESOURCE_ATTRIBUTES=host.name=<servidor>`}
          </code>
          <span style={{ color: "var(--text-2)" }}>
            <code>service.name</code> é o nome que aparece na lista de Traces e no Service Map;{" "}
            <code>host.name</code> casa o trace com um servidor no filtro "Servidor".
          </span>
        </li>
        <li>
          <strong>Ligue o OpenTelemetry conforme o seu stack:</strong>
        </li>
      </ol>

      <p className="help-section__what">
        <strong>Java / Spring (ex.: um ERP em Spring Boot):</strong> baixe o agente e rode com{" "}
        <code>-javaagent</code>, zero mudança de código:
      </p>
      <code className="help-code">
        {`# baixe uma vez (ou faça no Dockerfile)
curl -L -o otel-javaagent.jar https://github.com/open-telemetry/opentelemetry-java-instrumentation/releases/latest/download/opentelemetry-javaagent.jar
# rode a app com o agente + as variáveis do passo 2
java -javaagent:otel-javaagent.jar -jar app.jar`}
      </code>

      <p className="help-section__what">
        <strong>PHP (Laravel / Symfony / Slim, zero-code):</strong> precisa da extensão C
        (PHP ≥ 8.1) + pacotes Composer:
      </p>
      <code className="help-code">
        {`pecl install opentelemetry   # ou: install-php-extensions opentelemetry
composer require open-telemetry/sdk open-telemetry/exporter-otlp open-telemetry/opentelemetry-auto-laravel
# + as variáveis do passo 2, mais: OTEL_PHP_AUTOLOAD_ENABLED=true`}
      </code>

      <p className="help-section__what">
        <strong>Node.js:</strong> <code>npm i @opentelemetry/auto-instrumentations-node</code> e rode
        com <code>node --require @opentelemetry/auto-instrumentations-node/register app.js</code>.{" "}
        <strong>Python:</strong> <code>pip install opentelemetry-distro opentelemetry-exporter-otlp</code>,{" "}
        <code>opentelemetry-bootstrap -a install</code> e rode com{" "}
        <code>opentelemetry-instrument python app.py</code>. Ambos usam as mesmas variáveis do passo 2.
      </p>

      <ol className="help-section__how" start={4}>
        <li>
          <strong>Reinicie a aplicação e gere tráfego</strong> (acesse algumas telas / chame alguns
          endpoints).
        </li>
        <li>
          <strong>Abra a tela de Traces.</strong> Traces com erro ou lentos (≥ 1s) aparecem primeiro
          e são sempre guardados; dos normais, o painel guarda ~20% (tail sampling). Durante o teste,
          se quiser ver 100%, o administrador pode definir <code>REVOADA_TRACE_SAMPLE=1</code> no gateway.
        </li>
      </ol>

      <p className="help-section__what">
        Passo a passo completo por linguagem: <code>docs/instrumentacao-php.md</code> (PHP, com casos
        de app sem framework, WordPress e PHP legado) e <code>docs/instrumentacao-traces.md</code>{" "}
        (Java, Node, Python, .NET, Go).
      </p>
    </>
  );
}

// --- Primeiros passos, um cenário por bloco ---------------------------------
// O conteúdo é o mesmo de sempre; o que mudou é que cada receita mora atrás do
// seu próprio rótulo, em vez de todas empilhadas em sequência.
function PrimeirosPassos() {
  return (
    <>
      <p className="help-section__what">
        O Revoada coleta métricas, logs e traces dos seus hosts e serviços. O caminho mais
        rápido para começar é instalar o agente num host: ele coleta CPU, memória, disco e
        rede e envia por OTLP ao gateway (com buffer em disco quando o gateway está fora).
        Abaixo, uma receita por cenário, abra a sua.
      </p>

      <Collapsible titulo="O caminho mais curto: um servidor, um arquivo" defaultOpen>
        <p className="help-section__what">
          Em Infraestrutura, clique em <strong>“Adicionar servidor”</strong> e escolha{" "}
          <strong>“Configurar um servidor específico”</strong>: dê um nome ao servidor e escolha o
          sistema operacional. O painel gera a chave de ingestão e entrega{" "}
          <strong>um arquivo só</strong>, <code>.sh</code> no Linux, <code>.exe</code> no Windows,{" "}
          <code>.command</code> no macOS, já com a chave, o endereço do painel e os limites de
          recurso dentro. Leve para o servidor e rode: no Linux e no macOS com{" "}
          <code>sudo sh &lt;arquivo&gt;</code>; no Windows, botão direito →{" "}
          <strong>Executar como administrador</strong>. Trate o arquivo como uma senha e apague
          depois de instalar.
        </p>
        <p className="help-section__what">
          Assim que o agente reportar, o host aparece em Infraestrutura. Depois monte um dashboard,
          cadastre um canal de notificação e crie sua primeira regra de alerta, o checklist da tela
          Início acompanha esse progresso.
        </p>
      </Collapsible>

      <Collapsible titulo="Vários servidores de uma vez (instalador universal)">
        <p className="help-section__what">
          Na mesma tela, escolha “Baixar o instalador UNIVERSAL”, dê um nome ao lote e gere o{" "}
          <strong>instalador universal</strong>, um arquivo só, que roda em quantas máquinas você
          quiser. Ele não carrega chave: ao rodar, cada máquina pede ao painel a chave dela. Por isso
          revogar um servidor não derruba os outros, e o painel mostra quantos entraram por cada
          lote. Se o arquivo cair em mãos erradas, “Revogar”, em “Chaves de acesso”, corta as
          entradas novas sem afetar quem já está reportando.
        </p>
      </Collapsible>

      <Collapsible titulo="Instalar por SSH ou pela linha de comando">
        <p className="help-section__what">
          Se você tem acesso SSH à máquina, o caminho <strong>“Instalar por SSH”</strong> faz tudo
          daqui, sem tocar no servidor, só que ele exige <strong>Linux com systemd</strong>. E, para
          quem prefere a linha de comando, o instalador de uma linha continua valendo:
        </p>
        <code className="help-code">
          curl -fsSL https://&lt;host&gt;/install.sh | sh -s -- --key &lt;CHAVE&gt; --gateway
          https://&lt;host&gt;
        </code>
        <p className="help-section__what">
          O endereço do gateway depende de como o Revoada foi instalado. Com o{" "}
          <code>docker compose</code> da raiz do projeto, o gateway responde direto na porta{" "}
          <code>8090</code> (<code>http://&lt;host&gt;:8090</code>). Atrás de um proxy com HTTPS que
          encaminha a ingestão, use o <strong>mesmo endereço do painel, sem porta</strong>{" "}
          (<code>https://&lt;host&gt;</code>). Se o agente coleta mas nada aparece no painel, confira
          este endereço: o agente guarda tudo no buffer enquanto não alcança o gateway.
        </p>
      </Collapsible>

      <Collapsible titulo="Windows: instalação manual">
        <p className="help-section__what">
          O instalador <code>.exe</code> gerado pelo painel já faz tudo isto sozinho. Os passos
          abaixo servem para quem precisa instalar à mão (sem acesso ao painel na hora, ou para
          conferir o que o instalador faz). Num PowerShell{" "}
          <strong>aberto como Administrador</strong>:
        </p>
        <code className="help-code">
          {`# 1) Baixar o agente e criar as pastas
New-Item -ItemType Directory -Force C:\\revoada\\buffer | Out-Null
Invoke-WebRequest https://<host>/revoada-agent.exe -OutFile C:\\revoada\\revoada-agent.exe

# 2) Criar a configuração (pegue a chave em Infraestrutura > Adicionar servidor)
@"
gateway_url: https://<host>
key: SUA-CHAVE-AQUI
hostname: nome-do-servidor
interval_seconds: 15
buffer_dir: C:\\revoada\\buffer
"@ | Set-Content C:\\revoada\\agent.yaml -Encoding UTF8

# 3) Testar antes de instalar de vez
C:\\revoada\\revoada-agent.exe -config C:\\revoada\\agent.yaml doctor
C:\\revoada\\revoada-agent.exe -config C:\\revoada\\agent.yaml once

# 4) Deixar rodando sempre (inicia junto com o Windows, como SYSTEM)
schtasks /Create /TN "Revoada Agent" /TR "C:\\revoada\\revoada-agent.exe -config C:\\revoada\\agent.yaml" /SC ONSTART /RU SYSTEM /RL HIGHEST /F
schtasks /Run /TN "Revoada Agent"`}
        </code>
        <p className="help-section__what">
          O <code>doctor</code> diagnostica conexão e chave; o <code>once</code> envia uma coleta
          única, logo depois dele o servidor já aparece em Infraestrutura. O <code>hostname</code> é
          opcional, mas recomendado: é o nome que aparece no painel. A tarefa agendada faz o papel do
          serviço do systemd (o agente ainda não se registra como serviço nativo do Windows). No
          Windows o agente coleta CPU, RAM, disco, rede, tempo no ar e processos; containers Docker e
          logs do sistema (journald/syslog/kernel) são recursos Linux e ficam de fora. Para
          desinstalar: <code>schtasks /Delete /TN "Revoada Agent" /F</code> e apague a pasta{" "}
          <code>C:\revoada</code>.
        </p>
      </Collapsible>

      <Collapsible titulo="macOS">
        <p className="help-section__what">
          O instalador gerado pelo painel (<code>.command</code>) descobre sozinho se o Mac é Apple
          Silicon ou Intel e baixa o binário certo; instala em <code>/usr/local/bin</code> e registra
          um daemon do <code>launchd</code> que sobe junto com o sistema. Abra o Terminal na pasta do
          arquivo e rode <code>sudo sh &lt;arquivo&gt;</code>. Como no Windows, containers e logs do
          sistema são recursos Linux e ficam de fora. Para desinstalar:{" "}
          <code>sudo sh &lt;arquivo&gt; --uninstall</code>.
        </p>
      </Collapsible>

      <Collapsible titulo="Sem systemd: hospedagem compartilhada e cPanel">
        <p className="help-section__what">
          Use o modo avulso do agente por cron, em vez do serviço contínuo. Em servidor próprio (com
          root), basta acrescentar <code>--cron</code> ao instalador de uma linha. Numa conta
          compartilhada (sem root), coloque o binário na sua home e agende no "Trabalhos Agendados"
          (Cron Jobs) do cPanel:
        </p>
        <code className="help-code">
          * * * * * $HOME/bin/revoada-agent -config $HOME/revoada/agent.yaml once
        </code>
        <p className="help-section__what">
          O <code>revoada-agent once</code> coleta uma vez e sai; o cron o chama a cada minuto.
          No <code>agent.yaml</code>, defina um <code>hostname</code> único (o host físico é
          compartilhado entre contas) e um <code>buffer_dir</code> gravável pela sua conta.
        </p>
      </Collapsible>
    </>
  );
}

export function Help() {
  const [helpOpen, setHelpOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [mobile, setMobile] = useState(() => window.matchMedia("(max-width: 767px)").matches);
  const [tocOpen, setTocOpen] = useState(true);
  // Quais seções estão abertas. Começa vazio: o Guia abre como índice.
  const [abertas, setAbertas] = useState<Set<string>>(() => new Set());

  useEffect(() => {
    const mq = window.matchMedia("(max-width: 767px)");
    const on = () => {
      setMobile(mq.matches);
      setTocOpen(!mq.matches); // no mobile começa recolhido
    };
    on();
    mq.addEventListener("change", on);
    return () => mq.removeEventListener("change", on);
  }, []);

  const q = norm(query.trim());

  // Seções fixas (primeiros passos + páginas + glossário) com título p/ filtro.
  const pageEntries = Object.entries(help.pages);
  const conceptEntries = Object.entries(help.concepts);

  const firstStepsVisible = q === "" || norm("Primeiros passos").includes(q);
  const visiblePages = useMemo(
    () => pageEntries.filter(([, p]) => q === "" || norm(p.title).includes(q)),
    [pageEntries, q],
  );
  const visibleConcepts = useMemo(
    () => conceptEntries.filter(([, c]) => q === "" || norm(c.title).includes(q)),
    [conceptEntries, q],
  );
  const glossaryVisible = q === "" || norm("Glossário").includes(q) || visibleConcepts.length > 0;

  // Itens do sumário na ordem de exibição.
  const toc: { id: string; label: string }[] = [
    ...(firstStepsVisible ? [{ id: "primeiros-passos", label: "Primeiros passos" }] : []),
    ...visiblePages.map(([key, p]) => ({ id: `help-${key}`, label: p.title })),
    ...(glossaryVisible ? [{ id: "glossario", label: "Glossário" }] : []),
  ];

  // Buscar abre o que casou (senão a busca devolveria uma lista de títulos
  // fechados, e a pessoa teria de abrir um por um para ver se era aquele).
  // Limpar a busca fecha tudo de novo e o Guia volta a ser um índice.
  // A dependência é a lista de ids em texto — array novo a cada render
  // reexecutaria o efeito para sempre.
  const idsVisiveis = toc.map((t) => t.id).join("|");
  useEffect(() => {
    setAbertas(q === "" ? new Set() : new Set(idsVisiveis.split("|").filter(Boolean)));
  }, [q, idsVisiveis]);

  function alternar(id: string, aberto: boolean) {
    setAbertas((s) => {
      const n = new Set(s);
      if (aberto) n.add(id);
      else n.delete(id);
      return n;
    });
  }

  // O sumário ABRE a seção antes de rolar até ela — clicar num item e cair num
  // bloco fechado seria um beco sem saída.
  function irPara(id: string) {
    setAbertas((s) => new Set(s).add(id));
    requestAnimationFrame(() => scrollToSection(id));
  }

  const nothing = toc.length === 0;

  return (
    <div className="page">
      <PageHeader
        title="Guia do Revoada"
        subtitle="Manual do sistema: primeiros passos, o que faz cada tela e um glossário dos termos."
        onHelp={() => setHelpOpen(true)}
      />

      <input
        className="field help-search"
        type="search"
        placeholder="Buscar por tela ou termo…"
        aria-label="Buscar por tela ou termo"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
      />

      <div className="help-layout">
        <nav className="help-toc" aria-label="Sumário">
          {mobile ? (
            <button
              type="button"
              className="help-toc__toggle"
              aria-expanded={tocOpen}
              onClick={() => setTocOpen((o) => !o)}
            >
              {tocOpen ? <ChevronDown size={16} /> : <ChevronRight size={16} />}
              Sumário
            </button>
          ) : (
            <div className="help-toc__title">Sumário</div>
          )}
          {(!mobile || tocOpen) &&
            toc.map((t) => (
              <button
                key={t.id}
                type="button"
                className="help-toc__link"
                onClick={() => irPara(t.id)}
              >
                {t.label}
              </button>
            ))}
        </nav>

        <div className="help-content help-content--colapsavel">
          {nothing && (
            <p className="help-empty" role="status">
              Nenhum tópico corresponde à busca.
            </p>
          )}

          {firstStepsVisible && (
            <SecaoGuia
              id="primeiros-passos"
              titulo="Primeiros passos"
              resumo="instalar o agente, por cenário"
              aberta={abertas.has("primeiros-passos")}
              onAlternar={alternar}
            >
              <PrimeirosPassos />
            </SecaoGuia>
          )}

          {visiblePages.map(([key, p]) => (
            <SecaoGuia
              key={key}
              id={`help-${key}`}
              titulo={p.title}
              resumo={p.faq ? `${p.how.length} passos · ${p.faq.length} perguntas` : `${p.how.length} passos`}
              aberta={abertas.has(`help-${key}`)}
              onAlternar={alternar}
            >
              <p className="help-section__what">{p.what}</p>
              {key === "instrumentacao" ? (
                <TraceSetupSteps />
              ) : (
                <ol className="help-section__how">
                  {p.how.map((h, i) => (
                    <li key={i}>{h}</li>
                  ))}
                </ol>
              )}
              {p.faq && (
                <div className="help-faq">
                  {p.faq.map((f, i) => (
                    <div key={i}>
                      <p className="help-faq__q">{f.q}</p>
                      <p className="help-faq__a">{f.a}</p>
                    </div>
                  ))}
                </div>
              )}
            </SecaoGuia>
          ))}

          {glossaryVisible && (
            <SecaoGuia
              id="glossario"
              titulo="Glossário"
              resumo={`${(q === "" ? conceptEntries : visibleConcepts).length} termos`}
              aberta={abertas.has("glossario")}
              onAlternar={alternar}
            >
              <dl className="glossary__list">
                {(q === "" ? conceptEntries : visibleConcepts).map(([key, c]) => (
                  <div className="glossary__term" key={key}>
                    <dt>{c.title}</dt>
                    <dd>{c.body}</dd>
                  </div>
                ))}
              </dl>
            </SecaoGuia>
          )}
        </div>
      </div>

      <HelpPanel
        open={helpOpen}
        onClose={() => setHelpOpen(false)}
        title={help.pages.help.title}
        sections={[
          { heading: "O que é esta tela", body: help.pages.help.what },
          {
            heading: "Como usar",
            body: (
              <ol className="help-section__how">
                {help.pages.help.how.map((h, i) => (
                  <li key={i}>{h}</li>
                ))}
              </ol>
            ),
          },
        ]}
      />
    </div>
  );
}

// Uma seção do Guia. Controlada pela página para que a busca e o sumário
// consigam abrir a seção certa.
function SecaoGuia({
  id,
  titulo,
  resumo,
  aberta,
  onAlternar,
  children,
}: {
  id: string;
  titulo: string;
  resumo: string;
  aberta: boolean;
  onAlternar: (id: string, aberto: boolean) => void;
  children: ReactNode;
}) {
  return (
    <Collapsible
      id={id}
      titulo={titulo}
      resumo={resumo}
      open={aberta}
      onOpenChange={(v) => onAlternar(id, v)}
    >
      {children}
    </Collapsible>
  );
}
