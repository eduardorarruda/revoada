// Serviços descobertos: o que o agente manda cru ("php-fpm" / "8.1, 8.2") vira o
// que a pessoa lê ("PHP-FPM" / "PHP 8.1 e 8.2"). Tudo puro, sem tela.
import { describe, expect, it } from "vitest";
import { agruparServicosPorHost, descreverServico, juntarPtBR, listarVersoesPHP } from "./servicos";

describe("descreverServico", () => {
  it("PHP-FPM mostra as versões em português", () => {
    const s = descreverServico({ kind: "php-fpm", detail: "8.1, 8.2, 8.3, 8.4" });
    expect(s.nome).toBe("PHP-FPM");
    expect(s.detalhe).toBe("PHP 8.1, 8.2, 8.3 e 8.4");
    expect(s.categoria).toBe("app");
  });

  it("PHP-FPM sem versão no caminho diz que não identificou", () => {
    const s = descreverServico({ kind: "php-fpm", detail: "php-fpm" });
    expect(s.detalhe).toBe("versão não identificada");
  });

  it("Apache é reconhecido pelos dois nomes de processo", () => {
    expect(descreverServico({ kind: "apache", detail: "httpd" })).toMatchObject({
      nome: "Apache",
      detalhe: "processo httpd",
      categoria: "web",
    });
    expect(descreverServico({ kind: "apache", detail: "apache2" }).detalhe).toBe("processo apache2");
  });

  it("MySQL distingue MariaDB do MySQL pelo processo", () => {
    expect(descreverServico({ kind: "mysql", detail: "mariadbd" }).nome).toBe("MariaDB");
    expect(descreverServico({ kind: "mysql", detail: "mysqld" }).nome).toBe("MySQL");
    expect(descreverServico({ kind: "mysql", detail: "" }).nome).toBe("MySQL");
  });

  it("Docker conta os containers e guarda a lista", () => {
    const s = descreverServico({ kind: "docker", detail: "api,worker,db" });
    expect(s.nome).toBe("Docker");
    expect(s.detalhe).toBe("3 containers");
    expect(s.itens).toEqual(["api", "worker", "db"]);
    expect(s.categoria).toBe("containers");
    expect(descreverServico({ kind: "docker", detail: "so-um" }).detalhe).toBe("1 container");
    expect(descreverServico({ kind: "docker", detail: "" })).toMatchObject({ detalhe: "nenhum container", itens: [] });
  });

  it("serviço desconhecido não quebra: usa o próprio kind", () => {
    const s = descreverServico({ kind: "kafka", detail: "kafka" });
    expect(s.nome).toBe("kafka");
    expect(s.categoria).toBe("outro");
    expect(s.descricao.length).toBeGreaterThan(0);
  });

  it("toda descrição explica o serviço (princípio explicativo)", () => {
    for (const kind of ["apache", "php-fpm", "nginx", "mysql", "postgres", "redis", "mongodb", "clickhouse", "docker"]) {
      expect(descreverServico({ kind, detail: "" }).descricao.length).toBeGreaterThan(20);
    }
  });
});

describe("listarVersoesPHP / juntarPtBR", () => {
  it("separa e ordena as versões", () => {
    expect(listarVersoesPHP("8.4, 8.1,8.2")).toEqual(["8.1", "8.2", "8.4"]);
    expect(listarVersoesPHP("7.4, 8.10, 8.2")).toEqual(["7.4", "8.2", "8.10"]);
    expect(listarVersoesPHP("php-fpm")).toEqual([]);
    expect(listarVersoesPHP("")).toEqual([]);
  });
  it("junta com vírgula e 'e'", () => {
    expect(juntarPtBR([])).toBe("");
    expect(juntarPtBR(["8.2"])).toBe("8.2");
    expect(juntarPtBR(["8.1", "8.2"])).toBe("8.1 e 8.2");
    expect(juntarPtBR(["8.1", "8.2", "8.3"])).toBe("8.1, 8.2 e 8.3");
  });
});

describe("agruparServicosPorHost", () => {
  const base = { source: "process" };
  const services = [
    { ...base, hostname: "whm", kind: "mysql", detail: "mariadbd", discovered_at: "2026-09-25T13:00:00Z" },
    { ...base, hostname: "whm", kind: "php-fpm", detail: "8.2", discovered_at: "2026-09-25T13:05:00Z" },
    { ...base, hostname: "whm", kind: "apache", detail: "httpd", discovered_at: "2026-09-25T13:05:00Z" },
    { ...base, hostname: "crm", kind: "docker", detail: "a,b", source: "docker", discovered_at: "2026-09-25T12:00:00Z" },
  ];

  it("um grupo por servidor, em ordem alfabética, com a última descoberta", () => {
    const grupos = agruparServicosPorHost(services);
    expect(grupos.map((g) => g.hostname)).toEqual(["crm", "whm"]);
    expect(grupos[1].vistoEm).toBe("2026-09-25T13:05:00Z");
  });

  it("dentro do servidor, web antes de app, app antes de banco", () => {
    const whm = agruparServicosPorHost(services).find((g) => g.hostname === "whm");
    expect(whm?.servicos.map((s) => s.nome)).toEqual(["Apache", "PHP-FPM", "MariaDB"]);
  });

  it("lista vazia dá zero grupos", () => {
    expect(agruparServicosPorHost([])).toEqual([]);
  });
});
