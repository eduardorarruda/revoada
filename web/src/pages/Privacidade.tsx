// Política de privacidade (LGPD), rota pública #/privacidade: quem ainda não entrou
// também tem direito de ler o que o painel guarda sobre ele. Fica FORA do AppShell e
// do ToastProvider, como a status page.
//
// Cada afirmação aqui descreve o comportamento do código (onde está, nos comentários
// de cada seção). Os prazos que o administrador pode mudar vêm de /api/privacidade —
// os desta instalação, não os padrões; se a consulta falhar, ficam os padrões e a
// página avisa.
import { useEffect, useState } from "react";
import { BrandLogo } from "../components/BrandLogo";
import { buscarPrivacidade, PRIVACIDADE_PADRAO, type Privacidade as Dados } from "../api.privacidade";
import "./privacidade.css";

export function Privacidade() {
  const [dados, setDados] = useState<Dados>(PRIVACIDADE_PADRAO);
  const [padrao, setPadrao] = useState(false);

  useEffect(() => {
    let vivo = true;
    buscarPrivacidade()
      .then((d) => vivo && setDados(d))
      .catch(() => vivo && setPadrao(true));
    return () => {
      vivo = false;
    };
  }, []);

  const controlador = dados.controlador || "o administrador desta instalação";

  return (
    <div className="privacidade">
      <div className="privacidade__miolo">
        <header className="privacidade__topo">
          <a href="#/" className="privacidade__voltar">
            ← Voltar ao painel
          </a>
          <BrandLogo variant="full" />
          <h1>Política de privacidade</h1>
          <p className="privacidade__sub">
            O que este painel guarda sobre você, por quanto tempo e como pedir acesso, correção ou exclusão (Lei
            13.709/2018, LGPD).
          </p>
          {padrao && (
            <p className="privacidade__aviso" role="status">
              Não foi possível ler a configuração desta instalação: os prazos abaixo são os padrões do Revoada.
            </p>
          )}
        </header>

        <section aria-labelledby="pp-quem">
          <h2 id="pp-quem">Quem trata os dados</h2>
          <p>
            Este painel é uma instalação do Revoada, software de código aberto que cada organização instala no próprio
            servidor. Quem decide o que é monitorado e quem tem acesso é <strong>{controlador}</strong>, o controlador
            dos dados. O Revoada não envia dados ao autor do software nem a serviços de estatística ou propaganda.
          </p>
          {dados.contato ? (
            <p>
              Para exercer seus direitos ou tirar dúvidas: <strong>{dados.contato}</strong>.
            </p>
          ) : (
            <p>Para exercer seus direitos ou tirar dúvidas, fale com o administrador do painel.</p>
          )}
        </section>

        <section aria-labelledby="pp-usuarios">
          <h2 id="pp-usuarios">Dados de quem usa o painel</h2>
          {/* server/internal/store/migracoes/0001_base e 0002_seguranca; auth/crypto.go; auth/mfa.go */}
          <ul>
            <li>
              <strong>E-mail</strong>, que é o seu login, e o seu <strong>papel</strong> (administrador, operador ou
              leitor) com as permissões por servidor.
            </li>
            <li>
              <strong>Nome e celular</strong>, quando o administrador os cadastra. O celular serve para receber alertas
              por WhatsApp, se esse canal for ligado para você.
            </li>
            <li>
              <strong>Senha</strong>: guardada só como hash Argon2id. Ninguém, nem o administrador, consegue lê-la.
            </li>
            <li>
              <strong>Verificação em duas etapas</strong>: o segredo do app autenticador fica cifrado (AES-256-GCM, com
              a chave fora do banco); os códigos de recuperação, só como hash.
            </li>
            <li>
              <strong>Tentativas de login erradas</strong>: um contador, para bloquear a conta depois de 5 erros seguidos
              (1 minuto, depois 2, 4… até 1 hora). Zera no próximo login certo.
            </li>
          </ul>
          <p>
            O painel não registra seu IP nem seu navegador no login. O IP só é usado, em memória, para limitar
            tentativas, e é gravado no registro de ações descrito abaixo.
          </p>
        </section>

        <section aria-labelledby="pp-auditoria">
          <h2 id="pp-auditoria">Registro de ações (auditoria)</h2>
          {/* server/internal/audit/audit.go */}
          <p>
            Toda alteração feita pelo painel (criar, editar, apagar) fica registrada com quem fez, quando, o quê e de
            qual IP. Senhas, códigos, chaves, tokens, nome completo e celular são apagados do registro antes de gravar.
            O registro serve para segurança e prestação de contas, fica <strong>{dados.auditoria_dias} dias</strong> e
            continua existindo, pelo mesmo prazo, depois que um usuário é excluído.
          </p>
        </section>

        <section aria-labelledby="pp-cookies">
          <h2 id="pp-cookies">Cookies e armazenamento no navegador</h2>
          {/* server/internal/auth/handlers.go (revoada_refresh); web/src: theme.ts, AppShell, Collapsible, Hosts */}
          <p>
            O painel usa <strong>um único cookie</strong>, <code>revoada_refresh</code>, estritamente necessário para
            manter você conectado. Ele não é lido por JavaScript, só vai para o próprio painel e vence depois de{" "}
            {dados.sessao_inatividade_horas} horas sem uso, ou {dados.sessao_max_horas} horas depois do login, o que vier
            primeiro. Não há cookies de terceiros, de estatística ou de propaganda, por isso não há aviso de cookies.
          </p>
          <p>
            No armazenamento local do navegador ficam só preferências de tela (tema claro ou escuro, menu recolhido,
            seções abertas, modo de exibição de servidores). Elas não identificam você e não saem do seu navegador.
          </p>
        </section>

        <section aria-labelledby="pp-monitorados">
          <h2 id="pp-monitorados">Dados dos servidores e aplicações monitorados</h2>
          {/* agent/internal/logtail; core/redacao; gateway/internal/otlp/genai.go */}
          <p>
            O painel recebe métricas, logs, traces e eventos dos servidores e aplicações que o administrador cadastra.
            Logs podem conter dados pessoais de terceiros (por exemplo, o e-mail de um cliente numa linha de erro). Antes
            de enviar, o agente apaga chaves, tokens, senhas em endereços e números de cartão; CPF, e-mail e telefone não
            são reconhecidos e passam como estão.
          </p>
          <p>
            O texto de conversas com modelos de IA (prompts e respostas) vem <strong>desligado</strong>. Quando o
            administrador liga a gravação, o texto fica 7 dias e só administradores e operadores o leem, com cada leitura
            registrada.
          </p>
        </section>

        <section aria-labelledby="pp-prazos">
          <h2 id="pp-prazos">Por quanto tempo cada dado fica</h2>
          {/* deploy/migrations/clickhouse (TTLs); server/internal/store/prune.go; deploy/backup/backup.sh */}
          <div className="privacidade__tabela">
            <table>
              <thead>
                <tr>
                  <th scope="col">Dado</th>
                  <th scope="col">Prazo</th>
                </tr>
              </thead>
              <tbody>
                <tr>
                  <td>Conta de usuário</td>
                  <td>Até ser excluída por um administrador</td>
                </tr>
                <tr>
                  <td>Sessão (cookie)</td>
                  <td>
                    {dados.sessao_inatividade_horas} h sem uso, no máximo {dados.sessao_max_horas} h; sessões vencidas são
                    apagadas todo dia
                  </td>
                </tr>
                <tr>
                  <td>Registro de ações</td>
                  <td>{dados.auditoria_dias} dias</td>
                </tr>
                <tr>
                  <td>Notificações enviadas, alertas encerrados, checagens de sites</td>
                  <td>{dados.retencao_dias} dias</td>
                </tr>
                <tr>
                  <td>Logs</td>
                  <td>30 dias</td>
                </tr>
                <tr>
                  <td>Traces</td>
                  <td>15 dias</td>
                </tr>
                <tr>
                  <td>Métricas</td>
                  <td>7 dias (detalhe), 90 dias (por minuto), 730 dias (por hora)</td>
                </tr>
                <tr>
                  <td>Eventos</td>
                  <td>90 dias</td>
                </tr>
                <tr>
                  <td>Chamadas de IA</td>
                  <td>90 dias; totais por minuto, sem texto, 400 dias; texto das conversas (se ligado), 7 dias</td>
                </tr>
                <tr>
                  <td>Backup (se ligado pelo administrador)</td>
                  <td>14 cópias diárias e 3 mensais</td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>

        <section aria-labelledby="pp-compartilhamento">
          <h2 id="pp-compartilhamento">Com quem os dados são compartilhados</h2>
          {/* server/internal/notify/senders.go; sitecheck; cmd/server (ACME) */}
          <p>
            Por conta própria, com ninguém. O painel só fala com serviços de fora quando o administrador configura: envio
            de alertas (e-mail, webhook, Telegram ou WhatsApp por servidor próprio), checagem dos sites cadastrados e
            certificado HTTPS automático. Cada envio de alerta guarda o destino (e-mail ou número) pelo prazo da tabela
            acima.
          </p>
        </section>

        <section aria-labelledby="pp-seguranca">
          <h2 id="pp-seguranca">Segurança</h2>
          <p>
            Senhas com Argon2id, verificação em duas etapas obrigatória para administradores e operadores, sessão de
            acesso de 15 minutos guardada só na memória do navegador, bloqueio depois de tentativas erradas, segredos
            cifrados e confirmação de identidade antes de ações críticas. O uso de HTTPS depende de como o administrador
            publicou o painel.
          </p>
        </section>

        <section aria-labelledby="pp-direitos">
          <h2 id="pp-direitos">Seus direitos</h2>
          {/* useradmin (exclusão), logs/purge, traces/purge, ia/purge */}
          <p>
            Você pode pedir confirmação de que seus dados são tratados, acesso, correção, exclusão e informações sobre
            compartilhamento (art. 18 da LGPD). O pedido vai para o controlador, pelo canal indicado no início desta
            página. O painel tem as ferramentas para atender: excluir a conta (leva junto sessões e permissões) e apagar
            de forma dirigida logs, traces e conversas de IA. O registro de ações não é apagado antes do prazo, para
            preservar a prestação de contas.
          </p>
        </section>

        <footer className="privacidade__rodape">
          Este texto descreve o comportamento do Revoada nesta versão. Prazos que dependem da configuração aparecem com
          os valores desta instalação.
        </footer>
      </div>
    </div>
  );
}
