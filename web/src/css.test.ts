// O minificador de CSS do Vite 8 (Lightning CSS) junta `prop` e `-webkit-prop` da
// mesma regra e, quando o prefixado vem DEPOIS, fica só ele: o Chrome perde a
// propriedade. Foi assim que a barra do topo perdeu o desfoque. Escreva o prefixado
// antes, ou só a forma padrão (o build acrescenta o prefixo).
//
// Lido do disco: o Vitest entrega CSS vazio mesmo com `?raw`. O especificador é
// montado em tempo de execução para o tsc não exigir os tipos do Node.
type Fs = {
  readdirSync(dir: string, opts: { recursive: true }): string[];
  readFileSync(arquivo: string, cod: "utf8"): string;
};
const fs = (await import(/* @vite-ignore */ "node:" + "fs")) as Fs;
// O Vitest roda a partir de web/ (no jsdom, import.meta.url não é um caminho de arquivo).
const raiz = (globalThis as unknown as { process: { cwd(): string } }).process.cwd() + "/src/";
const folhas: Record<string, string> = Object.fromEntries(
  fs
    .readdirSync(raiz, { recursive: true })
    .filter((f) => f.endsWith(".css"))
    .map((f) => [f, fs.readFileSync(raiz + f, "utf8")]),
);

function prefixadoDepoisDoPadrao(css: string): string[] {
  const achados: string[] = [];
  for (const bloco of css.split("}")) {
    const vistas = new Set<string>();
    for (const decl of bloco.split(/[;{]/)) {
      const prop = decl.split(":")[0]?.trim() ?? "";
      if (prop.startsWith("-webkit-") && vistas.has(prop.slice("-webkit-".length))) achados.push(prop);
      vistas.add(prop);
    }
  }
  return achados;
}

describe("CSS", () => {
  it("acha as folhas de estilo", () => {
    expect(Object.keys(folhas).length).toBeGreaterThan(5);
  });

  it("propriedade -webkit- nunca vem depois da forma padrão na mesma regra", () => {
    const erros = Object.entries(folhas).flatMap(([arquivo, css]) =>
      prefixadoDepoisDoPadrao(css).map((prop) => `${arquivo}: ${prop}`),
    );
    expect(erros).toEqual([]);
  });

  it("o detector pega a ordem invertida", () => {
    expect(prefixadoDepoisDoPadrao(".a { backdrop-filter: blur(1px); -webkit-backdrop-filter: blur(1px); }")).toEqual([
      "-webkit-backdrop-filter",
    ]);
    expect(prefixadoDepoisDoPadrao(".a { -webkit-mask-image: none; mask-image: none; }")).toEqual([]);
  });
});
