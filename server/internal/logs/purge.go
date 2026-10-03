package logs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/audit"
)

// Expurgo cirúrgico de logs (LGPD).
//
// O que existia antes: um único recorte possível — "todo o host, antes de tal data".
// Medido: para remover UMA linha vazada no meio de três, o produto destruía DUAS; e
// um vazamento no começo da janela de 30 dias (o TTL real de `logs`) custava o mês
// inteiro daquele servidor. Aqui o recorte passa a ter três eixos:
//
//	servidor (ou "linhas SEM rótulo de host") × janela de DOIS lados × conteúdo.
//
// Duas invariantes que não podem ser afrouxadas:
//  1. preview e DELETE usam a MESMA função de WHERE (purgeWhere) — é o que garante
//     que o número mostrado antes é o número apagado depois;
//  2. o recorte é sempre explícito: nada de "campo vazio = tudo". Host vazio só vale
//     como alvo quando o pedido diz `no_host`, e a janela exige um fim.

// clockSkew tolera relógios ligeiramente adiantados ao validar o fim da janela (o
// corte pode ser "agora" no caso de apagar tudo; rejeitamos só o futuro claro).
const clockSkew = 5 * time.Minute

// maxBodyLike limita o padrão de conteúdo. Um literal gigante no WHERE vira uma
// consulta cara e uma linha de log gigante — 512 bytes cobrem qualquer segredo
// (token, CPF, chave) sem abrir essa porta.
const maxBodyLike = 512

// purgeFilter é o recorte de UM expurgo. Todos os campos entram no mesmo WHERE.
type purgeFilter struct {
	Host     string    // servidor alvo (labels['host'])
	NoHost   bool      // alvo = linhas SEM rótulo de host (labels['host']='')
	From     time.Time // início da janela (opcional; zero = desde sempre)
	To       time.Time // fim da janela (obrigatório; exclusivo)
	BodyLike string    // trecho do corpo da linha (substring literal, case-insensitive)
}

// escapeLike neutraliza os curingas do ILIKE. Sem isto, um segredo que contenha
// `%` ou `_` (comum em tokens e em URLs codificadas) casaria muito mais do que o
// operador pediu — e o expurgo apagaria linhas que ele nunca viu no preview.
func escapeLike(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, "%", `\%`)
	s = strings.ReplaceAll(s, "_", `\_`)
	return s
}

// bodyLikePred devolve o predicado de conteúdo para uma coluna de texto.
func bodyLikePred(col, pattern string) string {
	return col + " ILIKE " + quote("%"+escapeLike(pattern)+"%")
}

// purgeWhere monta a cláusula WHERE do expurgo/preview. FONTE ÚNICA: o preview
// conta com ela e o DELETE apaga com ela.
func purgeWhere(f purgeFilter) string {
	w := []string{"tenant_id='default'"}
	if f.NoHost {
		// Linhas sem rótulo de host. Em dev eram 17 de 45 em `logs` (37,8%) e 25 de 81
		// em `events` (30,9%) — inclusive o que chega por OTLP de coletor de terceiro,
		// que é justamente por onde um segredo entra. Antes, impurgáveis até o TTL.
		w = append(w, "labels['host']=''")
	} else {
		w = append(w, "labels['host']="+quote(f.Host))
	}
	if !f.From.IsZero() {
		w = append(w, fmt.Sprintf("ts >= toDateTime(%d)", f.From.Unix()))
	}
	w = append(w, fmt.Sprintf("ts < toDateTime(%d)", f.To.Unix()))
	if f.BodyLike != "" {
		w = append(w, bodyLikePred("body", f.BodyLike))
	}
	return strings.Join(w, " AND ")
}

