package logs

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestPurgeWhereRecorte: o WHERE carrega os TRÊS eixos do recorte — servidor,
// janela de dois lados e conteúdo. É o que permite tirar UMA linha vazada sem levar
// junto o mês inteiro do servidor.
func TestPurgeWhereRecorte(t *testing.T) {
	f := purgeFilter{
		Host:     "srv-01",
		From:     time.Unix(1_700_000_000, 0),
		To:       time.Unix(1_700_003_600, 0),
		BodyLike: "cpf 123",
	}
	w := purgeWhere(f)
	for _, want := range []string{
		"tenant_id='default'",
		"labels['host']='srv-01'",
		"ts >= toDateTime(1700000000)",
		"ts < toDateTime(1700003600)",
		"body ILIKE '%cpf 123%'",
	} {
		if !strings.Contains(w, want) {
			t.Errorf("purgeWhere não contém %q; got=%s", want, w)
		}
	}
}

// TestPurgeWhereSemHost: host vazio + no_host vira o predicado das linhas SEM
// rótulo — 17 de 45 linhas de `logs` em dev (37,8%) eram impurgáveis até o TTL.
func TestPurgeWhereSemHost(t *testing.T) {
	w := purgeWhere(purgeFilter{NoHost: true, To: time.Unix(1, 0)})
	if !strings.Contains(w, "labels['host']=''") {
		t.Errorf("no_host deveria casar linhas sem rótulo; got=%s", w)
	}
	if strings.Contains(w, "labels['host']=''''") {
		t.Errorf("predicado malformado: %s", w)
	}
}

// TestPurgeWhereEscaping: aspas no host não escapam da string (anti-injeção).
func TestPurgeWhereEscaping(t *testing.T) {
	w := purgeWhere(purgeFilter{Host: "a'b", To: time.Unix(1, 0)})
	if !strings.Contains(w, `labels['host']='a\'b'`) {
		t.Errorf("host mal escapado: %s", w)
	}
}

// TestEscapeLike: `%` e `_` do segredo procurado viram literais. Sem isso, um token
// com `%` casaria linhas que o operador nunca viu no preview.
func TestEscapeLike(t *testing.T) {
	// Dupla camada: escapeLike marca o curinga (`\%`) e quote escapa a barra para o
	// literal SQL (`\\%`). O ClickHouse desfaz uma camada ao ler a string, sobrando
	// `\%` — curinga neutralizado. Conferido no banco de dev:
	//   'tok%en_1' ILIKE '%tok\\%en\\_1%'  → 1
	//   'tokXenY1' ILIKE '%tok\\%en\\_1%'  → 0
	w := purgeWhere(purgeFilter{Host: "h", To: time.Unix(1, 0), BodyLike: "tok%en_1"})
	if !strings.Contains(w, `body ILIKE '%tok\\%en\\_1%'`) {
		t.Errorf("curingas não neutralizados: %s", w)
	}
}

// TestValidatePurge cobre as guardas do recorte.
func TestValidatePurge(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name    string
		f       purgeFilter
		wantErr bool
	}{
		{"host+corte", purgeFilter{Host: "srv-01", To: now.Add(-time.Hour)}, false},
		{"agora-vale", purgeFilter{Host: "srv-01", To: now}, false},
		{"janela-dois-lados", purgeFilter{Host: "s", From: now.Add(-2 * time.Hour), To: now.Add(-time.Hour)}, false},
		{"sem-host-explicito", purgeFilter{NoHost: true, To: now.Add(-time.Hour)}, false},
		{"host-vazio-sem-flag", purgeFilter{Host: "  ", To: now.Add(-time.Hour)}, true},
		{"no_host-com-host", purgeFilter{NoHost: true, Host: "srv-01", To: now}, true},
		{"corte-zero", purgeFilter{Host: "srv-01"}, true},
		{"corte-futuro", purgeFilter{Host: "srv-01", To: now.Add(time.Hour)}, true},
		{"from-depois-de-to", purgeFilter{Host: "s", From: now.Add(-time.Hour), To: now.Add(-2 * time.Hour)}, true},
		{"trecho-gigante", purgeFilter{Host: "s", To: now, BodyLike: strings.Repeat("x", maxBodyLike+1)}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if err := validatePurge(c.f); (err != nil) != c.wantErr {
				t.Errorf("validatePurge(%+v) err=%v, wantErr=%v", c.f, err, c.wantErr)
			}
		})
	}
}

