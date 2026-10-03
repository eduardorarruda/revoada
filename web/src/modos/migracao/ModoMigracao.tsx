// Modo Migração (ARQUITETURA §9): um "modo separado" do painel — layout, navegação e rotas
// próprios, com a saída para o painel sempre à vista.
import { ArrowLeft, Database, FolderKanban } from "lucide-react";
import { BrandLogo } from "../../components/BrandLogo";
import { Conexoes } from "./Conexoes";
import { Projetos } from "./Projetos";
import { Editor } from "./Editor";
import "./migracao.css";

export function ModoMigracao({ rota }: { rota: string }) {
  const projeto = rota.startsWith("/migracao/projetos/") ? decodeURIComponent(rota.slice("/migracao/projetos/".length)) : "";
  const aba = rota.startsWith("/migracao/conexoes") ? "conexoes" : "projetos";

  return (
    <div className="mm">
      <header className="mm__topo">
        <a className="mm__voltar" href="#/" title="Voltar ao painel">
          <ArrowLeft size={18} aria-hidden={true} />
          <span>Painel</span>
        </a>
        <div className="mm__marca">
          <BrandLogo variant="mark" />
          <div>
            <strong>Modo Migração</strong>
            <span>Firebird 2.x → 5 · Firebird → PostgreSQL</span>
          </div>
        </div>
        <nav className="mm__abas" aria-label="Seções do modo migração">
          <a className={`mm__aba${aba === "projetos" ? " is-ativa" : ""}`} href="#/migracao">
            <FolderKanban size={16} aria-hidden={true} /> Projetos
          </a>
          <a className={`mm__aba${aba === "conexoes" ? " is-ativa" : ""}`} href="#/migracao/conexoes">
            <Database size={16} aria-hidden={true} /> Conexões
          </a>
        </nav>
      </header>
      <main className="mm__conteudo">
        {projeto ? <Editor projetoId={projeto} /> : aba === "conexoes" ? <Conexoes /> : <Projetos />}
      </main>
    </div>
  );
}
