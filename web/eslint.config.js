import js from "@eslint/js";
import tseslint from "typescript-eslint";
import reactHooks from "eslint-plugin-react-hooks";

export default tseslint.config(
  { ignores: ["dist", "node_modules"] },
  js.configs.recommended,
  ...tseslint.configs.recommended,
  {
    files: ["**/*.{ts,tsx}"],
    plugins: { "react-hooks": reactHooks },
    rules: {
      "@typescript-eslint/no-explicit-any": "error",
      // Hooks: regra quebrada é erro; dependência faltando é aviso, e o gate roda com
      // --max-warnings=0, então também barra. Exceções ficam anotadas com o motivo.
      "react-hooks/rules-of-hooks": "error",
      "react-hooks/exhaustive-deps": "warn",
    },
  },
  {
    // Regra do CLAUDE.md: nenhuma cor hex hardcoded fora de tokens.css.
    files: ["src/**/*.{ts,tsx}"],
    rules: {
      "no-restricted-syntax": [
        "error",
        {
          selector: "Literal[value=/#[0-9a-fA-F]{3,8}\\b/]",
          message: "Cor hardcoded proibida — use os tokens de src/styles/tokens.css.",
        },
      ],
    },
  },
);