// validatePurge aplica as guardas. Devolve erro legível (pt-BR) para virar 400.
func validatePurge(f purgeFilter) error {
	if f.NoHost {
		if strings.TrimSpace(f.Host) != "" {
			return fmt.Errorf("com \"linhas sem servidor\" o campo host precisa ficar vazio")
		}
	} else if strings.TrimSpace(f.Host) == "" {
		// Recusa explícita: host vazio NÃO significa "todos". Quem quer as linhas sem
		// rótulo pede por nome (no_host), e a tela pede confirmação reforçada.
		return fmt.Errorf("host é obrigatório (para apagar as linhas SEM servidor, marque \"linhas sem servidor\")")
	}
	if f.To.IsZero() {
		return fmt.Errorf("a data de corte (fim da janela) é obrigatória")
	}
	if f.To.After(time.Now().Add(clockSkew)) {
		return fmt.Errorf("a data de corte não pode estar no futuro")
	}
	if !f.From.IsZero() && !f.From.Before(f.To) {
		return fmt.Errorf("o início da janela precisa ser anterior ao fim")
	}
	if len(f.BodyLike) > maxBodyLike {
		return fmt.Errorf("o trecho de conteúdo é longo demais (máx. %d caracteres)", maxBodyLike)
	}
	return nil
}

// O preview do expurgo é POST, e não GET, porque o `body_like` É o dado vazado.
//
// Na query string, o trecho procurado (um CPF, um token, uma senha que entrou no log)
// virava rastro em todo lugar por onde a URL passa: o nginx de produção loga
// `$request` inteiro em stdout, o agente coleta o stdout do container e devolve o
// trecho para dentro de `logs` — a MESMA tabela que o operador está tentando limpar.
// Some-se o histórico do navegador e o log do proxy. Testado em dev: o trecho saía
// intacto no log de acesso, e o redator do agente não mascara `body_like`.
//
// Caçar o vazamento recriava o vazamento. Em POST o recorte vai no CORPO, que ninguém
// no caminho registra; a location do expurgo ainda ganhou `access_log off` no nginx
// (deploy/prod/nginx.conf) como segunda camada, para o próprio caminho não deixar
// rastro nem quando alguém voltar a mandar parâmetro na URL.

// purgeBody é o corpo do POST /api/logs/purge E do POST /api/logs/purge/preview.
// Campo desconhecido é recusado: um filtro digitado errado que o servidor ignora em
// silêncio devolve 200 e apaga muito mais do que o operador pediu.
type purgeBody struct {
	Host     string `json:"host"`
	NoHost   bool   `json:"no_host"`
	Before   string `json:"before"`
	From     string `json:"from"`
	To       string `json:"to"`
	BodyLike string `json:"body_like"`
}

func (b purgeBody) filter() purgeFilter {
	return purgeFilter{
		Host:     b.Host,
		NoHost:   b.NoHost,
		From:     parseTime(b.From, time.Time{}),
		To:       parseTime(b.To, parseTime(b.Before, time.Time{})),
		BodyLike: b.BodyLike,
	}
}

// noLogSuffix impede que a própria consulta de expurgo vire uma nova cópia do
// segredo dentro de `system.query_log` (retido 3 dias, fora do alcance de qualquer
// expurgo do produto). Provado em dev: uma busca com um canário deixava o termo
// legível em 2 linhas de system.query_log 4 s depois; com este SETTINGS, zero.
const noLogSuffix = " SETTINGS log_queries=0"

// countPurge conta as linhas que casam com o recorte (o número do preview e o
// deleted_rows do expurgo — sempre a mesma cláusula).
func (h *Handler) countPurge(ctx context.Context, f purgeFilter) (int64, error) {
	sql := "SELECT count() AS c FROM logs WHERE " + purgeWhere(f)
	if f.BodyLike != "" {
		sql += noLogSuffix
	}
	rows, err := h.ch.QueryJSON(ctx, sql)
	if err != nil {
		return 0, err
	}
	if len(rows) > 0 {
		return asInt64(rows[0]["c"]), nil
	}
	return 0, nil
}

// notReached declara o que o expurgo NÃO alcança. Exclusão que não alcança o
// backup é adiamento, não exclusão — e é aí que o procedimento deixa de cumprir o
// pedido de LGPD. A resposta diz isso em voz alta, sempre; o operador precisa saber
// o que ainda falta fazer à mão.
type notReachedItem struct {
	Store     string `json:"store"`     // onde o dado ainda pode estar
	Retention string `json:"retention"` // por quanto tempo
	Note      string `json:"note"`      // o que fazer / por que não foi alcançado
}