// TestPreviewCampoDesconhecido: `"q":"senha"` (campo que não existe) devolvia 200 com
// o MESMO número de sem filtro — o operador achava que tinha restringido por conteúdo.
// Agora é 400, ANTES de qualquer consulta. Mesma regra do POST do expurgo, porque o
// preview passou a usar exatamente o mesmo corpo estrito.
func TestPreviewCampoDesconhecido(t *testing.T) {
	var consultou bool
	ch := fakeCH(t, func(string) string { consultou = true; return `{"c":"999"}` })
	h := NewHandler(ch, nil, "", nil)
	rec := httptest.NewRecorder()
	h.PurgePreview(rec, httptest.NewRequest("POST", "/api/logs/purge/preview",
		strings.NewReader(`{"host":"web01","before":"2026-01-01T00:00:00Z","q":"senha"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400, obtido %d (%s)", rec.Code, rec.Body.String())
	}
	if consultou {
		t.Error("não deveria consultar o ClickHouse com campo desconhecido")
	}
	if !strings.Contains(rec.Body.String(), "não reconhecido") {
		t.Errorf("mensagem pouco clara: %s", rec.Body.String())
	}
}

// TestPreviewNaoAceitaQueryString é a prova do vazamento que motivou a mudança: o
// `body_like` É o dado vazado (CPF, token), e em GET ele ia na URL — que o nginx
// registra em `$request`, que o agente coleta do stdout do container e devolve para
// dentro da MESMA tabela `logs` que o operador está limpando. Com o preview em POST,
// um recorte mandado na query string não é mais lido: chega vazio e é recusado.
func TestPreviewNaoAceitaQueryString(t *testing.T) {
	var consultou bool
	ch := fakeCH(t, func(string) string { consultou = true; return `{"c":"999"}` })
	h := NewHandler(ch, nil, "", nil)
	rec := httptest.NewRecorder()
	h.PurgePreview(rec, httptest.NewRequest("POST",
		"/api/logs/purge/preview?host=web01&before=2026-01-01T00:00:00Z&body_like=12345678900",
		strings.NewReader(`{}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("recorte na URL não pode valer; esperado 400, obtido %d (%s)", rec.Code, rec.Body.String())
	}
	if consultou {
		t.Error("não deveria consultar o ClickHouse com recorte só na URL")
	}
}

// TestPurgeCampoDesconhecido: mesma regra no corpo do POST.
func TestPurgeCampoDesconhecido(t *testing.T) {
	ch := fakeCH(t, func(string) string { return "" })
	h := NewHandler(ch, nil, "", nil)
	rec := httptest.NewRecorder()
	h.Purge(rec, httptest.NewRequest("POST", "/api/logs/purge",
		strings.NewReader(`{"host":"srv-01","before":"2026-01-01T00:00:00Z","q":"senha"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400, obtido %d (%s)", rec.Code, rec.Body.String())
	}
}

// TestPreviewEDeleteMesmoWhere é a invariante do recorte: o número mostrado é o
// número apagado porque preview e DELETE usam a MESMA cláusula.
func TestPreviewEDeleteMesmoWhere(t *testing.T) {
	const body = `{"host":"srv-01","from":"2026-01-01T00:00:00Z","to":"2026-01-02T00:00:00Z","body_like":"segredo"}`
	whereDe := func(sql, marca string) string {
		i := strings.Index(sql, marca)
		if i < 0 {
			return ""
		}
		return strings.TrimSuffix(strings.TrimSpace(sql[i+len(marca):]), noLogSuffix)
	}

	var wherePreview string
	chPrev := fakeCH(t, func(q string) string {
		if strings.Contains(q, "count()") {
			wherePreview = whereDe(q, "FROM logs WHERE ")
		}
		return `{"c":"3"}`
	})
	hp := NewHandler(chPrev, nil, "", nil)
	recP := httptest.NewRecorder()
	hp.PurgePreview(recP, httptest.NewRequest("POST", "/api/logs/purge/preview",
		strings.NewReader(body)))
	if recP.Code != http.StatusOK {
		t.Fatalf("preview: esperado 200, obtido %d (%s)", recP.Code, recP.Body.String())
	}

	var whereDelete string
	chDel := fakeCH(t, func(q string) string {
		switch {
		case strings.Contains(q, "is_done=0"):
			return `{"c":"0"}` // nenhuma limpeza concorrente
		case strings.Contains(q, "system.mutations"):
			return `{"mutation_id":"m1","is_done":"1","parts_to_do":"0","latest_fail_reason":""}`
		case strings.HasPrefix(q, "ALTER TABLE logs DELETE WHERE "):
			whereDelete = whereDe(q, "ALTER TABLE logs DELETE WHERE ")
			return ""
		case strings.Contains(q, "count()"):
			return `{"c":"3"}`
		}
		return ""
	})
	hd := NewHandler(chDel, nil, "", nil)
	recD := httptest.NewRecorder()
	hd.Purge(recD, httptest.NewRequest("POST", "/api/logs/purge", strings.NewReader(body)))
	if recD.Code != http.StatusOK {
		t.Fatalf("purge: esperado 200, obtido %d (%s)", recD.Code, recD.Body.String())
	}
	if wherePreview == "" || whereDelete == "" {
		t.Fatalf("não capturei as cláusulas (preview=%q delete=%q)", wherePreview, whereDelete)
	}
	if wherePreview != whereDelete {
		t.Errorf("preview e DELETE divergiram:\npreview=%s\ndelete =%s", wherePreview, whereDelete)
	}

	var got struct {
		Deleted   int64 `json:"deleted_rows"`
		Done      bool  `json:"done"`
		NotPurged []struct {
			Store string `json:"store"`
		} `json:"not_purged"`
		QueryLogPurged bool `json:"query_log_purged"`
	}
	_ = json.Unmarshal(recD.Body.Bytes(), &got)
	if got.Deleted != 3 || !got.Done {
		t.Errorf("resposta: deleted=%d done=%v", got.Deleted, got.Done)
	}
	// Cobertura declarada: a resposta diz o que NÃO foi alcançado.
	if len(got.NotPurged) == 0 {
		t.Fatal("a resposta precisa declarar o que não foi alcançado")
	}
	decl := recD.Body.String()
	for _, want := range []string{"metrics_1h", "730 dias", "backup", "notification_log"} {
		if !strings.Contains(decl, want) {
			t.Errorf("declaração de cobertura sem %q: %s", want, decl)
		}
	}
	if !got.QueryLogPurged {
		t.Error("com body_like, o expurgo deve limpar também system.query_log")
	}
}

// TestPurgeLimpaQueryLog: buscar o vazamento deixava uma cópia legível dele em
// system.query_log. O expurgo por conteúdo passa o rodo lá — e o próprio comando
// vai com log_queries=0, para não recriar a cópia que está apagando.
func TestPurgeLimpaQueryLog(t *testing.T) {
	var stmts []string
	ch := fakeCH(t, func(q string) string {
		stmts = append(stmts, q)
		switch {
		case strings.Contains(q, "is_done=0"):
			return `{"c":"0"}`
		case strings.Contains(q, "system.mutations"):
			return `{"mutation_id":"m1","is_done":"1","parts_to_do":"0","latest_fail_reason":""}`
		case strings.Contains(q, "count()"):
			return `{"c":"1"}`
		}
		return ""
	})
	h := NewHandler(ch, nil, "", nil)
	rec := httptest.NewRecorder()
	h.Purge(rec, httptest.NewRequest("POST", "/api/logs/purge",
		strings.NewReader(`{"host":"srv-01","to":"2026-01-02T00:00:00Z","body_like":"CANARIO"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("esperado 200, obtido %d (%s)", rec.Code, rec.Body.String())
	}
	var achou bool
	for _, s := range stmts {
		if strings.HasPrefix(s, "ALTER TABLE system.query_log DELETE") {
			achou = true
			if !strings.HasSuffix(s, noLogSuffix) {
				t.Errorf("o comando que limpa o query_log não pode ser registrado nele: %s", s)
			}
			if !strings.Contains(s, "query ILIKE '%CANARIO%'") {
				t.Errorf("filtro do query_log errado: %s", s)
			}
		}
	}
	if !achou {
		t.Error("o expurgo por conteúdo deve limpar system.query_log")
	}
	// Toda consulta que carrega o trecho procurado sai com log_queries=0.
	for _, s := range stmts {
		if strings.Contains(s, "CANARIO") && !strings.HasSuffix(s, noLogSuffix) {
			t.Errorf("consulta com o trecho sem log_queries=0: %s", s)
		}
	}
}

// TestPurgeConflict: mutation ativa na tabela logs → 409, sem disparar DELETE.
func TestPurgeConflict(t *testing.T) {
	var sawDelete bool
	ch := fakeCH(t, func(q string) string {
		if strings.Contains(q, "system.mutations") && strings.Contains(q, "is_done=0") {
			return `{"c":"1"}` // já há uma limpeza rodando
		}
		if strings.Contains(q, "ALTER TABLE logs DELETE") {
			sawDelete = true
		}
		return ""
	})
	h := NewHandler(ch, nil, "", nil)
	rec := httptest.NewRecorder()
	h.Purge(rec, httptest.NewRequest("POST", "/api/logs/purge",
		strings.NewReader(`{"host":"srv-01","before":"2026-01-01T00:00:00Z"}`)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("esperado 409, obtido %d (%s)", rec.Code, rec.Body.String())
	}
	if sawDelete {
		t.Error("DELETE não deveria ser disparado quando há mutation ativa")
	}
}

// TestPurgeHostVazioSemFlag: host vazio NÃO significa "todos" nem "sem rótulo" —
// exige o pedido explícito, com a confirmação reforçada da tela.
func TestPurgeHostVazioSemFlag(t *testing.T) {
	ch := fakeCH(t, func(string) string { return "" })
	h := NewHandler(ch, nil, "", nil)
	rec := httptest.NewRecorder()
	h.Purge(rec, httptest.NewRequest("POST", "/api/logs/purge",
		strings.NewReader(`{"host":"","before":"2026-01-01T00:00:00Z"}`)))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("esperado 400, obtido %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "sem servidor") {
		t.Errorf("a mensagem precisa ensinar o caminho certo: %s", rec.Body.String())
	}
}

// TestPurgePreviewSemHost: as linhas sem rótulo agora TÊM rota.
func TestPurgePreviewSemHost(t *testing.T) {
	var where string
	ch := fakeCH(t, func(q string) string {
		if i := strings.Index(q, "FROM logs WHERE "); i >= 0 {
			where = q[i+len("FROM logs WHERE "):]
		}
		return `{"c":"17"}`
	})
	h := NewHandler(ch, nil, "", nil)
	rec := httptest.NewRecorder()
	h.PurgePreview(rec, httptest.NewRequest("POST", "/api/logs/purge/preview",
		strings.NewReader(`{"no_host":true,"before":"2026-01-01T00:00:00Z"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("esperado 200, obtido %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(where, "labels['host']=''") {
		t.Errorf("preview deveria mirar as linhas sem rótulo: %s", where)
	}
	var got struct {
		Rows  int64  `json:"rows"`
		Scope string `json:"scope"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Rows != 17 {
		t.Errorf("rows esperado 17, obtido %d", got.Rows)
	}
	if !strings.Contains(got.Scope, "SEM rótulo") {
		t.Errorf("o escopo precisa ser explícito na tela: %q", got.Scope)
	}
}

// TestPurgePreview: o recorte histórico (host + before) continua valendo, agora no corpo.
func TestPurgePreview(t *testing.T) {
	ch := fakeCH(t, func(q string) string {
		if strings.Contains(q, "count()") && strings.Contains(q, "FROM logs") {
			return `{"c":"42"}`
		}
		return ""
	})
	h := NewHandler(ch, nil, "", nil)
	rec := httptest.NewRecorder()
	h.PurgePreview(rec, httptest.NewRequest("POST", "/api/logs/purge/preview",
		strings.NewReader(`{"host":"srv-01","before":"2026-01-01T00:00:00Z"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("esperado 200, obtido %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Rows int64 `json:"rows"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Rows != 42 {
		t.Errorf("rows esperado 42, obtido %d", got.Rows)
	}
}

// TestPurgeStatusFailReason: mutation travada por erro expõe o motivo (não fica
// eterna no spinner).
func TestPurgeStatusFailReason(t *testing.T) {
	ch := fakeCH(t, func(q string) string {
		if strings.Contains(q, "system.mutations") {
			return `{"mutation_id":"m1","is_done":"0","parts_to_do":"2","latest_fail_reason":"disk full"}`
		}
		return ""
	})
	h := NewHandler(ch, nil, "", nil)
	rec := httptest.NewRecorder()
	h.PurgeStatus(rec, httptest.NewRequest("GET", "/api/logs/purge/status?id=m1", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("esperado 200, obtido %d", rec.Code)
	}
	var got struct {
		Done       bool   `json:"done"`
		FailReason string `json:"fail_reason"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Done {
		t.Error("mutation com erro não está done")
	}
	if got.FailReason != "disk full" {
		t.Errorf("fail_reason esperado 'disk full', obtido %q", got.FailReason)
	}
}

// TestBuscaNaoEntraNoQueryLog: a busca que carrega o termo do usuário sai com
// log_queries=0 — caçar o vazamento não pode criar uma segunda cópia dele.
func TestBuscaNaoEntraNoQueryLog(t *testing.T) {
	var sqls []string
	ch := fakeCH(t, func(q string) string { sqls = append(sqls, q); return "" })
	h := NewHandler(ch, nil, "", nil)

	rec := httptest.NewRecorder()
	h.Search(rec, httptest.NewRequest("GET", "/api/logs/search?q=CANARIO", nil))
	rec = httptest.NewRecorder()
	h.Histogram(rec, httptest.NewRequest("GET", "/api/logs/histogram?q=CANARIO", nil))
	rec = httptest.NewRecorder()
	h.Patterns(rec, httptest.NewRequest("GET", "/api/logs/patterns?q=CANARIO", nil))
	if len(sqls) != 3 {
		t.Fatalf("esperava 3 consultas, obtive %d", len(sqls))
	}
	for _, s := range sqls {
		if !strings.Contains(s, "CANARIO") {
			t.Fatalf("consulta sem o termo? %s", s)
		}
		if !strings.HasSuffix(strings.TrimSpace(s), strings.TrimSpace(noLogSuffix)) {
			t.Errorf("consulta com termo do usuário sem log_queries=0: %s", s)
		}
	}

	// Sem termo livre, a consulta continua registrada (o operador precisa enxergar
	// custo e erro no banco).
	sqls = nil
	rec = httptest.NewRecorder()
	h.Search(rec, httptest.NewRequest("GET", "/api/logs/search?host=srv-01", nil))
	if strings.Contains(sqls[0], "log_queries") {
		t.Errorf("consulta sem termo não precisa sair do query_log: %s", sqls[0])
	}
}

// TestGuardaIgnoraMutationTravada é o travamento medido no código: a guarda
// anti-concorrência contava TUDO com is_done=0, e uma mutation que falhou de forma
// PERMANENTE (part corrompida, disco cheio) fica em is_done=0 para sempre. A partir
// dela, todo expurgo respondia 409 "já existe uma limpeza em andamento"
// indefinidamente — o direito ao esquecimento travado no dia em que é exigido, e sem
// nenhum caminho pela interface para destravar.
func TestGuardaIgnoraMutationTravada(t *testing.T) {
	var guarda string
	ch := fakeCH(t, func(q string) string {
		switch {
		case strings.Contains(q, "is_done=0") && strings.Contains(q, "count()"):
			guarda = q
			return `{"c":"0"}` // a travada não é contada
		case strings.Contains(q, "system.mutations"):
			return `{"mutation_id":"m9","is_done":"1","parts_to_do":"0","latest_fail_reason":""}`
		case strings.Contains(q, "count()"):
			return `{"c":"5"}`
		}
		return ""
	})
	h := NewHandler(ch, nil, "", nil)
	rec := httptest.NewRecorder()
	h.Purge(rec, httptest.NewRequest("POST", "/api/logs/purge",
		strings.NewReader(`{"host":"srv-01","before":"2026-01-01T00:00:00Z"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("esperado 200, obtido %d (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(guarda, "latest_fail_reason=''") {
		t.Errorf("a guarda precisa ignorar as mutations com falha permanente: %s", guarda)
	}
}

// TestPurgeCancelMataSoAMutationPedida: o KILL fica preso à tabela `logs` e ao id
// informado — nunca uma varredura que possa derrubar mutation de outra tabela.
func TestPurgeCancelMataSoAMutationPedida(t *testing.T) {
	var kill string
	ch := fakeCH(t, func(q string) string {
		if strings.HasPrefix(q, "KILL MUTATION") {
			kill = q
		}
		return ""
	})
	h := NewHandler(ch, nil, "", nil)
	rec := httptest.NewRecorder()
	h.PurgeCancel(rec, httptest.NewRequest("POST", "/api/logs/purge/cancel",
		strings.NewReader(`{"mutation_id":"mutation_42.txt"}`)))
	if rec.Code != http.StatusOK {
		t.Fatalf("esperado 200, obtido %d (%s)", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"table='logs'", "mutation_id='mutation_42.txt'", "currentDatabase()"} {
		if !strings.Contains(kill, want) {
			t.Errorf("KILL sem %q: %s", want, kill)
		}
	}
	// Sem id não há KILL nenhum: um cancelamento "de tudo" seria destrutivo.
	rec2 := httptest.NewRecorder()
	h.PurgeCancel(rec2, httptest.NewRequest("POST", "/api/logs/purge/cancel", strings.NewReader(`{}`)))
	if rec2.Code != http.StatusBadRequest {
		t.Errorf("sem mutation_id esperado 400, obtido %d", rec2.Code)
	}
}

// TestPurgeStatusListaTravadas: a tela precisa DESCOBRIR a limpeza travada para
// oferecer o cancelamento. E o `command` da mutation não pode aparecer na resposta —
// ele cita o `body_like`, ou seja, o próprio dado que o expurgo tenta apagar.
func TestPurgeStatusListaTravadas(t *testing.T) {
	ch := fakeCH(t, func(q string) string {
		if strings.Contains(q, "latest_fail_reason!=''") {
			return `{"mutation_id":"m7","command":"DELETE WHERE body ILIKE '%12345678900%'",` +
				`"parts_to_do":"3","latest_fail_reason":"Cannot read all data","create_time":"2026-08-01 10:00:00"}`
		}
		return ""
	})
	h := NewHandler(ch, nil, "", nil)
	rec := httptest.NewRecorder()
	h.PurgeStatus(rec, httptest.NewRequest("GET", "/api/logs/purge/status", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("esperado 200, obtido %d (%s)", rec.Code, rec.Body.String())
	}
	corpo := rec.Body.String()
	if !strings.Contains(corpo, "m7") || !strings.Contains(corpo, "Cannot read all data") {
		t.Errorf("a travada precisa aparecer com o motivo: %s", corpo)
	}
	if strings.Contains(corpo, "12345678900") {
		t.Errorf("o comando cita o dado vazado e não pode ir para a tela: %s", corpo)
	}
}
