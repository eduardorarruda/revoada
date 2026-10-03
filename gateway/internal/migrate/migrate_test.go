package migrate

import "testing"

func TestSplitStatements(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want int
	}{
		{"vazio", "", 0},
		{"uma instrução", "CREATE TABLE x (a Int)", 1},
		{"duas instruções", "CREATE TABLE x (a Int);\nCREATE TABLE y (b Int)", 2},
		{"comentário com ponto-e-vírgula não quebra", "-- nota (7 dias); resto\nCREATE TABLE x (a Int)", 1},
		{"linhas em branco e comentários ignorados", "\n-- c1\n\nSELECT 1;\n-- c2\n", 1},
		{"ponto-e-vírgula final não gera instrução vazia", "SELECT 1;", 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := len(splitStatements(c.in))
			if got != c.want {
				t.Fatalf("splitStatements(%q) = %d instruções, quer %d", c.in, got, c.want)
			}
		})
	}
}

func TestSplitStatementsPreservaComentarioInterno(t *testing.T) {
	// Garante que o corpo real da instrução sobrevive mesmo com comentário antes.
	stmts := splitStatements("-- comentário\nCREATE TABLE metrics (ts DateTime)")
	if len(stmts) != 1 {
		t.Fatalf("esperava 1 instrução, veio %d: %v", len(stmts), stmts)
	}
	if stmts[0] != "CREATE TABLE metrics (ts DateTime)" {
		t.Fatalf("instrução inesperada: %q", stmts[0])
	}
}