// fingerprint identifica o trecho procurado SEM guardá-lo. A trilha precisa poder
// dizer "estes dois expurgos usaram o mesmo padrão" sem virar mais uma cópia do
// segredo (audit_log não tem poda).
func fingerprint(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:6])
}

func notReached(bodyLike bool, queryLogPurged bool) []notReachedItem {
	items := []notReachedItem{
		{Store: "metrics_1m (métricas por minuto)", Retention: "90 dias",
			Note: "o expurgo de logs não toca métricas; use a exclusão completa do servidor."},
		{Store: "metrics_1h (métricas por hora)", Retention: "730 dias",
			Note: "rollup de 2 anos: é a cópia mais longeva do produto."},
		{Store: "events (eventos/anotações)", Retention: "90 dias",
			Note: "títulos e corpos de evento não são apagados por este expurgo."},
		{Store: "notification_log (Postgres)", Retention: "120 dias (REVOADA_RETENTION_DAYS)",
			Note: "o texto enviado ao WhatsApp/e-mail pode repetir o trecho vazado."},
		{Store: "backup diário no MinIO (ClickHouse + pg_dump)", Retention: "sem expiração automática configurada",
			Note: "cópia SEM cifra; o expurgo não a alcança, precisa ser tratada fora do painel."},
	}
	if bodyLike {
		// Conferido em dev: system.mutations guarda o COMANDO do expurgo, e o comando
		// cita o trecho procurado ("DELETE WHERE … body ILIKE '%…%'"). Não há como
		// apagar essa entrada por SQL — ela sai sozinha quando a tabela passa do limite
		// de mutations concluídas guardadas (finished_mutations_to_keep, 100 por tabela).
		items = append(items, notReachedItem{
			Store: "system.mutations do ClickHouse", Retention: "até sair pelo limite de mutations guardadas (100 por tabela)",
			Note: "o comando do expurgo cita o trecho procurado; não é apagável por SQL."})
		if !queryLogPurged {
			items = append(items, notReachedItem{
				Store: "system.query_log do ClickHouse", Retention: "3 dias",
				Note: "não foi possível limpar as consultas que citam o trecho; verifique a permissão de ALTER."})
		}
	}
	return items
}

// purgeQueryLog apaga de system.query_log as consultas que citam o trecho
// procurado. Caçar o vazamento criava uma segunda cópia dele: o ClickHouse grava a
// consulta inteira, com o termo legível. As buscas novas já não são registradas
// (log_queries=0), mas as antigas ficaram — este é o rodo. Best-effort: em
// instalação onde o usuário do painel não tem ALTER, devolve o erro e a resposta
// declara o item como NÃO alcançado.
func (h *Handler) purgeQueryLog(ctx context.Context, pattern string) error {
	// O próprio comando carrega o segredo → também não pode ser registrado.
	stmt := "ALTER TABLE system.query_log DELETE WHERE " +
		bodyLikePred("query", pattern) + noLogSuffix
	return h.ch.Exec(ctx, stmt)
}

// PurgePreview conta quantas linhas o recorte vai apagar — o número mostrado ANTES
// de confirmar (e reportado como deleted_rows ao final). POST com o MESMO corpo
// estrito do expurgo: preview e DELETE compartilham recorte, validação e cláusula
// WHERE, que é o que garante que o número mostrado antes é o número apagado depois.
func (h *Handler) PurgePreview(w http.ResponseWriter, r *http.Request) {
	var body purgeBody
	if err := decodeStrict(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f := body.filter()
	if err := validatePurge(f); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	count, err := h.countPurge(r.Context(), f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"rows":       count,
		"scope":      describePurge(f),
		"not_purged": notReached(f.BodyLike != "", false),
	})
}

