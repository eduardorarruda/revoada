// ErrorBoundary — uma tela com defeito não apaga o painel inteiro.
//
// Antes dela, uma exceção num único gráfico (ex.: o canvas ainda sem tamanho)
// derrubava a árvore React toda: menu, topo e conteúdo sumiam, e sobrava uma tela
// preta sem nenhuma explicação. Agora o defeito fica contido na área da tela, o
// menu continua funcionando e a mensagem diz o que houve e o que fazer.
//
// Precisa ser classe: o React 19 ainda não tem equivalente em hook.
import { Component, createRef, type ErrorInfo, type ReactNode } from "react";
import { TriangleAlert } from "lucide-react";

// Depois de um deploy, uma aba aberta pede um pedaço de JS que não existe mais
// (o nome muda a cada build). O React.lazy guarda a falha para sempre: "tentar de
// novo" repetiria o erro — só recarregar a página resolve.
function ehVersaoNova(erro: Error): boolean {
  return /dynamically imported module|Importing a module script failed|Failed to fetch|ChunkLoadError/i.test(erro.message);
}

interface State {
  erro: Error | null;
}

export class ErrorBoundary extends Component<{ children: ReactNode }, State> {
  state: State = { erro: null };
  // O elemento que tinha o foco sumiu com a tela; o foco vai para o título do erro,
  // senão cairia no <body> e o leitor de tela ficaria sem saber o que houve.
  private titulo = createRef<HTMLHeadingElement>();

  componentDidUpdate(_: unknown, antes: State) {
    if (!antes.erro && this.state.erro) this.titulo.current?.focus();
  }

  static getDerivedStateFromError(erro: Error): State {
    return { erro };
  }

  componentDidCatch(erro: Error, info: ErrorInfo) {
    // Fica no console para quem for depurar; a tela mostra a versão legível.
    console.error("Tela com erro:", erro, info.componentStack);
  }

  render() {
    const { erro } = this.state;
    if (!erro) return this.props.children;
    return (
      <div className="page">
        <div className="tela-erro" role="alert">
          <span className="tela-erro__icone" aria-hidden="true">
            <TriangleAlert size={24} />
          </span>
          <h2 className="tela-erro__titulo" ref={this.titulo} tabIndex={-1}>
            {ehVersaoNova(erro) ? "O painel foi atualizado" : "Esta tela encontrou um erro"}
          </h2>
          {ehVersaoNova(erro) ? (
            <>
              <p className="tela-erro__texto">
                Saiu uma versão nova do painel enquanto esta aba estava aberta. Recarregue a página para usar a versão
                atual, nada do que está salvo se perde.
              </p>
              <div className="row">
                <button type="button" className="btn btn--primary" onClick={() => window.location.reload()}>
                  Recarregar a página
                </button>
              </div>
            </>
          ) : (
            <>
              <p className="tela-erro__texto">
                O resto do painel continua funcionando, use o menu normalmente. Se o erro voltar ao tentar de novo,
                envie a mensagem abaixo para quem cuida do painel.
              </p>
              <code className="tela-erro__detalhe">{erro.message}</code>
              <div className="row">
                <button type="button" className="btn btn--primary" onClick={() => this.setState({ erro: null })}>
                  Tentar de novo
                </button>
                <a className="btn btn--ghost" href="#/">
                  Ir para o Início
                </a>
              </div>
            </>
          )}
        </div>
      </div>
    );
  }
}
