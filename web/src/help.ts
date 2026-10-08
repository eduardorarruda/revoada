// Catálogo central de textos de ajuda do Revoada.
// Fonte única de verdade para PageHeader/HelpPanel (o que é a tela), FormField
// (o que é o campo) e a página #/help (glossário). Tom: português simples, 1-3
// frases, sempre com consequência prática e, quando ajuda, um exemplo.
// Proibido jargão sem explicação; nada em inglês.

export interface PageHelp {
  title: string;
  what: string;
  how: string[];
  faq?: { q: string; a: string }[];
}

export interface ConceptHelp {
  title: string;
  body: string;
}

export const help: {
  pages: Record<string, PageHelp>;
  fields: Record<string, string>;
  concepts: Record<string, ConceptHelp>;
} = {
  pages: {
    home: {
      title: "Início",
      what: "Visão geral do que importa agora: quantos hosts e sites estão no ar, alertas em disparo e um checklist para terminar de configurar o Revoada.",
      how: [
        "Olhe os cartões do topo para ter o resumo do ambiente (hosts, alertas, sites).",
        "Clique em qualquer cartão para ir direto à tela detalhada daquele assunto.",
        "Veja a lista \"atenção agora\" com o que está com problema neste momento.",
        "Complete os itens do checklist de primeiros passos para deixar o sistema útil.",
      ],
      faq: [
        {
          q: "O checklist sumiu, é problema?",
          a: "Não. Ele se recolhe num selo \"Configuração completa\" quando você termina todos os passos. É sinal de que está tudo pronto.",
        },
        {
          q: "Os números batem com o resto do sistema?",
          a: "Sim, são calculados dos mesmos dados das outras telas. Se um cartão mostra zero, é porque ainda não há aquele tipo de dado (ex.: nenhum agente instalado).",
        },
      ],
    },
    wall: {
      title: "Mural de Saúde",
      what: "Painel de semáforo (verde/amarelo/vermelho) com o estado de cada host, pensado para ficar numa TV do time. Cada cartão mostra CPU, RAM e disco com valor legível e cor própria e, quando o host roda Docker, também os containers com seu uso e estado; um relance mostra se o ambiente está saudável.",
      how: [
        "Cada cartão é um host; a cor da borda indica o estado geral (ok, atenção, crítico ou sem sinal).",
        "Dentro do cartão, cada recurso tem seu próprio semáforo: CPU em % de uso, RAM e disco em GB usados / totais.",
        "Se o host roda Docker, o cartão lista os containers (nome, estado e uso de CPU/memória), com as falhas em destaque no topo.",
        "O estado do cartão é o pior entre os três recursos e os alertas ativos daquele host, assim um host a 95% de CPU nunca aparece verde, mesmo sem regra de alerta.",
        "Deixe a tela aberta: ela atualiza sozinha a cada poucos segundos.",
        "Se um host aparecer cinza (\"sem sinal\"), o agente parou de reportar, nenhum número velho é mostrado.",
        "Admin: use o botão \"Limiares\" para ajustar quando cada recurso vira Alerta ou Crítico (global ou por host).",
        "Para exibir numa TV de forma pública use um token em TVs & Playlists.",
      ],
      faq: [
        {
          q: "A tela ficou parada, os dados estão velhos?",
          a: "Se a conexão ao vivo cai, aparece um aviso \"sem conexão\". Sem esse aviso, o que você vê é atual.",
        },
        {
          q: "Como cada recurso vira amarelo ou vermelho?",
          a: "Por limiares em % de uso. CPU e RAM: Saudável abaixo de 70%, Alerta de 70% a 90%, Crítico acima de 90%. Disco é mais rígido: Saudável abaixo de 75%, Alerta de 75% a 90%, Crítico acima de 90%. Um admin pode mudar esses valores no botão \"Limiares\", de forma global ou com override por host.",
        },
        {
          q: "Por que alguns cartões mostram containers e outros não?",
          a: "Só aparecem containers nos hosts em que o agente detecta o Docker. Sem Docker (ou sem nenhum container), o cartão simplesmente não traz a seção. O uso de CPU de cada container está na mesma escala do CPU do servidor: 0 a 100% da máquina inteira, então os containers somam para o total.",
        },
      ],
    },
    hosts: {
      title: "Infraestrutura",
      what: "Cada host (servidor/máquina) com o agente instalado aparece na listagem, em tabela densa (estado, CPU/RAM/disco, rede, uptime, último contato) ou em cartões, com as métricas ao vivo do servidor inteiro, cada recurso com seu próprio semáforo (verde/amarelo/vermelho). Traz também o inventário (núcleos, SO, kernel, IPs, versão do agente) e a lista de serviços descobertos automaticamente.",
      how: [
        "Admin: use \"Adicionar servidor\". O painel pergunta primeiro COMO instalar e explica o que cada caminho exige: (1) \"Configurar um servidor específico\", que baixa um instalador pronto com a chave daquele servidor dentro, serve para Linux, Windows e macOS; (2) instalar por SSH a partir do painel, sem tocar no servidor, só funciona em Linux com systemd; (3) baixar o instalador UNIVERSAL, um arquivo só que instala em vários servidores. A quarta opção, \"Chaves de acesso\", lista o que já foi criado e permite revogar ou excluir.",
        "Instale o agente num host para ele aparecer aqui (o passo-a-passo está no estado vazio).",
        "Em cada cartão veja CPU (% em uso), RAM e swap (GB usados / totais), disco por montagem (uma linha por ponto de montagem, ex.: \"/\" e \"/dados\"), rede (↓ download / ↑ upload por segundo) e nº de processos. A cor de cada recurso é o seu semáforo: verde saudável, amarelo alerta, vermelho crítico, a barrinha é proporcional ao uso.",
        "Use a busca para filtrar por nome de host, SO ou kernel.",
        "Renomeie um host para dar um apelido amigável (ex.: \"Banco de Produção\"): o nome técnico continua sendo a chave, e o apelido passa a aparecer nos seletores de servidor das outras telas.",
        "Clique no nome do servidor para abrir a página de detalhe, com abas (Visão geral, CPU, RAM, Discos, Rede, Containers, Processos, Alertas), seletor de intervalo de datas e o painel \"Informações do servidor\". Use \"Ver logs\" para abrir os logs já filtrados por aquele servidor.",
        "No modal do host, a aba \"Armazenamento de logs\" mostra quanto os logs ocupam no banco (ClickHouse) e, para admins, permite definir um limite e apagar logs antigos deste servidor por data.",
        "Veja a aba de serviços descobertos para achar o que roda em cada servidor sem configurar nada: servidores web (Apache, Nginx), PHP-FPM com as versões instaladas, bancos (MySQL/MariaDB, PostgreSQL, MongoDB, ClickHouse), Redis e os containers Docker. O agente procura a cada 5 minutos, e cada serviço tem um \"?\" que explica o que ele faz.",
      ],
      faq: [
        {
          q: "Como uso o instalador que baixei?",
          a: "Vale para os dois tipos (um servidor ou universal). No Linux e no macOS, leve o arquivo para o servidor e rode \"sudo sh <arquivo>\" (no macOS, pelo Terminal). No Windows, clique com o botão direito no .exe e escolha \"Executar como administrador\", ele instala o agente em C:\\revoada e o registra para iniciar junto com o Windows. Em todos os casos a chave de ingestão, o endereço do painel e os limites de recurso já vêm dentro do arquivo. O servidor aparece na lista em até um minuto.",
        },
        {
          q: "Tenho dez servidores para instalar. Preciso baixar dez arquivos?",
          a: "Não. Em \"Adicionar servidor\", escolha \"Baixar o instalador UNIVERSAL\", dê um nome ao lote e gere o instalador universal: um arquivo só, que roda em quantas máquinas você quiser. Ele não carrega chave, carrega um convite. Ao rodar, cada máquina pede ao painel a chave dela, então revogar um servidor não derruba os outros. A tabela mostra quantos entraram por cada lote.",
        },
        {
          q: "O instalador universal expira? E se o arquivo cair em mãos erradas?",
          a: "Ele vale até você revogar. Quem tiver o arquivo consegue cadastrar servidores no painel, não consegue ler dado nenhum nem entrar na sua conta, mas consegue poluir o inventário com máquinas falsas. Se isso acontecer, vá em \"Chaves de acesso\" e use \"Revogar\" na linha do lote: entradas novas param na hora, e os servidores que já entraram continuam reportando, porque cada um tem a sua chave. Cada entrada fica registrada na tela de Auditoria.",
        },
        {
          q: "Preciso escolher entre Apple Silicon e Intel no macOS?",
          a: "Não. O instalador do macOS descobre o processador do Mac na hora e baixa o binário certo (arm64 ou amd64). O mesmo arquivo serve para os dois.",
        },
        {
          q: "O instalador é secreto? Posso reaproveitar em outro servidor?",
          a: "Ele carrega a chave de ingestão daquele servidor: trate como senha e apague depois de instalar. Não reaproveite em outra máquina, cada servidor deve ter a sua chave, para que revogar uma não derrube as outras. Para reinstalar o mesmo servidor, use \"Instalador\" na linha da chave: reaproveita a chave existente em vez de criar mais uma.",
        },
        {
          q: "O que acontece com a senha/chave que digito em \"Adicionar servidor\"?",
          a: "Ela é usada para conectar ao servidor por SSH e instalar o agente e, depois, fica guardada cifrada num cofre, apenas para reprovisionar e atualizar o agente sem você redigitar. Nunca é exibida em claro nem devolvida pela API. Por ser uma credencial sensível, só administradores usam o recurso e a conexão com a central deve ser confiável (em produção a app é HTTPS).",
        },
        {
          q: "Para que serve \"Atualizar agente\" no cartão do servidor?",
          a: "Reexecuta a instalação num servidor que você adicionou por SSH, usando a credencial do cofre, sem redigitar senha ou chave. É a forma de atualizar o agente de toda a frota. Aparece só nos servidores provisionados por SSH.",
        },
        {
          q: "Como cada recurso vira amarelo ou vermelho?",
          a: "Pelos mesmos limiares em % de uso do Mural de Saúde. CPU e RAM: saudável abaixo de 70%, alerta de 70% a 90%, crítico acima de 90%. Disco é mais rígido: saudável abaixo de 75%, alerta de 75% a 90%, crítico acima de 90%. Cada ponto de montagem do disco recebe sua própria cor.",
        },
        {
          q: "A CPU aqui bate com a do painel do meu provedor (DigitalOcean, Contabo, AWS)?",
          a: "Sim, a partir do agente 0.8.1. O \"Uso de CPU\" mostra só o que a sua máquina realmente consumiu; o tempo que o hipervisor tomou para atender outro cliente sai numa métrica separada, \"CPU roubada pelo hipervisor\" (system.cpu.steal). Antes da 0.8.1 os dois vinham somados, e por isso o Revoada mostrava um número mais alto que o do provedor, medido num VPS real: 13,9% aqui contra 10,4% lá, porque 9,9 pontos eram roubo. Se você quiser a leitura antiga, que é \"quanto tempo meus processos passaram sem poder rodar\", basta somar as duas séries num painel: elas medem o mesmo intervalo e não se sobrepõem.",
        },
        {
          q: "O que é \"CPU roubada pelo hipervisor\" (steal) e quando devo me preocupar?",
          a: "É o tempo em que o seu servidor quis usar o processador e não conseguiu, porque o processador físico estava atendendo outro cliente do mesmo provedor. Não é consumo seu nem ociosidade: é fila. Abaixo de 5% sustentado é normal em VPS; acima de 10% sustentado significa que a máquina física está superlotada, e isso não se resolve otimizando o seu código, é assunto para o suporte do provedor pedir a migração da sua VM. Atenção ao configurar alertas: como o \"Uso de CPU\" não conta mais o steal, um servidor pode estar sufocado com a CPU marcando pouco. Crie também uma regra sobre system.cpu.steal.",
        },
        {
          q: "Por que aparece \"Sem métricas recentes\" num cartão?",
          a: "O host está no inventário mas parou de reportar (agente desligado, travado ou sem rede até o gateway). Não mostramos números velhos nem zeros inventados, só o inventário e este aviso, até o agente voltar a reportar.",
        },
        {
          q: "Instalei o agente e o host não apareceu.",
          a: "Confira se a chave de ingestão e o endereço do gateway estão certos na configuração do agente. Rode \"revoada-agent doctor\" para diagnosticar.",
        },
        {
          q: "O que significa \"sem sinal\"?",
          a: "O host já reportou antes mas parou. Normalmente o agente foi desligado, travou ou perdeu rede até o gateway.",
        },
        {
          q: "Como instalo em hospedagem compartilhada / cPanel (sem root)?",
          a: "Coloque o binário do agente na sua home e agende \"revoada-agent once\" pelo Cron Jobs do cPanel (a cada 1 min). Em servidor próprio com root, use o instalador com --cron. Defina um hostname único no agent.yaml, pois o host físico é compartilhado entre contas.",
        },
        {
          q: "Como instalo o agente num servidor Windows?",
          a: "Em Infraestrutura, clique em \"Adicionar servidor\", escolha \"Configurar um servidor específico\", dê um nome ao servidor, marque Windows e baixe o .exe. No servidor, clique nele com o botão direito e escolha \"Executar como administrador\": ele instala em C:\\revoada, escreve a configuração com a chave e registra a tarefa que sobe o agente junto com o Windows. A instalação manual (baixar o .exe puro, escrever o agent.yaml e criar a tarefa no schtasks) continua descrita no Guia, na seção Primeiros passos. No Windows o agente coleta CPU, RAM, disco, rede e processos; containers Docker e logs do sistema são recursos Linux e ficam de fora.",
        },
        {
          q: "O que é o \"limite de armazenamento de logs\" e como difere da retenção de 30 dias?",
          a: "São coisas distintas. A retenção de 30 dias (TTL) é automática: o banco descarta sozinho qualquer log com mais de 30 dias. O limite de armazenamento é um teto em disco (já vem definido em 4 GB por padrão) que serve só para enxergar no medidor se os logs estão crescendo demais (verde/amarelo/vermelho), ele não apaga nada sozinho; é um alerta visual. Um admin pode alterar o valor ou zerá-lo (0 = sem limite). Para reduzir de fato o que está guardado, use \"Apagar logs\".",
        },
        {
          q: "Apagar logs por data remove mesmo? Afeta os outros servidores?",
          a: "Remove de verdade (o dado é fisicamente excluído do banco e o espaço é liberado, não há lixeira). A exclusão é sempre só do servidor que você abriu: escolha \"manter últimos 7 dias\", \"manter só hoje\", uma data específica ou apagar tudo daquele host. Os logs dos outros servidores não são tocados. Antes de confirmar, o painel mostra quantas linhas serão apagadas. É uma ação de admin e irreversível.",
        },
        {
          q: "Preciso apagar UMA linha de log que vazou um dado pessoal. Dá para não apagar o resto?",
          a: "Dá, é para isso que existe o expurgo cirúrgico, na aba \"Armazenamento de logs\" do servidor (só admin). Ele recorta por três eixos ao mesmo tempo, e você usa quantos precisar: (1) CONTEÚDO, um trecho literal da linha (\"contém\", sem diferenciar maiúsculas); (2) JANELA, de quando até quando; (3) ALVO, o servidor aberto OU as \"linhas sem servidor\". Combinando os três, apaga-se exatamente o que precisa sair, em vez de \"tudo daquele servidor antes de tal data\", que era a única opção antes e é destruição em massa para remover uma linha. O painel mostra o número de linhas atingidas ANTES de você confirmar, e o recorte pré-visualizado é literalmente o mesmo que será executado.",
        },
        {
          q: "O que são as \"linhas sem servidor\" no expurgo?",
          a: "Logs que chegaram sem rótulo de servidor, tipicamente enviados por OTLP direto de uma aplicação, e não pelo agente. Elas existem, aparecem na tela de Logs como \"(sem servidor)\" e não pertencem a host nenhum; sem essa opção elas seriam inalcançáveis pela interface, justamente no caminho por onde é mais provável um segredo de terceiro entrar. Por ser um alvo largo (atinge o que veio de fora do agente), o painel exige que você digite a confirmação por extenso antes de executar.",
        },
        {
          q: "O expurgo é reversível? E o que ele NÃO alcança?",
          a: "Não é reversível: o banco reescreve os arquivos e o dado some, sem lixeira. E ele alcança só a tabela de LOGS, por isso, ao terminar, a tela mostra a lista \"não alcançado\" (not_purged): o mesmo texto pode continuar existindo nas métricas por minuto (90 dias), no rollup de métricas por hora (730 dias, a cópia mais longeva do produto), nos eventos (90 dias), no registro de notificações enviadas (120 dias) e no backup diário, que não tem expiração automática e precisa ser tratado fora do painel. Quando você busca por um trecho, o próprio comando do expurgo fica registrado no banco por um tempo, citando o trecho. Ler essa lista é parte do procedimento: fechar um pedido de LGPD sem ela é declarar apagado um dado que continua vivo por até dois anos.",
        },
        {
          q: "Por que a lista de chaves não mostra mais a chave inteira?",
          a: "Porque a listagem inteira era um vazamento à espera de acontecer: numa única requisição, uma sessão de administrador comprometida (aba aberta, token roubado) entregava a credencial de ingestão de TODOS os servidores, e com ela se escreve métrica, log e heartbeat em nome de qualquer host. Agora a lista mostra a chave mascarada (\"dev-…46\"), que serve para você reconhecer a linha e não serve para autenticar. O texto completo existe em dois momentos: quando a chave é criada (uma vez, para você copiar) e quando alguém clica em \"Revelar\", uma chave por vez.",
        },
        {
          q: "Revelar uma chave fica registrado?",
          a: "Fica. \"Revelar\" é uma ação de escrita justamente para passar pela trilha de auditoria: quem revelou, a chave de qual servidor e quando. Roubar a frota deixa de ser uma única consulta silenciosa e passa a ser N ações registradas, uma a uma, na tela de Auditoria. Se você só precisa reinstalar um servidor, prefira gerar o instalador, ele já leva a chave dentro e não exige revelar nada.",
        },
        {
          q: "O que faz o botão \"Apagar servidor\" (vermelho) no cartão?",
          a: "Apaga o servidor por completo e é irreversível: remove TODOS os dados dele no painel, inventário, métricas e históricos, logs, traces, eventos, a chave de ingestão, serviços descobertos, limiares, as permissões de quem podia vê-lo e a participação dele em grupos; o host também sai do escopo das regras de alerta. Para o agente na máquina há duas escolhas no modal: (1) ele se desinstala sozinho, a ordem viaja na consulta que ele já faz de hora em hora, sem SSH e sem senha, e ele avisa o painel quando terminar; (2) apagar só os dados do painel, para máquina já desativada, em que não há agente a quem pedir. A auto-desinstalação depende do agente estar rodando, atualizado e com `panel_url` configurado, máquina desligada só recebe a ordem quando voltar, e o painel desiste dela depois de sete dias. Se escolher apagar só os dados, pare o agente ANTES: enquanto ele reporta, o servidor volta a aparecer sozinho por alguns segundos (o painel faz uma segunda limpeza um minuto e meio depois para levar esse rastro embora). Para evitar engano, é preciso digitar o hostname exato e confirmar. Só admins veem o botão.",
        },
      ],
    },
    dashboards: {
      title: "Dashboards",
      what: "Coleções de painéis (gráficos e números) que você monta para acompanhar métricas. Ficam salvos, versionados e podem ir para uma TV.",
      how: [
        "Comece pela \"Visão do Host\": escolha um servidor e o Revoada gera um dashboard pronto com o servidor inteiro (CPU, memória, disco, rede e tempo no ar). É o caminho recomendado.",
        "Prefere montar do zero? Use \"Dashboard em branco\" (avançado) e adicione os painéis um a um.",
        "Abra o dashboard e use \"Editar\" para adicionar, reordenar ou remover painéis.",
        "Em cada painel, defina o escopo: por padrão o painel mede o servidor inteiro; opcionalmente foque num container específico.",
        "Marque \"agrupar por host\" num painel para ter uma série por servidor e comparar vários servidores no mesmo gráfico.",
        "Toda alteração salva vira uma versão; use o histórico para restaurar uma anterior.",
      ],
      faq: [
        {
          q: "Qual a diferença entre \"Visão do Host\" e \"Dashboard em branco\"?",
          a: "A Visão do Host é o caminho recomendado: você só escolhe o servidor e o dashboard já vem pronto com o servidor inteiro (CPU, memória, disco, rede, tempo no ar). O Dashboard em branco é o modo avançado: começa vazio, para você montar cada painel manualmente.",
        },
        {
          q: "Um painel mede o servidor inteiro ou um serviço?",
          a: "Por padrão, o servidor inteiro, é o escopo mais útil e evita painéis confusos. Ao adicionar um painel você pode, opcionalmente, focar num container Docker específico: esses já têm métricas próprias (container.cpu, container.mem, container.running…). Já os serviços de aplicação descobertos (MySQL, Nginx...) ainda não têm métricas dedicadas; elas dependem de exporters e chegam numa etapa futura.",
        },
        {
          q: "Apaguei painéis sem querer, dá para voltar?",
          a: "Sim. Abra o histórico de versões do dashboard e restaure a versão anterior. Restaurar cria uma nova versão e não apaga o histórico.",
        },
        {
          q: "Por que não tem arrastar-e-soltar?",
          a: "O editor é por formulário (com botões de subir/descer). É de propósito: entrega o mesmo resultado de forma mais previsível e à prova de erros.",
        },
      ],
    },
    explore: {
      title: "Explore",
      what: "Área para montar uma consulta rápida a uma métrica sem criar um dashboard. Serve para investigar uma dúvida pontual (\"como está a CPU deste serviço na última hora?\").",
      how: [
        "Escolha a métrica num select (a lista vem do que já foi coletado).",
        "Use o seletor \"Servidor\" para investigar um único servidor ou deixe em \"Todos\" para agregar todos.",
        "Defina a agregação (média, soma, máximo...) e por qual rótulo agrupar (ex.: por host, uma linha por servidor).",
        "Ajuste a janela de tempo e veja o resultado no gráfico.",
        "Se gostar do resultado, use \"salvar em dashboard\" para guardar como painel.",
      ],
      faq: [
        {
          q: "A métrica que quero não aparece na lista.",
          a: "Só aparecem métricas que já chegaram ao sistema. Se ainda não coletou aquele dado, instrumente o serviço ou instale o agente no host primeiro.",
        },
      ],
    },
    logs: {
      title: "Logs",
      what: "Busca em todos os registros de texto (logs) do seu ambiente, com histograma, padrões e acompanhamento ao vivo. Não são só logs de aplicação: o agente também coleta o servidor inteiro, journald, syslog, containers Docker e o kernel (dmesg), então dá para investigar o que aconteceu no sistema, e não só nos seus serviços.",
      how: [
        "Digite um termo na busca ou use um dos exemplos clicáveis para começar.",
        "Filtre por servidor, por serviço e por severidade (erro, aviso, informação...).",
        "Use o filtro \"Fonte\" para escolher de onde vêm as linhas: Sistema (journald), Containers (Docker), Syslog, Kernel (dmesg) ou Arquivo. \"Todas as fontes\" mistura tudo.",
        "Ative o \"ao vivo\" para ver novas linhas chegando em tempo real.",
        "Cuidado com os pisos de severidade: INFO+, WARN+ e ERROR+ ESCONDEM as linhas sem nível declarado (\"não classificado\"), que na maioria dos servidores são o grosso do log. Se a busca ficou estranhamente vazia, volte para \"qualquer nível\" ou escolha \"não classificado\".",
        "Se uma busca é importante, use \"salvar como métrica\" para contá-la ao longo do tempo.",
      ],
      faq: [
        {
          q: "O que \"salvar como métrica\" cria?",
          a: "Cria uma métrica numérica que conta quantas linhas batem com a sua busca a cada intervalo. Assim você pode gráficar e até alertar sobre a frequência daquele log.",
        },
        {
          q: "O que é a severidade \"não classificado\"?",
          a: "São as linhas em que o agente não conseguiu ler um nível. Nem todo log declara severidade: uma linha crua do journald, do syslog ou da saída de um container muitas vezes chega só como texto, e o painel não inventa um nível que o programa não escreveu, ela fica como \"não classificado\" (UNKNOWN). Não é erro nem perda: a linha está inteira e pesquisável.",
        },
        {
          q: "Filtrei por INFO+ e quase tudo sumiu. O painel perdeu meus logs?",
          a: "Não. Os pisos de nível são um corte por número de severidade, e linha sem nível declarado vale zero, ou seja, QUALQUER piso (INFO+, WARN+, ERROR+) descarta todo o \"não classificado\" junto. Medido no ambiente de desenvolvimento: de 75 linhas, 71 eram não classificadas; filtrar \"INFO+\" para tirar ruído escondia 95% do log e dava a impressão de que o servidor tinha ficado quieto. Para ver só essas linhas, escolha \"não classificado\" no seletor; para ver tudo, \"qualquer nível\".",
        },
        {
          q: "Não acho o log de um serviço.",
          a: "Confira se o serviço realmente envia logs ao Revoada e se a janela de tempo cobre o momento. Logs antigos podem já ter sido removidos pelo TTL.",
        },
      ],
    },
    traces: {
      title: "Traces",
      what: "Mostra o caminho completo de uma requisição passando por vários serviços (waterfall), com a duração de cada etapa. Serve para achar onde uma operação está lenta ou falhando.",
      how: [
        "Filtre por servidor, por serviço, por status (erro) ou por duração mínima para achar traces interessantes.",
        "Abra um trace para ver a cascata (waterfall) de spans e onde o tempo foi gasto; um ⚠ marca os spans que registraram uma exception.",
        "Repare no selo \"parcial\": esse trace perdeu spans para a amostragem, então a operação mostrada como raiz pode não ser a raiz de verdade e a duração é um piso (o real é igual ou maior). Para comparar latência, escolha um trace sem esse selo.",
        "Clique num span para ver o detalhe, incluindo tipo, mensagem e stacktrace da exception, quando houver.",
        "Use o botão \"Logs deste trace\" no waterfall para ver os logs correlacionados de forma exata pelo trace_id (não só por serviço + momento).",
        "Para ver traces aqui, instrumente seus serviços com OTLP (veja Instrumentação / Enviar traces).",
        "Site em PHP no cPanel? Use o botão \"Instrumentar PHP (cPanel)\" (só admin) para gerar um arquivo pronto e ligar a instrumentação sem tocar no código, uma linha no MultiPHP INI Editor (auto_prepend_file) e cada requisição vira um trace.",
      ],
      faq: [
        {
          q: "Por que não vejo 100% dos traces?",
          a: "É intencional. O Revoada usa tail sampling: guarda os traces com erro ou acima de 1 segundo e cerca de 20% do restante, para economizar armazenamento. A decisão é tomada assim que o trace aparece e vale para os spans que chegarem depois. Quando um trace só se revela interessante mais tarde, o erro estava num span que chegou por último, os spans já descartados não voltam: ele é guardado, mas marcado como parcial.",
        },
        {
          q: "O que significa o selo \"parcial\" num trace?",
          a: "Que faltam spans nele. O trace foi descartado pela amostragem e depois promovido a guardado, e o que já tinha sido jogado fora não pôde ser recuperado. Consequência prática: a operação exibida como raiz pode ser apenas o span mais antigo que sobrou (e não a requisição que o usuário realmente fez), e a duração é um PISO, o tempo real é igual ou maior, por isso ela aparece como \"≥ 900 ms\". Use traces parciais para investigar o que aconteceu, não para medir latência nem para comparar operações.",
        },
        {
          q: "Como vejo traces do meu site PHP (cPanel)?",
          a: "Na tela de Traces, clique em \"Instrumentar PHP (cPanel)\" (botão de admin): escolha o servidor/chave e o nome do serviço e o painel gera um revoada-tracer.php pronto. Suba o arquivo pelo File Manager na sua HOME, fora do public_html, e adicione 1 linha no MultiPHP INI Editor, auto_prepend_file = /home/SEU_USUARIO/revoada-tracer.php (ou um .user.ini na pasta do site com a mesma linha). Isso registra 1 trace por requisição (rota, duração, status HTTP), sem alterar o código e sem somar latência. Para spans profundos (queries SQL, chamadas internas) é preciso a extensão OpenTelemetry do PHP.",
        },
        {
          q: "Como vejo os logs exatos de um trace?",
          a: "Abra o trace e clique em \"Logs deste trace\". A correlação é feita pelo trace_id, então só aparecem as linhas de log daquela mesma requisição. Para funcionar, o serviço precisa incluir o trace_id nos logs que envia.",
        },
      ],
    },
    instrumentacao: {
      title: "Instrumentação / Enviar traces",
      what: "Como fazer seus serviços exportarem traces (e logs/métricas) para o Revoada via OTLP. Sem instrumentar, as telas de Traces e Service Map ficam vazias.",
      how: [
        "Aponte o exportador OTLP da sua aplicação para o endpoint público do gateway: http://<gateway>:8090 (ex.: http://SEU-GATEWAY:8090). O SDK acrescenta /v1/traces sozinho.",
        "Use OTLP/HTTP com protobuf (recomendado) ou JSON; gzip é opcional (com ou sem, ambos funcionam).",
        "Autentique enviando o header X-Revoada-Key: <serverkey> (a mesma chave de ingestão usada pelo agente).",
        "Defina o atributo de recurso service.name com o nome do serviço, é ele que aparece na lista de traces e no service map. Use host.name para casar o trace com um servidor no filtro \"Servidor\".",
        "Gere tráfego e volte à tela de Traces: traces com erro ou lentos aparecem primeiro.",
      ],
      faq: [
        {
          q: "Como configuro em cada linguagem (PHP, Java/Spring, Node, Python, .NET, Go)?",
          a: "Todas usam o SDK/agente do OpenTelemetry com exportador OTLP/HTTP. Em geral basta definir as variáveis de ambiente OTEL_EXPORTER_OTLP_ENDPOINT=http://<gateway>:8090, OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf, OTEL_EXPORTER_OTLP_HEADERS=X-Revoada-Key=<serverkey> e OTEL_SERVICE_NAME=<seu-serviço>. A visão geral do endpoint com receitas por linguagem está em docs/instrumentacao-traces.md; o passo a passo detalhado de PHP está em docs/instrumentacao-php.md.",
        },
        {
          q: "Preciso instalar o agente para enviar traces?",
          a: "Não. O agente coleta o servidor inteiro, CPU, memória e disco, mas também os containers Docker (uso e estado) e os logs do sistema (journald, syslog, containers e kernel/dmesg). Os traces, por outro lado, vêm direto da aplicação instrumentada com OpenTelemetry, exportando em OTLP para o gateway. Agente e instrumentação são complementares e independentes.",
        },
      ],
    },
    alerts: {
      title: "Alertas",
      what: "Onde você cria regras que vigiam suas métricas e avisam quando algo passa de um limite. Também lista os alertas ativos e o histórico.",
      how: [
        "Vá em Regras e use o assistente de 3 passos: o quê (métrica/filtros), quando (limite e duração) e quem (rota/severidade).",
        "Atalho \"+ Container caído\": abre o assistente já preenchido para avisar quando um container do Docker para de rodar (métrica container.running, mínimo < 1, severidade crítica). Revise (ex.: restrinja a um servidor ou a um container por filtro) e salve pelo fluxo normal.",
        "Use o preview para ver quantas vezes a regra teria disparado nos últimos dias antes de salvar.",
        "Ligue ou desligue uma regra pelo interruptor, sem apagá-la.",
        "Nas abas Ativos e Histórico, use o seletor \"Servidor\" para focar nos alertas de um único servidor.",
        "Em Ativos, reconheça (ack) um alerta com um clique.",
        "A regra \"Servidor parou de reportar\" já vem pronta e marcada como \"de fábrica\": ela avisa quando um servidor fica mudo. Você pode ajustar o tempo tolerado e os canais, mas não desligá-la nem apagá-la.",
      ],
      faq: [
        {
          q: "Como aviso quando um container do Docker cai?",
          a: "Use o atalho \"+ Container caído\" na aba Regras. Ele pré-preenche uma regra com a métrica container.running (vale 1 no ar, 0 parado), agregada por mínimo, condição \"< 1\" e severidade crítica, ou seja, dispara assim que qualquer container monitorado zera. Revise o escopo (todos os servidores, um servidor específico, ou um container por filtro) e salve.",
        },
        {
          q: "Criei a regra para todos os containers, mas os novos não entram.",
          a: "Reabra o aviso de container caído e confira a opção no topo. \"Todos os containers\" acompanha sozinho o que for criado depois. Se estiver em \"só os containers que eu escolher\", a lista fica congelada no que foi marcado, e um container novo só passa a ser vigiado se você voltar e marcá-lo. Na dúvida, escolha \"todos\".",
        },
        {
          q: "Um container está parado de propósito. Como paro de receber alerta dele?",
          a: "Na aba Ativos, use o botão de sino cortado na linha do alerta do container. O painel deixa de alertar sobre ele em todas as regras, o alerta atual é encerrado e ele sai da TV; nada muda no servidor. Para voltar a vigiar, use a aba Regras › Containers ignorados.",
        },
        {
          q: "Criei a regra mas nada dispara.",
          a: "Confira o limite, o operador e principalmente o campo \"por\" (a violação precisa persistir esse tempo). O preview retroativo ajuda a calibrar antes de confiar na regra. Lembre também do piso: mesmo com \"por\" igual a 0, o painel só abre o alerta depois de 2 avaliações seguidas violando, cerca de 1 minuto. Se você derrubou algo agora e nada apareceu nos primeiros 30 segundos, espere o ciclo seguinte.",
        },
        {
          q: "Por que o alerta demora cerca de um minuto para aparecer, mesmo com \"por\" = 0?",
          a: "Porque uma única avaliação nunca foi evidência suficiente. Antes, um pico de um segundo, às vezes uma amostra só dentro da janela, bastava para abrir um incidente, e um valor passeando em volta do limite (91, 89, 91, 89…) abria e fechava o alerta a cada 30 segundos, gerando dezenas de mensagens sobre um problema só. Agora o painel exige 2 avaliações consecutivas para abrir e 2 para encerrar. O custo é cerca de um minuto de atraso; o ganho é que o que chega até você é problema, não ruído.",
        },
        {
          q: "O que significa a mensagem \"SEM DADOS\" que recebi?",
          a: "Que o alerta saiu da lista de ativos porque as medições PARARAM DE CHEGAR, não porque o problema acabou. É diferente de \"resolvido\": o servidor pode estar pior, inclusive desligado. Antes as duas situações mandavam o mesmo texto verde de recuperação, e um servidor que morreu era anunciado como recuperado. Ao receber \"SEM DADOS\", confira se a máquina e o agente estão de pé.",
        },
        {
          q: "O painel avisa se um servidor simplesmente parar de responder?",
          a: "Sim, pela regra de fábrica \"Servidor parou de reportar\". Ela é necessária porque todas as outras regras comparam um VALOR, e um servidor mudo não tem valor nenhum para comparar, \"maior que 90\" é falso quando não há número, então um agente morto deixaria todos os outros alertas em silêncio. Ela dispara quando um servidor do inventário fica mais de 5 minutos sem nenhum sinal (ajustável no campo \"Limite\" da regra). Um servidor cadastrado que nunca reportou não conta: não há ausência a detectar em quem nunca esteve presente.",
        },
        {
          q: "Recebi \"OSCILANDO\" e depois nada. O incidente terminou?",
          a: "Quando um alerta fica abrindo e fechando muitas vezes seguidas, o painel manda um aviso de \"oscilando\" e engole os seguintes para não virar enxurrada. Isso agora é visível: as mensagens dizem quantas transições foram suprimidas na última hora, e assim que o vaivém para de verdade o painel envia a resolução, mesmo tendo suprimido as anteriores. Se você recebeu só o \"oscilando\", o incidente ainda estava em curso.",
        },
        {
          q: "Reconhecer (ack) resolve o alerta?",
          a: "Não. Ack apenas avisa ao time que alguém está cuidando; o alerta continua ativo até a métrica voltar ao normal. Fica registrado quem reconheceu.",
        },
        {
          q: "O alerta é de algo que eu removi de propósito e não sai da lista. Como encerro?",
          a: "Use \"Marcar como resolvido\" na linha do alerta (só administradores). Ele sai dos ativos e vai para o histórico marcado como encerrado à mão, com o seu nome, assim quem consultar depois distingue \"o problema passou\" de \"alguém disse que passou\". Atenção: isso encerra a ocorrência, não silencia a regra. Se a condição voltar a valer, um alerta novo dispara. Quando o recurso deixou de existir de vez (um container removido), o próprio painel encerra sozinho por falta de dados em alguns minutos; a marcação manual serve para não esperar.",
        },
      ],
    },
    notify: {
      title: "Canais de alerta",
      what: "Configura os canais para onde os alertas são enviados: e-mail (SMTP), webhook, Telegram e WhatsApp (via Evolution API). Cada canal é um destino que você cadastra e testa antes de usar.",
      how: [
        "Cadastre um destino (e-mail, webhook, Telegram ou WhatsApp) e clique em \"enviar teste\" para confirmar que a entrega funciona.",
        "Para WhatsApp, preencha primeiro o card de integração (Evolution API): URL base, instância e apikey; os canais de WhatsApp reusam essa configuração.",
        "Edite ou remova um canal a qualquer momento; o teste continua disponível para revalidar a entrega.",
      ],
      faq: [
        {
          q: "O alerta dispara mas ninguém recebe.",
          a: "Confirme que o canal passou no teste (\"enviar teste\") e que a regra de alerta aponta para ele. Um canal que falha no teste não vai entregar o alerta real.",
        },
        {
          q: "Como recebo alertas no WhatsApp?",
          a: "Preencha o card de integração WhatsApp (Evolution API) com a URL base, a instância pareada e a apikey; depois crie um canal do tipo WhatsApp informando o número de destino. Todos os canais de WhatsApp compartilham a mesma integração.",
        },
        {
          q: "Como faço um usuário receber no WhatsApp só os alertas dos servidores dele?",
          a: "Crie (ou edite) um canal de WhatsApp com o número dessa pessoa e, no campo \"Canal pessoal de\", selecione o usuário. Depois, na tela Usuários & Acessos, marque \"Receber alerta\" nos servidores que ela deve acompanhar. A partir daí, quando um alerta de um desses servidores dispara, ele chega no canal pessoal dela, além dos canais fixos da regra. Um canal pessoal não recebe os alertas dos outros servidores.",
        },
        {
          q: "Onde vejo o que já foi enviado?",
          a: "No botão \"Histórico de envios\", no topo da tela: abre um modal com o registro de cada notificação disparada, quando, por qual canal, o assunto e se deu certo.",
        },
        {
          q: "O que significam \"enviado\", \"aguardando confirmação\" e \"falhou\" no histórico?",
          a: "\"Enviado\" quer dizer que o provedor confirmou ter recebido a mensagem para entregar (no WhatsApp, que ela passou pelos servidores dele, não que já apareceu no aparelho). \"Aguardando confirmação\" é o estado de quem ainda não respondeu: o painel volta a perguntar sozinho e atualiza a linha em poucos minutos. \"Falhou\" traz o motivo ao lado. Todo envio de WhatsApp passa por \"aguardando confirmação\", porque a Evolution API só aceita a mensagem na hora e entrega depois.",
        },
        {
          q: "O teste de WhatsApp deu \"aceito, mas a entrega não foi confirmada\". E agora?",
          a: "A Evolution API recebeu a mensagem, mas o WhatsApp não confirmou a entrega no tempo em que o botão esperou. Abra o \"Histórico de envios\" em um minuto: a linha vira \"entregue\" ou \"falhou\" com o motivo. A recusa mais comum acontece quando o destinatário nunca conversou com o número do painel, peça que ele mande qualquer mensagem para esse número e teste de novo.",
        },
      ],
    },
    websites: {
      title: "Websites & Jornadas",
      what: "Monitora se seus sites estão no ar, medindo tempo de resposta e validade do certificado a partir de várias sondas. Jornadas testam fluxos de várias etapas (ex.: login e compra).",
      how: [
        "Cadastre um check com a URL e o intervalo de teste.",
        "Escolha as sondas (locais de onde o site é testado) e o grupo para a status page.",
        "Para fluxos com passos, monte uma jornada escolhendo ações (abrir URL, preencher, clicar, esperar texto).",
        "Acompanhe o estado (UP, DEGRADADO, SUSPEITO, DOWN), o uptime% e o certificado.",
      ],
      faq: [
        {
          q: "Meu site caiu mas o estado só ficou DEGRADADO, por quê?",
          // O texto anterior ("um site só vira DOWN quando pelo menos 2 sondas falham")
          // é falso no caso PADRÃO — check sem sondas designadas, em que a origem é
          // única (a central) e bastam 2 falhas consecutivas dela. Quem lia tratava
          // como corroborado por vários pontos o que pode ser um soluço da nossa rede.
          a: "É proteção contra falso positivo, e ela depende de quantas sondas o check tem. Com 2 ou mais sondas designadas, o site só vira DOWN quando pelo menos 2 delas falham, se só 1 falha, fica DEGRADADO, porque pode ter sido um problema de rede daquela sonda. Com origem única (o padrão, sem sondas designadas), o DOWN vem de 2 falhas consecutivas da sonda central: a confirmação é no tempo, não em pontos de vista diferentes, então uma queda da rede da própria central também aparece como site fora do ar.",
        },
        {
          q: "O que significam DNS, TLS e TTFB no tempo?",
          a: "É o tempo de resposta dividido em fases: DNS (achar o endereço), TLS (negociar a conexão segura) e TTFB (tempo até o primeiro byte da resposta). Ajuda a saber onde está a lentidão.",
        },
      ],
    },
    tvs: {
      title: "TVs & Playlists",
      what: "Gera links (tokens) somente-leitura para exibir dashboards e o Mural de Saúde em telas de TV, sem login. Playlists fazem a TV alternar entre várias telas.",
      how: [
        "Crie um token informando nome, local e qual dashboard mostrar.",
        "Abra o endereço #/tv/{token} na própria TV para exibir em modo cheia-tela.",
        "Crie uma playlist com vários itens e o tempo de cada um em segundos.",
        "Atribua a playlist ao token para a TV alternar entre as telas sozinha.",
      ],
      faq: [
        {
          q: "É seguro deixar esse link numa TV pública?",
          a: "Sim. O token é somente-leitura e não permite login nem alterações. Se um token vazar, basta revogá-lo.",
        },
        {
          q: "A TV mostra os alertas ou só o dashboard?",
          a: "Ambos. Além do dashboard ou da playlist, a TV destaca os alertas: quando um alerta novo dispara aparece um aviso em destaque por alguns segundos, e um alerta crítico assume a tela inteira até normalizar, assim ninguém precisa estar olhando no momento exato.",
        },
      ],
    },
    users: {
      title: "Usuários & Acessos",
      what: "Tela de administrador para cadastrar usuários e definir, servidor a servidor, o que cada um pode ver, editar e de quais recebe alerta. Grupos de servidores são um atalho: em vez de marcar os mesmos servidores para vários usuários, cria-se o grupo uma vez e vincula-se aos usuários. Administradores veem e podem tudo; usuário comum só enxerga o que for liberado aqui.",
      how: [
        "Aba Usuários: use \"Novo usuário\" para cadastrar (nome, email, celular e senha provisória). O email é o login do usuário; ele troca a senha no primeiro acesso.",
        "O celular cria automaticamente um canal pessoal de WhatsApp já vinculado ao usuário, ele passa a receber alertas assim que você liberar servidores com \"Receber alerta\".",
        "Marque \"Administrador\" só para quem deve gerenciar o painel inteiro; o resto é usuário comum, restrito às permissões por servidor.",
        "Clique em \"Permissões\" de um usuário para abrir a matriz: por servidor, marque Ver, Editar e/ou Receber alerta. Editar e Receber alerta implicam Ver automaticamente.",
        "Prefira grupos quando um conjunto de servidores é liberado junto (ex.: \"Servidores Revoada\"): crie o grupo na aba Grupos e depois marque-o nas permissões do usuário.",
        "Use \"Editar\" para trocar o papel, desativar a conta (deixa de logar e de receber alerta), redefinir a senha ou excluir o usuário (remove em definitivo a conta, permissões e o canal pessoal de WhatsApp).",
        "Fique de olho no aviso \"N servidores sem permissão\": são servidores que nenhum usuário comum vê ainda, atribua-os a um usuário ou grupo.",
      ],
      faq: [
        {
          q: "Criei um usuário e ele não vê nada. É bug?",
          a: "Não. Todo usuário comum nasce sem acesso a nenhum servidor, é o padrão seguro. Abra \"Permissões\" e libere os servidores (ou vincule um grupo). Só então ele passa a ver aqueles servidores em todas as telas.",
        },
        {
          q: "Qual a diferença entre permissão direta e grupo?",
          a: "As duas concedem acesso e se somam. A permissão direta é marcada na matriz daquele usuário. O grupo é um atalho: liga vários servidores de uma vez para quem você vincular. Um servidor pode estar em vários grupos (compartilhado), quem participa de qualquer um deles o vê.",
        },
        {
          q: "Como um usuário recebe alerta no WhatsApp dos servidores dele?",
          a: "Ao cadastrar com celular, o canal pessoal de WhatsApp já é criado e vinculado. Falta só marcar \"Receber alerta\" nos servidores desejados em Permissões. Quando um alerta de um servidor liberado dispara, ele chega no canal pessoal do usuário, além dos canais fixos da regra. (Você pode conferir ou ajustar o número na tela Canais de alerta.)",
        },
        {
          q: "Com o que o usuário faz login?",
          a: "Com o email cadastrado. A senha é a provisória que você definiu; no primeiro acesso o sistema obriga a troca. O administrador de bootstrap (\"admin\") continua entrando pelo nome de usuário original.",
        },
        {
          q: "Por que não consigo desativar/rebaixar um administrador?",
          a: "O painel impede remover o último administrador ativo, para não ficar sem quem administre. Crie ou promova outro admin antes.",
        },
      ],
    },
    agentUpdates: {
      title: "Atualização dos agentes",
      what: "Tela de administrador que decide quando o agente instalado nos seus servidores troca de versão. Por padrão cada agente pergunta ao painel a cada hora se há versão nova e se atualiza sozinho, cômodo no dia a dia, arriscado numa noite ruim: uma versão com defeito chegaria a toda a frota em cerca de uma hora. Aqui você desliga essa troca automática, prende a frota numa versão conhecida, ou segura um servidor específico. A lista mostra um SERVIDOR por linha, os mesmos de Infraestrutura, com a versão do agente que cada um está rodando e o que ele relatou na última tentativa de atualização.",
      how: [
        "Para parar tudo agora: marque \"Desligar a auto-atualização de toda a frota\", escreva o motivo e salve. Os agentes seguem coletando normalmente; só param de trocar de versão.",
        "Para prender a frota numa versão conhecida (mais comum que desligar): preencha \"Fixar uma versão\" com algo como 0.9.1 e salve. Quem estiver atrás sobe até ela e ninguém passa disso.",
        "O motivo é obrigatório ao desligar ou fixar, e aparece para quem abrir a tela depois. Escreva pensando em quem vai encontrar a frota parada de madrugada.",
        "Para deixar UM servidor de fora (o banco de produção, por exemplo), use \"Segurar\" na linha dele. O freio do servidor vence a política da frota, e o motivo também é obrigatório, ele aparece ao lado do servidor para quem chegar depois.",
        "A lista é de SERVIDORES, os mesmos de Infraestrutura, com a versão do agente que cada um está rodando de verdade.",
        "Confira a coluna \"Última atualização\": ela mostra o que cada agente relatou na última tentativa, inclusive as que falharam.",
        "Servidores marcados como \"só troca reinstalando\" nunca se atualizam sozinhos: rode o instalador do painel na máquina para trocar a versão deles.",
        "Se o botão \"Segurar\" estiver desligado, leia a coluna \"Freio\": o painel não conseguiu dizer com certeza qual chave de acesso é daquele servidor, e prefere não agir a agir na máquina errada.",
      ],
      faq: [
        {
          q: "Por que alguns servidores aparecem sem o botão \"Segurar\"?",
          a: "Porque o freio é gravado na CHAVE DE ACESSO do servidor, não no nome dele: é a chave que o agente apresenta quando pergunta ao painel se há versão nova, e é o único identificador que o painel reconhece nessa hora. A tela mostra servidores e resolve servidor → chave sozinha, por duas vias: a exata, quando o próprio agente informa o nome da máquina ao consultar; e a aproximada, quando o nome que alguém digitou ao criar a chave bate com o servidor. Quando nenhuma das duas resolve, ou quando duas chaves disputam a mesma máquina, o botão fica desligado e a coluna \"Freio\" diz o motivo. Um freio aplicado na chave errada pararia a atualização de outro servidor, o que é pior do que não oferecer o freio.",
        },
        {
          q: "O que significa \"não pergunta ao painel\" na coluna de atualização?",
          a: "Que aquele servidor nunca consultou o painel sobre versões, não há registro nenhum de consulta da chave dele. É o esperado em agentes anteriores à auto-atualização (a frota 0.7.0 é assim) e em agentes instalados sem o endereço do painel. Consequência prática: ele NÃO troca de versão sozinho, por mais que se espere, e a política da frota não o alcança. Para atualizá-lo e ligar o relato, reinstale o agente pelo painel (Infraestrutura → Adicionar servidor). A coleta de métricas e logs não é afetada por nada disso.",
        },
        {
          q: "O que é a lista \"Chaves sem servidor identificado\", no fim da tela?",
          a: "São as chaves de acesso que o painel não conseguiu atribuir com segurança a nenhum servidor: batizadas com um nome que não é o de nenhum servidor (\"Teste traces\"), de servidores que ainda não reportaram, ou em disputa com outra chave pela mesma máquina. Elas ficam visíveis de propósito, enquanto o agente não informar o próprio nome, é a única forma de segurar a atualização daqueles hosts. Antes de usar o freio ali, confirme de qual servidor é a chave.",
        },
        {
          q: "Mudei a política. Quando ela passa a valer?",
          a: "Na próxima vez que cada agente perguntar ao painel, até 1 hora. Ela também não desfaz nada: quem já se atualizou continua na versão nova. Para voltar um servidor de versão de propósito, fixe a versão antiga aqui e reinstale o agente naquela máquina.",
        },
        {
          q: "O que significa \"só troca reinstalando\" na coluna de versão?",
          a: "Que aquele servidor não tem, no sistema, o componente que troca o binário do agente por outro (ele roda em modo cron/launchd, ou com systemd anterior à versão 231). Ele pergunta ao painel e relata normalmente, mas nunca vai se atualizar sozinho, esperar não resolve. Para atualizá-lo, gere o instalador em Infraestrutura → Adicionar servidor → Chaves de acesso e rode-o na máquina: ele reinstala o agente já na versão nova. Nada é baixado no servidor enquanto isso, de propósito.",
        },
        {
          q: "Desligar a atualização para de coletar meus dados?",
          a: "Não. O agente continua enviando CPU, memória, disco, rede, containers e logs exatamente como antes. A única coisa que para é a troca de versão do programa.",
        },
        {
          q: "Qual a diferença entre desligar e fixar uma versão?",
          a: "Desligar congela cada servidor onde ele está, a frota fica misturada, cada um na versão que tinha. Fixar leva todo mundo para a MESMA versão e prende ali. Para investigar um defeito, fixar costuma ser melhor: você fica com uma frota uniforme e previsível.",
        },
      ],
    },
    audit: {
      title: "Auditoria",
      what: "Registro de tudo o que foi alterado no painel: quem fez, quando, em qual tela/recurso e o conteúdo que foi enviado. É gravado automaticamente em toda criação, edição e remoção, ninguém precisa (nem pode) ligar ou desligar. Só administradores enxergam esta tela.",
      how: [
        "Use os filtros para achar o que procura: por pessoa (\"Quem\"), por assunto (\"Onde\"), por tipo de ação (criou/alterou/removeu) ou por um dia específico.",
        "Clique numa linha para expandir e ver exatamente o conteúdo enviado naquela alteração.",
        "A coluna \"Resultado\" mostra se a ação foi concluída ou recusada, tentativas negadas (ex.: sem permissão) também ficam registradas.",
        "\"Origem\" é o endereço de onde partiu a ação, útil para investigar acesso indevido.",
      ],
      faq: [
        {
          q: "Aparece senha ou chave de acesso na trilha?",
          a: "Nunca. Senhas, chaves SSH, chaves de agente, tokens e cabeçalhos de webhook são substituídos por um marcador antes de gravar. A trilha mostra que o campo foi enviado, mas jamais o seu valor.",
        },
        {
          q: "Por que não vejo quem apenas consultou uma tela?",
          a: "A auditoria registra alterações, não navegação. Abrir um gráfico ou listar servidores não muda nada e não entra aqui, isso manteria a trilha poluída e esconderia o que importa.",
        },
        {
          q: "Posso apagar ou editar um registro?",
          a: "Não. A trilha é somente leitura, inclusive para administradores, é isso que a torna confiável. As entradas saem sozinhas apenas quando ficam mais velhas que o prazo de retenção (1 ano por padrão).",
        },
        {
          q: "Excluí um usuário. Perco o histórico do que ele fez?",
          a: "Não. O nome de quem agiu é gravado junto com a ação, então o registro continua completo mesmo depois de a conta ser removida.",
        },
      ],
    },
    help: {
      title: "Guia do Revoada",
      what: "Manual navegável dentro do próprio sistema: primeiros passos, como instalar o agente, como criar um alerta e um glossário dos termos usados no Revoada.",
      how: [
        "Use o sumário lateral para pular entre as seções.",
        "Digite na busca para filtrar os títulos e achar um assunto rápido.",
        "Consulte o glossário quando encontrar um termo que não conhece.",
        "Cada tela também tem um botão \"?\" que abre a ajuda específica dela.",
      ],
    },
    ia: {
      title: "Agentes de IA",
      what: "Custo, tokens, latência e erros das suas aplicações de IA (chatbots, automações com ferramentas, pipelines que chamam LLM), e o passo a passo de cada execução. Os dados vêm dos spans OpenTelemetry que essas aplicações já mandam. O Revoada só observa: não chama nenhum modelo nem guarda chave de provedor.",
      how: [
        "Visão geral: a frase do topo resume a janela; abaixo, o custo no tempo, quanto cada modelo e cada agente gastou e as ferramentas que mais falham.",
        "Execuções: filtre por agente, modelo, status ou custo mínimo e clique numa execução para ver o replay.",
        "Replay: cada passo (modelo, ferramenta, agente) com tokens, custo e duração. Use ← → para andar; repetições da mesma ferramenta aparecem como possível loop.",
        "Modelos e preços: o custo é tokens × preço vigente na data da chamada. Cadastre o preço dos modelos marcados \"sem preço\".",
      ],
      faq: [
        {
          q: "Por que o custo aparece como \"parcial\"?",
          a: "Porque parte das chamadas não entrou na soma: o modelo não tem preço cadastrado ou a biblioteca não informou os tokens. O valor real é maior. O Revoada mostra \"sem preço\" em vez de inventar zero.",
        },
        {
          q: "Por que não vejo a conversa?",
          a: "A gravação de prompts e respostas vem desligada (é dado pessoal). Quando ligada (REVOADA_GENAI_CONTEUDO no gateway), o conteúdo é redigido, guardado por 7 dias e só administradores e operadores leem, com cada leitura na auditoria.",
        },
      ],
    },
  },

  fields: {
    // --- Alertas / regras ---
    "alert.metric":
      "A métrica que a regra vai vigiar. Ex.: uso de CPU, memória livre ou latência. É o número que será comparado com o limite.",
    "alert.scope":
      "A quais servidores a regra se aplica. \"Todos os servidores\" a torna global (vale para qualquer servidor que reporte a métrica). \"Servidores específicos\" restringe a um ou vários servidores escolhidos, útil para uma regra que só interessa a um cliente ou ambiente. Combina com os filtros abaixo.",
    "alert.filters":
      "Restringe ainda mais por qualquer rótulo (ex.: ambiente \"produção\", disco \"/\"). Combina com a escolha de servidores acima. Sem filtros nem servidores, a regra vale para tudo que reporta essa métrica.",
    "alert.operator":
      "Como comparar a métrica com o limite: maior que, menor que, igual etc. Ex.: \"maior que\" 90 dispara quando o valor passa de 90.",
    "alert.threshold":
      "O valor que conta como violação quando ultrapassado. Ex.: 90 para CPU acima de 90%. Ajuste para não ser sensível demais nem de menos.",
    "alert.for":
      "Por quanto tempo a violação precisa persistir antes de disparar o alerta. Evita alarme falso por pico de 1 segundo. Ex.: 5 minutos só alerta se o valor ficar alto por 5 minutos contínuos. Deixar 0 NÃO significa \"dispara no primeiro instante\": mesmo com 0, o painel exige 2 avaliações consecutivas violando (cerca de 1 minuto) para abrir, e 2 consecutivas abaixo do limite para encerrar. É um piso de segurança que vale para todas as regras; o valor que você digitar aqui só tem efeito se for maior que ele.",
    "alert.severity":
      "A gravidade do alerta (ex.: aviso, atenção, crítica). Define a cor e a urgência com que aparece nas telas e nos canais de alerta. Ex.: crítica salta aos olhos no Mural e na TV; aviso é mais discreto.",
    "alert.channels":
      "Para quais canais este alerta será enviado quando disparar. Marque um ou mais; se não marcar nenhum, o alerta vai para TODOS os canais ativos. Canais desativados não recebem, mesmo se marcados.",
    "alert.enabled":
      "Liga ou desliga a regra sem apagá-la. Desligada, ela para de vigiar e de disparar. Útil durante manutenção planejada.",
    "alert.flapping":
      "Indica que o alerta fica ligando e desligando repetidamente em pouco tempo. O sistema marca isso para você não ser bombardeado de avisos: manda um aviso de \"oscilando\" e engole os seguintes. A supressão não é silenciosa, as mensagens dizem quantas transições foram suprimidas na última hora, e quando o vaivém termina de verdade o painel manda a resolução mesmo assim, para o incidente não acabar sem você saber. Costuma sinalizar limite mal calibrado ou instabilidade real.",
    "alert.preview":
      "Simula a regra contra o histórico e mostra quantas vezes ela teria disparado nos últimos dias. Use antes de salvar para saber se vai alertar de menos ou de mais. Ex.: \"teria disparado 42 vezes em 7 dias\" indica limite baixo demais.",

    // --- Adicionar servidor por SSH (Fase G) ---
    "provision.name":
      "Um nome para você reconhecer o servidor no Revoada (ex.: \"Banco de Produção\"). Não precisa ser o hostname técnico; serve para identificar o servidor nesta e nas próximas telas.",
    "provision.host":
      "O endereço para a central alcançar o servidor por SSH: um IP (ex.: 10.0.0.12) ou um nome que resolva na sua rede (ex.: app-prod-01). É por aqui que o agente é instalado.",
    "provision.port":
      "A porta do SSH no servidor. O padrão é 22; mude só se o seu SSH escuta em outra porta.",
    "provision.user":
      "O usuário usado para conectar e instalar o agente. Precisa poder executar a instalação (normalmente root ou um usuário com sudo). Ex.: root.",
    "provision.auth":
      "Como a central se autentica no servidor: por Senha (a senha desse usuário) ou por Chave SSH (a chave privada em formato PEM). A credencial é usada para instalar e fica guardada cifrada num cofre, para reprovisionar e atualizar o agente depois sem você redigitar. Nunca é exibida em claro nem devolvida pela API. Como é sensível, só administradores usam e a conexão com a central deve ser confiável (em produção a app é HTTPS).",

    // --- Canais de alerta ---
    "channel.type":
      "O meio pelo qual o aviso é enviado: e-mail, webhook, Telegram ou WhatsApp. Cada tipo pede campos diferentes. Ex.: escolha WhatsApp para receber no celular do time.",
    "channel.name":
      "Um nome para você identificar o canal. Ex.: \"E-mail Ops\" ou \"Telegram do time\". Não afeta a entrega, só a organização.",
    "channel.owner":
      "Vincula o canal a um usuário (canal pessoal). Deixando em \"Nenhum\", o canal é da regra: entra no envio de todos os alertas configurados. Vinculado a um usuário, o canal passa a receber SÓ os alertas dos servidores em que esse usuário tem \"Receber alerta\" marcado (na tela Usuários & Acessos), e deixa de entrar no envio geral das regras. É assim que cada pessoa recebe no WhatsApp apenas os alertas dos seus servidores.",
    "channel.email.to":
      "Os endereços de e-mail que recebem o aviso, separados por vírgula. Ex.: ops@empresa.com, plantao@empresa.com. Todos os endereços recebem cada notificação.",
    "channel.webhook.url":
      "O endereço https para onde o Revoada envia os dados do alerta em JSON. Use para integrar com outros sistemas (ex.: um bot ou uma automação). Precisa ser acessível pelo servidor do Revoada.",
    "channel.telegram.token":
      "O token do bot do Telegram, obtido com o @BotFather. É o que autoriza o Revoada a enviar mensagens por aquele bot. Sem ele, o envio falha.",
    "channel.telegram.chatId":
      "O identificador numérico do chat ou grupo que recebe as mensagens. Descubra-o falando com o bot e consultando as atualizações dele. Ex.: -1001234567890 para um grupo.",
    "channel.whatsapp.baseUrl":
      "O endereço da sua instância do wuzapi (ex.: https://revoada.exemplo.com.br/wuzapi). É por ele que o Revoada envia as mensagens. Precisa ser acessível pelo servidor do Revoada.",
    "channel.whatsapp.token":
      "O token da sessão criada no wuzapi, é ele que autoriza o envio e identifica qual número de WhatsApp envia. Fica guardado de forma segura e não é exibido depois de salvo.",
    "channel.whatsapp.to":
      "Quem recebe o aviso: números com DDI e DDD (ex.: 5511999999999) ou o ID de um grupo (…@g.us), separados por vírgula. Todos recebem cada notificação. Antes de enviar, o painel pergunta ao WhatsApp qual é o número real da pessoa, muita conta antiga está registrada sem o nono dígito, e mandar para o número \"certo\" não chegaria. Se o número não tiver WhatsApp, o envio falha dizendo isso, em vez de fingir que enviou.",
    "channel.whatsapp.lid":
      "O identificador interno que o WhatsApp usa para a pessoa (ex.: 123456789012345), com 14 a 16 dígitos. Preencha um por destinatário, na mesma ordem dos números. É o que faz o alerta chegar de verdade: o WhatsApp entrega a quem já conversou com o número do painel e descarta em silêncio o resto. Sem LID o painel ainda tenta pelo número, mas registra o envio como \"sem confirmação\", porque não tem como saber se chegou. Para obter o LID, peça à pessoa que envie uma mensagem para o número do painel.",
    "channel.test":
      "Envia uma mensagem de teste agora e mostra o resultado na hora. São três respostas possíveis: \"enviado\" (chegou ao destino), \"sem confirmação\" em amarelo (o provedor aceitou, mas ninguém garante a entrega, no WhatsApp, falta o LID) e a falha em vermelho, com o motivo e o que fazer. Use sempre após cadastrar.",
    // --- Websites / jornadas ---
    "check.url":
      "O endereço completo do site a ser testado, começando com http:// ou https://. Ex.: https://loja.empresa.com. É essa página que as sondas vão acessar.",
    "check.interval":
      "De quanto em quanto tempo o site é testado. 60s é um bom padrão. Intervalos muito curtos detectam quedas mais rápido, mas geram mais carga e ruído.",
    "check.group":
      "O grupo em que o site aparece na status page pública. Ex.: agrupar \"Loja\", \"Blog\" e \"API\" separadamente. Serve só para organizar a exibição.",
    "check.probes":
      "Os locais (sondas) de onde o site é testado. Com várias sondas, o Revoada exige que pelo menos 2 falhem para declarar DOWN, evitando falso positivo por rede de uma sonda. A central conta como sonda.",
    "check.expectStatus":
      "O código HTTP que significa \"tudo certo\" para este site. Normalmente 200. Se a resposta vier diferente, o check é considerado falho. Ex.: use 200 para uma página comum.",
    "check.timing":
      "Mostra o tempo de resposta dividido em fases DNS → TLS → TTFB. Ajuda a saber onde está a lentidão: achar o endereço, negociar a conexão segura ou o servidor responder.",
    "check.cert":
      "Acompanha quantos dias faltam para o certificado HTTPS expirar. Um selo avisa quando está perto do fim, para você renovar antes de o site ficar inacessível.",
    "journey.name":
      "Um nome para a jornada (fluxo de vários passos). Ex.: \"Login e checkout\". Serve para identificar o fluxo na lista e nos avisos.",
    "journey.interval":
      "De quanto em quanto tempo a jornada inteira é executada. Como testa vários passos, tende a ser mais pesada; intervalos maiores (ex.: 5 min) costumam ser suficientes.",
    "journey.steps":
      "A sequência ordenada de passos que simulam um usuário (abrir URL, preencher campo, clicar, esperar texto). A jornada só passa se todos os passos, na ordem, tiverem sucesso.",
    "journey.step.action":
      "O que este passo faz: abrir uma URL, enviar um formulário, ou verificar o status e o texto da resposta. Cada ação pede campos próprios. Ex.: \"esperar texto\" confirma que a página final mostra \"Pedido confirmado\".",

    // --- Dashboards / painéis ---
    "dashboard.title":
      "O nome do dashboard, exibido na lista e no topo da tela. Ex.: \"Visão da Produção\". Escolha algo que diga de relance para que serve.",
    "dashboard.folder":
      "A pasta onde o dashboard fica guardado, para organizar quando houver muitos. Ex.: \"Infra\" ou \"Negócio\". Não altera o conteúdo, só a arrumação.",
    "dashboard.uid":
      "O identificador único e permanente do dashboard, usado no endereço e por TVs/playlists. Não muda mesmo que você renomeie o título, então links continuam válidos.",
    "panel.type":
      "O formato do painel: série temporal (linha), número grande, tabela, medidor etc. Escolha conforme o dado. Ex.: use série temporal para acompanhar CPU ao longo do tempo.",
    "panel.title":
      "O título do painel dentro do dashboard. Ex.: \"CPU por host\". Ajuda quem olha a entender o que aquele gráfico mostra.",
    "panel.scope":
      "Define o que o painel mede. \"Servidor inteiro\" (padrão) mostra o recurso do servidor como um todo, opcionalmente restrito a um servidor no seletor; sem escolher servidor, agrega todos. \"Container específico\" foca num container Docker: ao escolher o container, o painel já assume uma métrica de container (CPU, memória, reinícios). Serviços descobertos como MySQL ou Nginx ainda não têm métricas próprias, elas dependem de exporters e chegam numa etapa futura, por isso não aparecem aqui como escopo. O escopo é só uma camada amigável: ele preenche os filtros e a métrica por você; os filtros avançados continuam disponíveis.",
    "panel.metric":
      "A métrica que o painel exibe. A lista vem do que já foi coletado. Ex.: escolha a métrica de uso de memória para plotar a RAM.",
    "panel.filters":
      "Restringe o painel a parte dos dados (ex.: host=web-01). Sem filtros, mostra todas as séries daquela métrica, o que pode poluir o gráfico.",
    "panel.agg":
      "Como combinar vários pontos num só valor: média, soma, máximo, mínimo. Ex.: máximo destaca picos; média suaviza. Muda bastante a leitura do gráfico.",
    "panel.size":
      "O tamanho do painel no dashboard: largura em colunas (1 a 4) e altura (P/M/G). Ex.: um número-chave cabe pequeno; um gráfico detalhado pede largura maior.",
    "dashboard.starterHost":
      "O host usado para gerar automaticamente uma \"Visão do Host\" pronta (CPU, memória, disco, rede). Escolha um host que já reporta para o dashboard vir com dados.",
    "dashboard.version":
      "O número da versão atual do dashboard. Toda alteração salva incrementa esse número e guarda um histórico, permitindo restaurar uma versão anterior sem perder nada.",

    // --- TV / playlists ---
    "tv.token":
      "O link secreto e somente-leitura para exibir numa TV sem login. Abra #/tv/{token} na tela. Se vazar, revogue o token e o acesso é cortado na hora.",
    "tv.playlist":
      "A playlist atribuída a este token; faz a TV alternar entre várias telas. Deixe em branco para a TV mostrar um único dashboard fixo.",
    "playlist.name":
      "Um nome para a playlist. Ex.: \"Telão NOC\". Serve para você reconhecê-la ao atribuir a um token de TV.",
    "playlist.items":
      "As telas que a playlist exibe em sequência (dashboards existentes e/ou o Mural de Saúde), na ordem escolhida. A TV passa de uma para a próxima automaticamente.",
    "playlist.itemDuration":
      "Quantos segundos cada item fica na tela antes de trocar. Ex.: 20s dá tempo de ler sem cansar. Muito curto atrapalha a leitura; muito longo demora a mostrar o resto.",

    // --- Explore / logs / traces ---
    "explore.metric":
      "A métrica que você quer investigar. A lista vem do que já foi coletado. Ex.: escolha a latência do serviço \"api\" para ver como ela variou.",
    "explore.agg":
      "Como combinar os vários pontos coletados dentro de cada intervalo do gráfico em um só número. As opções são exatamente as do seletor: média, máximo, mínimo, soma e último. Use máximo para caçar picos, média para ver o comportamento típico e último para saber onde a métrica está agora. A agregação escolhida aparece escrita no título e no rodapé do gráfico, para o pico nunca ser lido como valor típico.",
    "explore.groupBy":
      "Por qual rótulo separar o resultado em várias linhas. Ex.: agrupar por host mostra uma linha por servidor, em vez de tudo somado num só.",
    "explore.window":
      "O período de tempo analisado (ex.: última hora, últimas 24h). Janelas maiores dão contexto; menores dão detalhe. Ajuste conforme o que investiga.",
    "logs.query":
      "O texto ou expressão que filtra os logs. Ex.: buscar \"timeout\" mostra só linhas com essa palavra. Combine com os filtros de serviço e severidade para afunilar.",
    "logs.source":
      "De onde a linha de log veio no servidor. Além dos logs de aplicação, o agente coleta o servidor inteiro: \"Sistema (journald)\" (serviços do systemd), \"Containers (Docker)\", \"Syslog\", \"Kernel (dmesg)\" e \"Arquivo\" (arquivos de log avulsos). Escolha uma fonte para, por exemplo, ver só o que o kernel ou os containers registraram. \"Todas as fontes\" não filtra. Combina com serviço, servidor e severidade.",
    "logs.severity":
      "O nível mínimo de gravidade das linhas exibidas (informação, aviso, erro...). Ex.: filtrar por \"erro\" esconde o ruído e mostra só o que deu problema.",
    "logs.saveMetric":
      "Cria uma métrica que conta, ao longo do tempo, quantas linhas batem com a busca atual. Assim você pode gráficar e alertar sobre a frequência daquele log. Ex.: contar erros de pagamento por minuto.",
    "logs.liveTail":
      "Acompanha os logs em tempo real, com novas linhas aparecendo conforme chegam (como um \"tail -f\"). Um indicador mostra se a conexão ao vivo está ativa.",
    "containers.cpu":
      "O CPU de cada container está na MESMA escala do \"CPU em uso\" do servidor: 0 a 100% da máquina inteira, onde 100% = todos os núcleos saturados. Então dá para somar: se três containers marcam 12%, 8% e 5%, eles ocupam 25% da máquina, e esse total tem de caber dentro do CPU do servidor. Não divida por nada, o número já está pronto. (Até a versão 0.7 do agente esta métrica seguia a convenção do `docker stats`, em que 100% queria dizer UM núcleo e a soma podia passar de 100%; se você vir um valor acima de 100% num gráfico antigo, é dado de antes da correção.)",
    "traces.sampling":
      "O Revoada não guarda todos os traces: mantém os que têm erro ou passam de 1 segundo e cerca de 20% do restante (escolha determinística pelo id do trace). A decisão é tomada na primeira vez que o trace aparece e os spans que chegam depois a herdam. Quando um trace só fica interessante mais tarde, o que já foi descartado não volta: ele é guardado com o selo \"parcial\". Por isso você não vê todos os traces normais; é intencional, para economizar armazenamento.",
    "traces.partial":
      "O selo \"parcial\" diz que faltam spans naquele trace: ele chegou a ser descartado pela amostragem e só depois virou interessante, e o que já tinha sido jogado fora não pôde ser recuperado. Consequência: a operação exibida como raiz pode ser só o span mais antigo que sobrou, e a duração é um PISO (por isso aparece como \"≥ 900 ms\"), o tempo real é igual ou maior. Sirva-se dele para investigar; para medir latência ou comparar operações, escolha um trace sem o selo.",
    "traces.waterfall":
      "A visão em cascata de um trace: cada barra é uma etapa (span) e o comprimento é a duração. Mostra o que rodou em paralelo, o que esperou e onde o tempo foi gasto.",
    "traces.errorsFromLogs":
      "A aba \"Erros & stack traces\" não depende de instrumentação: os stack traces vêm dos LOGS. O agente costura um traceback multiline (várias linhas) num único registro de log e o classifica como erro; o Revoada então agrupa erros idênticos por assinatura (números, UUID e hex viram curingas) e conta quantas vezes cada um ocorreu. Cada grupo é expansível para ver o stack trace completo do exemplo. Filtre por servidor, serviço, fonte (journald/docker/syslog/kernel/arquivo) e janela de tempo.",
    "traces.spansVsErrors":
      "São duas visões complementares. \"Distribuído (spans)\" mostra traces distribuídos REAIS, o caminho de uma requisição por vários serviços, e exige que as aplicações estejam instrumentadas com OpenTelemetry (OTLP); sem isso, fica vazia. \"Erros & stack traces\" já funciona hoje, sem instrumentação, extraindo os stack traces dos logs. Use spans para entender latência e dependências entre serviços; use erros & stack traces para achar rapidamente o que está quebrando.",
  },

  concepts: {
    "tail-sampling": {
      title: "Tail sampling (amostragem de traces)",
      body: "Estratégia para não guardar 100% dos traces e ainda assim não perder o que importa. O Revoada mantém os traces com erro ou com duração acima de 1 segundo, e cerca de 20% dos demais, escolhidos de forma determinística pelo id do trace. A decisão é do trace inteiro: tomada na primeira vez que ele aparece e herdada pelos spans que chegarem depois. Se um trace só se revelar interessante mais tarde (o erro estava no último span a chegar), ele passa a ser guardado, mas os spans já descartados não voltam, e ele fica marcado como PARCIAL, com raiz possivelmente errada e duração que vale só como piso. Consequência: você vê os problemas e os lentos, e apenas parte dos traces normais, de propósito, para economizar armazenamento.",
    },
    "nome-amigavel": {
      title: "Nome amigável do servidor (apelido)",
      body: "Um rótulo legível que você dá a um host em Infraestrutura (ex.: \"Banco de Produção\") sem mudar o nome técnico, que continua sendo a chave interna. O apelido aparece nos seletores \"Servidor\" de Explore, Logs, Traces e Alertas e nas séries agrupadas por host, deixando as telas mais fáceis de ler num ambiente com vários servidores.",
    },
    sonda: {
      title: "Sonda",
      body: "Um ponto de observação de onde um site é testado. Cada sonda tenta acessar a URL e reporta se conseguiu e em quanto tempo. Com sondas em locais diferentes, dá para distinguir uma queda real do site de um problema de rede em um único ponto. A central do Revoada também conta como uma sonda.",
    },
    "consenso-de-sondas": {
      title: "Consenso de sondas",
      body: "Regra que decide o estado de um site combinando o resultado de várias sondas. Um site só é declarado DOWN quando pelo menos 2 sondas falham; se apenas 1 falha, ele fica DEGRADADO. Isso evita falso positivo por um problema de rede de uma única sonda. A central conta como sonda nessa contagem.",
    },
    "estados-de-site": {
      title: "Estados de um site",
      body: "Um site monitorado progride por quatro estados conforme piora: UP (tudo certo) → DEGRADADO (sinal de problema, ex.: 1 sonda falhando ou lentidão) → SUSPEITO (falhas se acumulando, retestadas a cada 30 s) → DOWN (fora do ar). Quantas falhas seguidas confirmam o DOWN depende do nível do site: crítico 2 (~30 s), padrão 3 (~1 min), básico 4 (~1,5 min). Enquanto está DOWN o painel retesta a cada 30 s, então o aviso de recuperação chega na hora e diz quanto tempo a queda durou de verdade. A gradação evita alarme por instabilidade de segundos e mostra a tendência antes da queda total.",
    },
    flapping: {
      title: "Flapping (oscilação)",
      body: "Quando um alerta ou site fica ligando e desligando repetidamente em pouco tempo, em vez de estabilizar. O Revoada detecta e marca isso para não bombardear o time com avisos a cada oscilação. Costuma indicar um limite mal calibrado ou uma instabilidade real que merece investigação, não só um susto passageiro.",
    },
    downsampling: {
      title: "Downsampling (redução de resolução)",
      body: "Para não guardar dados brutos para sempre, o Revoada reduz as métricas para resoluções de 1 minuto e de 1 hora. Dados recentes ficam em alta resolução (detalhe fino); dados mais antigos ficam resumidos por minuto e por hora. Consequência: gráficos de períodos longos são mais leves, ao custo de menos detalhe fino no passado distante.",
    },
    ttl: {
      title: "TTL (tempo de vida dos dados)",
      body: "O prazo após o qual os dados antigos são apagados automaticamente. No Revoada o TTL é em camadas: cada resolução (bruta, 1 minuto, 1 hora) tem seu próprio prazo, então o detalhe fino some antes do resumo. Consequência: você não acha logs ou métricas mais antigos que o TTL, planeje a retenção conforme sua necessidade.",
    },
    otlp: {
      title: "OTLP",
      body: "O protocolo padrão do OpenTelemetry para enviar métricas, logs e traces. É por ele que seus serviços e o agente entregam telemetria ao gateway do Revoada (por HTTP ou gRPC). Consequência: para ver traces e métricas de uma aplicação aqui, instrumente-a para exportar em OTLP para o endereço do gateway.",
    },
    uptime: {
      title: "Uptime (disponibilidade)",
      // NÃO é "porcentagem do tempo": o cálculo é count(ok)/count(*) sobre as
      // SONDAGENS do período (store.MakeUptimeStat). A diferença importa porque este
      // texto fica ao lado do número na status page pública: com sondagens faltando,
      // "porcentagem do tempo" convida a converter em minutos fora do ar uma medida
      // que não mediu aqueles minutos. Por isso a cobertura anda junto do percentual.
      body: "O percentual de sondagens bem-sucedidas num período (dia, mês, 90 dias), não é a porcentagem do tempo: conta-se quantas verificações deram certo entre as que realmente aconteceram. Por isso o número vem sempre com a COBERTURA ao lado, que diz qual fatia das sondagens esperadas de fato ocorreu; com cobertura baixa, o percentual descreve poucas amostras e não deve ser convertido em minutos fora do ar. É a principal medida de confiabilidade exibida nos checks e na status page pública.",
    },
    exemplar: {
      title: "Exemplar",
      body: "Um ponto de uma métrica que carrega o id de um trace representativo daquele instante. Serve de ponte: ao ver um pico no gráfico, você pula direto para um trace real que exemplifica aquele momento. Consequência: investigar \"por que ficou lento às 14h\" vira um clique da métrica para o trace, sem caçar na mão.",
    },
    "onboarding-ssh": {
      title: "Onboarding por SSH (Adicionar servidor)",
      body: "Forma automática de colocar um servidor sob monitoramento sem colar comandos por SSH na mão. Em \"Adicionar servidor\" (só admin) você informa host, usuário e credencial; a central conecta por SSH, gera a chave de ingestão, instala o agente e sobe o serviço, mostrando cada passo ao vivo. É a alternativa ao caminho manual (\"Chaves de agente\"), que continua disponível. Depois, \"Atualizar agente\" reexecuta a instalação usando a credencial guardada no cofre, sem redigitar.",
    },
    "cofre-cifrado": {
      title: "Cofre de credenciais (cifrado)",
      body: "Onde o Revoada guarda, de forma cifrada, a senha ou chave SSH usada para provisionar um servidor. A credencial serve para instalar o agente e depois reprovisionar/atualizar sem você redigitar. Consequência: ela nunca é exibida em claro nem devolvida pela API, só administradores usam o recurso e, em produção, a conexão com a central é por HTTPS. Se preferir não guardar credenciais, use o caminho manual de instalação por chave de agente.",
    },
    "permissao-efetiva": {
      title: "Permissão efetiva",
      body: "O que um usuário realmente pode fazer em cada servidor, calculado pela UNIÃO das permissões diretas (marcadas na matriz dele) com as herdadas dos grupos de servidores que ele participa. Se qualquer origem concede Ver/Editar/Receber alerta, o usuário tem aquilo. Editar e Receber alerta sempre incluem Ver. Administradores ignoram tudo isto: veem e podem tudo.",
    },
    "canal-pessoal": {
      title: "Canal pessoal (notificação)",
      body: "Um canal de notificação (geralmente WhatsApp) vinculado a um usuário específico, na tela Canais de alerta. Diferente dos canais da regra (que recebem todos os alertas configurados), um canal pessoal só recebe os alertas dos servidores em que aquele usuário tem \"Receber alerta\" marcado. Assim cada pessoa é avisada apenas do que lhe diz respeito, sem virar destino de todos os alertas.",
    },
    "expurgo-cirurgico": {
      title: "Expurgo cirúrgico de logs",
      body: "Exclusão dirigida de linhas de log, na aba \"Armazenamento de logs\" de um servidor (só admin). Recorta por três eixos combináveis: CONTEÚDO (um trecho literal da linha), JANELA (de quando até quando) e ALVO (o servidor aberto ou as \"linhas sem servidor\", que chegaram sem rótulo de host, tipicamente por OTLP de uma aplicação). Serve para cumprir um pedido de exclusão de dado pessoal sem apagar o histórico inteiro do servidor. É irreversível, o banco reescreve os arquivos e não há lixeira, e o painel mostra o número de linhas atingidas antes de você confirmar. Alcance limitado, de propósito: ele toca apenas a tabela de logs, e a lista \"não alcançado\" exibida ao final diz onde o mesmo dado ainda pode viver (métricas por minuto, 90 dias; rollup por hora, 730 dias; eventos; registro de notificações enviadas; backup diário sem expiração). Exclusão que não chega a essas cópias é adiamento, não exclusão.",
    },
    "severidade-nao-classificada": {
      title: "Severidade \"não classificado\" (UNKNOWN)",
      body: "Linha de log em que o agente não conseguiu ler um nível. Nem todo log declara severidade: journald, syslog e a saída crua de um container muitas vezes chegam só como texto, e o painel não inventa um nível que o programa não escreveu. A linha continua inteira e pesquisável. Consequência importante na tela de Logs: os pisos INFO+, WARN+ e ERROR+ são um corte por número de severidade, e linha sem nível vale zero, ou seja, qualquer piso ESCONDE todo o \"não classificado\", que na maioria dos servidores é o grosso do log. Para ver só essas linhas use a opção \"não classificado\"; para ver tudo, \"qualquer nível\".",
    },
    "chave-mascarada": {
      title: "Chave de acesso mascarada e \"Revelar\"",
      body: "Na lista de chaves de acesso, a chave de ingestão aparece encurtada (\"dev-…46\"): ela identifica a linha, mas não autentica. O motivo é concreto: a listagem completa entregava, numa única consulta, a credencial de TODOS os servidores para qualquer sessão de administrador comprometida, e com essa credencial se escreve métrica, log e heartbeat em nome de qualquer host. O texto completo só existe em dois momentos: na criação da chave (uma vez, para copiar) e num clique explícito em \"Revelar\", uma chave por vez, que fica registrado na trilha de auditoria com quem revelou, de qual servidor e quando. Para reinstalar um servidor, prefira gerar o instalador, ele já leva a chave dentro e não exige revelar nada.",
    },
    "amarracao-servidor-chave": {
      title: "Amarração servidor → chave (atualização dos agentes)",
      body: "O freio da auto-atualização é gravado na CHAVE de ingestão, não no nome do servidor: quando o agente pergunta ao painel se há versão nova, ele se identifica pela chave, e é só isso que o painel reconhece naquele instante. Como a tela de Atualização dos agentes lista servidores, ela precisa resolver servidor → chave. Faz isso por duas vias: a EXATA, quando o próprio agente informa o nome da máquina ao consultar; e a APROXIMADA, quando o nome digitado ao criar a chave bate com o hostname técnico ou com o nome amigável do servidor. Se nenhuma resolve, ou se duas chaves disputam a mesma máquina, o painel não age e diz isso na tela, um freio aplicado na chave errada pararia a atualização de outro servidor.",
    },
    "container-caido": {
      title: "Container caído (alerta)",
      body: "Regra pronta que avisa quando um container do Docker deixa de rodar. Baseia-se na métrica container.running, que vale 1 quando o container está no ar e 0 quando parou; agregada pelo mínimo na janela e comparada com \"< 1\", ela dispara assim que qualquer amostra zera. Vem com severidade crítica. O atalho \"+ Container caído\" na aba Regras de Alertas pré-preenche essa regra para você revisar (escopo, filtros) e salvar.",
    },
  },
};