// describePurge devolve o recorte em português, para a tela repetir ao operador
// exatamente o que vai sair (e para a trilha guardar a mesma frase).
func describePurge(f purgeFilter) string {
	var b strings.Builder
	if f.NoHost {
		b.WriteString("linhas SEM rótulo de servidor")
	} else {
		b.WriteString("servidor " + f.Host)
	}
	if !f.From.IsZero() {
		b.WriteString(", de " + f.From.UTC().Format(time.RFC3339))
	}
	b.WriteString(", até " + f.To.UTC().Format(time.RFC3339))
	if f.BodyLike != "" {
		b.WriteString(", só linhas cujo conteúdo contém o trecho informado")
	}
	return b.String()
}

// activeMutations conta mutations REALMENTE em andamento na tabela logs (guarda
// anti-concorrência).
//
// `latest_fail_reason=”` no filtro é a correção. Antes a guarda contava tudo com
// is_done=0, e uma mutation que falhou de forma PERMANENTE (part corrompida, disco
// cheio, coluna que sumiu) fica em is_done=0 para sempre: a partir dela, todo expurgo
// passava a responder 409 "já existe uma limpeza em andamento" indefinidamente. O
// direito ao esquecimento travado exatamente no dia em que é exigido, e sem nenhum
// caminho pela interface para destravar.
//
// Uma mutation com motivo de falha preenchido não está progredindo — não há nada com
// que concorrer. Ela deixa de bloquear, e o operador ainda ganha um botão para
// removê-la de vez (PurgeCancel).
func (h *Handler) activeMutations(ctx context.Context) (int64, error) {
	rows, err := h.ch.QueryJSON(ctx, `SELECT count() AS c FROM system.mutations
		WHERE database=currentDatabase() AND table='logs' AND is_done=0 AND latest_fail_reason=''`)
	if err != nil {
		return 0, err
	}
	if len(rows) > 0 {
		return asInt64(rows[0]["c"]), nil
	}
	return 0, nil
}

// StuckMutations lista as mutations travadas por falha permanente na tabela logs —
// o que a tela precisa mostrar para oferecer o "cancelar limpeza travada".
func (h *Handler) StuckMutations(ctx context.Context) ([]map[string]any, error) {
	rows, err := h.ch.QueryJSON(ctx, `SELECT mutation_id, command, parts_to_do,
		latest_fail_reason, toString(create_time) AS create_time
		FROM system.mutations
		WHERE database=currentDatabase() AND table='logs' AND is_done=0 AND latest_fail_reason!=''
		ORDER BY create_time DESC LIMIT 20`)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"mutation_id": asString(r["mutation_id"]),
			// `command` NÃO vai na resposta: ele cita o `body_like`, ou seja, o próprio
			// dado que o expurgo tenta apagar. Mostrar o comando na tela seria mais uma
			// cópia do segredo, agora no navegador de quem abrir a tela.
			"parts_remaining": asInt64(r["parts_to_do"]),
			"fail_reason":     asString(r["latest_fail_reason"]),
			"created_at":      asString(r["create_time"]),
		})
	}
	return out, nil
}

