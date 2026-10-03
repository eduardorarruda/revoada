package copia

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
	_ "time/tzdata" // o container de teste pode não ter /usr/share/zoneinfo

	"github.com/eduardorarruda/revoada/core/migracao/plano"
)

// Achado na validação ponta a ponta (ERP com 30 mil clientes): com o agente no fuso
// de São Paulo, 65 datas chegaram ao PostgreSQL UM DIA ANTES — justamente os dias
// em que o horário de verão começava à meia-noite (2005-10-16 → 2005-10-15). O
// driver montava a DATA como meia-noite no fuso local, que não existe nesses dias, e
// o Go normalizava para as 23h do dia anterior. Nada no Revoada percebia: a
// verificação compara o que foi convertido com o que foi gravado, e o erro nascia
// antes, na leitura. DATE e TIMESTAMP do Firebird não têm fuso: são relógio de
// parede, e assim têm de chegar.
func TestDataEHoraNoHorarioDeVeraoChegamIntactas(t *testing.T) {
	sp, err := time.LoadLocation("America/Sao_Paulo")
	if err != nil {
		t.Fatal(err)
	}
	antes := time.Local
	time.Local = sp // o agente roda no servidor do cliente, no fuso dele
	t.Cleanup(func() { time.Local = antes })

	esp, _, dbd := prepararCom(t, extras{
		apagaFB: []string{"RV_VERAO"},
		fb: []string{
			`CREATE TABLE RV_VERAO (ID INTEGER NOT NULL PRIMARY KEY, DIA DATE, MOMENTO TIMESTAMP, INSTANTE TIMESTAMP)`,
			// meia-noite inexistente (início do horário de verão)
			`INSERT INTO RV_VERAO VALUES (1, '2005-10-16', '2005-10-16 00:30:00.1234', NULL)`,
			`INSERT INTO RV_VERAO VALUES (2, '2018-11-04', '2018-11-04 00:00:00', NULL)`,
			// hora que acontece duas vezes (fim do último horário de verão)
			`INSERT INTO RV_VERAO VALUES (3, '2019-02-16', '2019-02-16 23:30:00', NULL)`,
			// dia comum: o instante (timestamptz) é o relógio lido no fuso do agente
			`INSERT INTO RV_VERAO VALUES (4, '2024-06-01', '2024-06-01 12:00:00', '2024-06-01 12:00:00')`,
		},
		pg: []string{`CREATE TABLE %[1]s.rv_verao (id integer PRIMARY KEY, dia date, momento timestamp, instante timestamptz)`},
	})
	s := esp.Destino.Opcoes["schema"]
	bruto, _ := json.Marshal(esp)
	r := novoRelator()
	out, err := Executar(context.Background(), bruto, r)
	if err != nil {
		t.Fatalf("executar: %v (eventos %v)", err, r.eventos)
	}
	if rt := tabelaExecutada(t, out.(*plano.ResumoExecucao), "rv_verao"); !rt.ConteudoOK {
		t.Fatalf("rv_verao sem conteúdo conferido: %+v", rt)
	}
	quer := map[int]string{
		1: "2005-10-16|2005-10-16 00:30:00.1234|~",
		2: "2018-11-04|2018-11-04 00:00:00|~",
		3: "2019-02-16|2019-02-16 23:30:00|~",
		4: "2024-06-01|2024-06-01 12:00:00|2024-06-01 12:00:00",
	}
	rows, err := dbd.Query(`SELECT id, dia::text || '|' || momento::text || '|' ||
		COALESCE((instante AT TIME ZONE 'America/Sao_Paulo')::text, '~') FROM ` + s + `.rv_verao ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var v string
		if err := rows.Scan(&id, &v); err != nil {
			t.Fatal(err)
		}
		if v != quer[id] {
			t.Errorf("id %d: o PostgreSQL guardou %q, a origem tem %q", id, v, quer[id])
		}
	}
	for _, m := range r.eventos {
		if strings.Contains(m, "descarta_hora") {
			t.Errorf("DATE do Firebird não tem hora para descartar: %q", m)
		}
	}
	// e a dupla conferência pelo texto dos bancos (que pegaria o erro antigo) bate,
	// com as datas do início do horário de verão e o timestamptz no fuso do agente
	ver := verificar(t, bruto)
	vt := tabelaVerificada(t, ver, "rv_verao")
	if !ver.TextoOK || vt.Texto == nil || !vt.Texto.OK || !contem(vt.Texto.Colunas, "dia", "momento", "instante") {
		b, _ := json.MarshalIndent(vt.Texto, "", " ")
		t.Fatalf("conferência pelo texto de rv_verao: %s", b)
	}
}