// PurgeCancel mata uma mutation travada (KILL MUTATION). Admin-only.
//
// Sem isto, a única saída de um 409 permanente era abrir um cliente do ClickHouse na
// mão, em produção, com o operador que precisa cumprir um pedido de LGPD esperando.
func (h *Handler) PurgeCancel(w http.ResponseWriter, r *http.Request) {
	var body struct {
		MutationID string `json:"mutation_id"`
	}
	if err := decodeStrict(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	id := strings.TrimSpace(body.MutationID)
	if id == "" {
		http.Error(w, "mutation_id é obrigatório", http.StatusBadRequest)
		return
	}
	// KILL preso à tabela `logs` e ao id informado: nunca uma varredura que possa
	// derrubar mutation de outra tabela por engano.
	stmt := "KILL MUTATION WHERE database=currentDatabase() AND table='logs' AND mutation_id=" +
		quote(id) + " SYNC" + noLogSuffix
	if err := h.ch.Exec(r.Context(), stmt); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	audit.Annotate(r.Context(), "mutation_cancelada", id)
	writeJSON(w, http.StatusOK, map[string]any{"cancelled": id})
}

// Purge apaga as linhas que casam com o recorte (DELETE de verdade: a mutation
// reescreve as parts e libera disco). Dispara e acompanha ~5s; se concluir, reporta
// done + linhas apagadas (contadas com a MESMA cláusula do preview); senão devolve o
// mutation_id para a UI acompanhar.
func (h *Handler) Purge(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	var body purgeBody
	if err := decodeStrict(r, &body); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f := body.filter()
	if err := validatePurge(f); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	// Guarda anti-concorrência: uma limpeza de cada vez na tabela logs.
	if n, err := h.activeMutations(ctx); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	} else if n > 0 {
		http.Error(w, "já existe uma limpeza de logs em andamento", http.StatusConflict)
		return
	}

	// Conta antes (será o deleted_rows) — a mutation em si não conta linhas.
	deleted, err := h.countPurge(ctx, f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Dispara a mutation (assíncrona).
	stmt := "ALTER TABLE logs DELETE WHERE " + purgeWhere(f)
	if f.BodyLike != "" {
		stmt += noLogSuffix
	}
	if err := h.ch.Exec(ctx, stmt); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Rodo em system.query_log: as buscas antigas pelo mesmo trecho guardaram uma
	// cópia legível dele. Best-effort — o resultado vai na resposta.
	queryLogPurged := false
	var queryLogErr string
	if f.BodyLike != "" {
		if err := h.purgeQueryLog(ctx, f.BodyLike); err != nil {
			queryLogErr = err.Error()
		} else {
			queryLogPurged = true
		}
	}

	// A trilha precisa do NÚMERO: sem ele ninguém distingue, meses depois, uma
	// limpeza de 3 linhas de uma que levou 400 mil.
	audit.Annotate(ctx, "deleted_rows", deleted)
	audit.Annotate(ctx, "escopo", describePurge(f))
	if f.BodyLike != "" {
		audit.Annotate(ctx, "query_log_limpo", queryLogPurged)
		audit.Annotate(ctx, "trecho_sha256", fingerprint(f.BodyLike))
	}

	resp := map[string]any{
		"deleted_rows": deleted,
		"scope":        describePurge(f),
		"not_purged":   notReached(f.BodyLike != "", queryLogPurged),
	}
	if f.BodyLike != "" {
		resp["query_log_purged"] = queryLogPurged
		if queryLogErr != "" {
			resp["query_log_error"] = queryLogErr
		}
	}

	// Acompanha até ~5s. Dado pequeno conclui em segundos → resposta síncrona.
	deadline := time.Now().Add(5 * time.Second)
	for {
		st, err := MutationStatus(ctx, h.ch, "logs", "")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		switch {
		case st.FailReason != "":
			resp["done"] = false
			resp["mutation_id"] = st.MutationID
			resp["fail_reason"] = st.FailReason
			audit.Annotate(ctx, "mutation_falhou", st.FailReason)
			writeJSON(w, http.StatusOK, resp)
			return
		case st.Done:
			resp["done"] = true
			writeJSON(w, http.StatusOK, resp)
			return
		case time.Now().After(deadline):
			resp["done"] = false
			resp["mutation_id"] = st.MutationID
			audit.Annotate(ctx, "mutation_pendente", st.MutationID)
			writeJSON(w, http.StatusOK, resp)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// PurgeStatus informa o andamento de uma mutation (para volumes grandes que rodam
// em segundo plano). fail_reason preenchido = mutation travada por erro.
// Sem `id`, devolve só a lista de limpezas TRAVADAS — é como a tela descobre que
// existe uma para cancelar (antes, um 409 permanente não tinha nem diagnóstico).
func (h *Handler) PurgeStatus(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSpace(r.URL.Query().Get("id"))
	travadas, err := h.StuckMutations(r.Context())
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if id == "" {
		writeJSON(w, http.StatusOK, map[string]any{"stuck": travadas})
		return
	}
	st, err := MutationStatus(r.Context(), h.ch, "logs", id)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"done": st.Done, "parts_remaining": st.PartsToDo, "fail_reason": st.FailReason,
		"stuck": travadas,
	})
}
