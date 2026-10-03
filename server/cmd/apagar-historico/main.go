// Comando apagar-historico: apaga o histórico de telemetria no ClickHouse.
//
// Serve ao caso de virada de semântica: quando o significado de uma métrica muda
// (o agente 0.7.0 contava a rede em dobro e somava o steal na CPU; o 0.8.1 não), o
// histórico antigo não é "velho", é INCOMPARÁVEL — e um gráfico que emenda as duas
// convenções na mesma linha mente sem avisar. Zerar e recomeçar com a medida certa
// é, às vezes, mais honesto do que preservar.
//
// POR QUE ISTO NÃO MORA NO AGENTE: o agente roda em cada máquina monitorada, só
// fala OTLP com o gateway e não tem credencial de ClickHouse. Um comando de apagar
// histórico dentro dele daria a QUALQUER host monitorado o poder de zerar o banco
// do painel — um servidor invadido apagaria a própria evidência. Quem tem a conexão
// com o banco é o servidor, e é aqui que a ação pertence.
//
// SEGURANÇA, em seis camadas:
//
//  1. DRY-RUN por padrão. Sem -execute, só relata o que apagaria — e o relatório
//     conta o RECORTE, não a tabela inteira: é o que permite conferir que
//     `-host srv-02` casa o que se espera antes de confirmar.
//  2. -confirmo tem de repetir o NOME DO BANCO. É a trava contra apagar produção
//     achando que é o dev: os dois comandos são idênticos menos por essa palavra.
//  3. ALLOWLIST de tabelas. `schema_migrations` e `legacy_migration_checkpoint`
//     não são apagáveis por nenhuma combinação de flags — zerar a primeira faria
//     as migrações rodarem de novo sobre um banco já migrado.
//  4. Relatório ANTES e DEPOIS, com contagem por tabela no escopo e no total.
//  5. `tenant_id` sempre no WHERE, e TRUNCATE só quando o banco tem um inquilino
//     só — conferido no banco, não presumido. Nomes de host colidem entre
//     clientes ("web01"), e sem essa fronteira `-host web01` alcançaria o alheio.
//  6. TRILHA: o apagamento se registra em `events` DEPOIS de acontecer (antes
//     seria apagado por ele mesmo). Expurgo sem rastro é indistinguível de
//     encobrimento.
//
// O que ele NÃO faz, e diz isso na saída: não alcança backups, não apaga nada no
// Postgres e NÃO revoga a chave de ingestão — com `-host`, o agente daquele
// servidor continua enviando e o host reaparece em minutos. Exclusão completa de
// servidor é outra coisa (internal/hostadmin).
//
// Uso (dentro do container do servidor):
//
//	apagar-historico                                    # dry-run: relata e sai
//	apagar-historico -execute -confirmo painel          # apaga TUDO
//	apagar-historico -execute -confirmo painel -antes-de 2026-08-10T13:13:00Z
//	apagar-historico -execute -confirmo painel -host srv-02
//	apagar-historico -execute -confirmo painel -tabelas metrics,metrics_1m,metrics_1h
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/chquery"
	"github.com/eduardorarruda/revoada/server/internal/config"
)

// tabelasDeDados é a ALLOWLIST: só estas podem ser apagadas, e a ordem é a do
// relatório. Qualquer nome fora daqui é recusado, inclusive vindo de -tabelas.
//
// Ficam DE FORA de propósito:
//   - schema_migrations: é o estado das migrações, não telemetria. Zerá-la faria o
//     boot reaplicar migrações sobre um banco já migrado.
//   - legacy_migration_checkpoint: marca até onde a importação do formato legado
//     chegou; perdê-la reimportaria o que já entrou.
//   - metrics_1m_mv / metrics_1h_mv: são VIEWS, não guardam linha. O dado mora nas
//     tabelas-alvo (metrics_1m/metrics_1h), que estão na lista.
var tabelasDeDados = []string{"metrics", "metrics_1m", "metrics_1h", "events", "logs", "spans"}

func main() {
	execute := flag.Bool("execute", false, "executa de verdade (sem esta flag é só dry-run)")
	confirmo := flag.String("confirmo", "", "exigido com -execute: repita o NOME DO BANCO que vai ser apagado")
	tabelas := flag.String("tabelas", "", "lista separada por vírgula (default: todas as tabelas de telemetria)")
	antesDe := flag.String("antes-de", "", "apaga só o anterior a este instante, em RFC3339 (default: tudo)")
	host := flag.String("host", "", "apaga só a TELEMETRIA deste servidor, não revoga a chave nem para a ingestão (default: todos)")
	tenant := flag.String("tenant", "default", "inquilino cujos dados serão apagados")
	flag.Parse()

	addr, user, pass, db := config.ClickHouse()
	ch := chquery.New(addr, user, pass, db)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	if err := ch.Ping(ctx); err != nil {
		log.Fatalf("ClickHouse indisponível em %s: %v", addr, err)
	}

	alvos, err := resolverTabelas(*tabelas)
	if err != nil {
		log.Fatal(err)
	}
	corte, err := resolverCorte(*antesDe)
	if err != nil {
		log.Fatal(err)
	}
	escopo := descreverEscopo(corte, *host)
	cond := predicado(*tenant, corte, *host)

	log.Printf("banco: %s em %s", db, addr)
	log.Printf("inquilino: %s", *tenant)
	log.Printf("escopo: %s", escopo)
	log.Printf("tabelas: %s", strings.Join(alvos, ", "))
	log.Println("---- antes (no escopo · total da tabela) ----")
	relatar(ctx, ch, alvos, cond)

	if !*execute {
		log.Println("== DRY-RUN, nada foi apagado ==")
		log.Printf("Para apagar de verdade: -execute -confirmo %s", db)
		return
	}
	if *confirmo != db {
		log.Fatalf("RECUSADO: -confirmo precisa ser exatamente %q (o nome do banco). Veio %q.\n"+
			"Esta trava existe para não apagar produção achando que é o ambiente de teste.", db, *confirmo)
	}

	// TRUNCATE zera a tabela inteira, de TODOS os inquilinos. Só é aceitável quando
	// existe um inquilino só — e isso é verificado no banco, não presumido. Havendo
	// mais de um, cai para ALTER DELETE com o predicado, que respeita a fronteira.
	truncavel := corte == nil && *host == ""
	if truncavel {
		unico, err := inquilinoUnico(ctx, ch, alvos, *tenant)
		if err != nil {
			log.Fatalf("conferindo os inquilinos do banco: %v", err)
		}
		truncavel = unico
		if !unico {
			log.Printf("há mais de um inquilino no banco: apagando só %q (sem TRUNCATE)", *tenant)
			cond = predicado(*tenant, nil, "")
		}
	}

	log.Println("== EXECUTANDO ==")
	for _, t := range alvos {
		if err := apagar(ctx, ch, t, cond, truncavel); err != nil {
			log.Fatalf("apagando %s: %v", t, err)
		}
		log.Printf("  %-12s apagado (%s)", t, escopo)
	}

	// TRUNCATE é síncrono; mutation (ALTER DELETE) é assíncrona. Espera as pendentes
	// para o relatório final descrever o banco de verdade, e não um estado a caminho.
	if !truncavel {
		log.Println("aguardando as mutations terminarem…")
		if err := esperarMutations(ctx, ch, alvos); err != nil {
			log.Printf("  AVISO: %v (o apagamento segue em segundo plano)", err)
		}
	}

	log.Println("---- depois (no escopo · total da tabela) ----")
	relatar(ctx, ch, alvos, cond)
	registrarNaTrilha(ctx, ch, *tenant, escopo, alvos)
	naoAlcancado()
	log.Println("== concluído ==")
}

// registrarNaTrilha grava o apagamento como evento, DEPOIS de apagar.
//
// A ordem é o ponto: `events` está na allowlist, então um registro gravado antes
// seria apagado pela própria operação — e um expurgo que apaga o próprio registro é
// indistinguível de encobrimento. Gravado depois, ele sobrevive e é a primeira linha
// da tabela recém-esvaziada.
//
// Falhar aqui não desfaz o apagamento (já aconteceu), mas precisa GRITAR: um
// apagamento sem rastro é exatamente o que este bloco existe para impedir.
func registrarNaTrilha(ctx context.Context, ch *chquery.Client, tenant, escopo string, alvos []string) {
	err := ch.InsertEvent(ctx, tenant, "historico_apagado",
		"Histórico de telemetria apagado",
		fmt.Sprintf("Escopo: %s. Tabelas: %s.", escopo, strings.Join(alvos, ", ")),
		map[string]string{"escopo": escopo, "tabelas": strings.Join(alvos, ",")})
	if err != nil {
		log.Printf("  ATENÇÃO: o apagamento foi feito, mas NÃO ficou registrado em `events`: %v", err)
		return
	}
	log.Println("  registrado em `events` (kind=historico_apagado)")
}

// naoAlcancado declara o que este comando NÃO apaga. Um relatório que diz só
// "concluído" deixa o operador achando que o dado sumiu do mundo — e o backup o
// ressuscita inteiro na primeira restauração.
func naoAlcancado() {
	log.Println("NÃO alcançado por este comando:")
	log.Println("  · backups (ClickHouse e pg_dump), restaurar um backup traz o histórico de volta")
	log.Println("  · Postgres: inventário de hosts, resultados de sondagem, alertas, trilha de auditoria")
	log.Println("  · a chave de ingestão do servidor continua válida: o agente segue enviando")
}

// inquilinoUnico diz se `tenant` é o único presente nas tabelas alvo. Serve para
// decidir entre TRUNCATE (rápido, mas cego a inquilino) e ALTER DELETE (respeita a
// fronteira). Conferido no banco, não presumido pela configuração.
func inquilinoUnico(ctx context.Context, ch *chquery.Client, alvos []string, tenant string) (bool, error) {
	for _, t := range alvos {
		rows, err := ch.QueryJSON(ctx, fmt.Sprintf(
			"SELECT count() AS c FROM (SELECT DISTINCT tenant_id FROM %s WHERE tenant_id != %s LIMIT 1)", t, aspas(tenant)))
		if err != nil {
			return false, err
		}
		if len(rows) > 0 && inteiro(rows[0]["c"]) > 0 {
			return false, nil
		}
	}
	return true, nil
}

// resolverTabelas valida a lista pedida contra a allowlist. Nome desconhecido é erro
// FATAL, não aviso: um apagamento que ignora em silêncio o que não entendeu deixa o
// operador achando que apagou o que pediu.
func resolverTabelas(lista string) ([]string, error) {
	if strings.TrimSpace(lista) == "" {
		return tabelasDeDados, nil
	}
	permitida := map[string]bool{}
	for _, t := range tabelasDeDados {
		permitida[t] = true
	}
	var out []string
	for _, raw := range strings.Split(lista, ",") {
		t := strings.TrimSpace(raw)
		if t == "" {
			continue
		}
		if !permitida[t] {
			return nil, fmt.Errorf("tabela %q não é apagável por este comando.\nPermitidas: %s",
				t, strings.Join(tabelasDeDados, ", "))
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-tabelas veio vazio")
	}
	return out, nil
}

func resolverCorte(s string) (*time.Time, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(s))
	if err != nil {
		return nil, fmt.Errorf("-antes-de precisa estar em RFC3339 (ex.: 2026-08-10T13:13:00Z): %w", err)
	}
	return &t, nil
}

func descreverEscopo(corte *time.Time, host string) string {
	var partes []string
	if corte != nil {
		partes = append(partes, "anterior a "+corte.UTC().Format(time.RFC3339))
	}
	if host != "" {
		partes = append(partes, "servidor "+host)
	}
	if len(partes) == 0 {
		return "TUDO (todo o histórico, todos os servidores)"
	}
	return strings.Join(partes, " e ")
}

// predicado monta as condições do escopo. É a fonte ÚNICA do WHERE: o mesmo valor
// alimenta o relatório e o apagamento, e é isso que garante que o número mostrado
// antes é o número apagado depois. Quando as duas consultas divergem, o dry-run vira
// decoração — o operador confere um total e confirma outra coisa.
//
// `tenant_id` entra sempre. É a fronteira entre inquilinos, e todo o resto do código
// que apaga no ClickHouse a respeita (logs/purge.go:66). Nomes de host colidem entre
// clientes por natureza ("web01", "srv1"); sem esta condição, `-host web01` alcançaria
// o servidor de outro cliente.
func predicado(tenant string, corte *time.Time, host string) []string {
	cond := []string{fmt.Sprintf("tenant_id = %s", aspas(tenant))}
	if corte != nil {
		cond = append(cond, fmt.Sprintf("ts < toDateTime(%d)", corte.Unix()))
	}
	if host != "" {
		cond = append(cond, fmt.Sprintf("labels['host'] = %s", aspas(host)))
	}
	return cond
}

// apagar executa a operação certa para o escopo.
//
// `truncavel` só vem verdadeiro quando não há recorte E o banco tem um inquilino só
// (conferido em inquilinoUnico). Aí TRUNCATE é instantâneo e não gera mutation — um
// ALTER DELETE sem recorte sobre 30 milhões de linhas reescreveria as parts inteiras,
// minutos de I/O para chegar ao mesmo lugar.
func apagar(ctx context.Context, ch *chquery.Client, tabela string, cond []string, truncavel bool) error {
	if truncavel {
		return ch.Exec(ctx, "TRUNCATE TABLE "+tabela)
	}
	return ch.Exec(ctx, fmt.Sprintf("ALTER TABLE %s DELETE WHERE %s", tabela, strings.Join(cond, " AND ")))
}

// aspas escapa uma string para literal SQL do ClickHouse. Vale mesmo o host vindo do
// operador: um hostname com aspa simples quebraria a query, e o hábito de escapar
// não pode depender de quem digitou.
func aspas(s string) string {
	return "'" + strings.NewReplacer(`\`, `\\`, `'`, `\'`).Replace(s) + "'"
}

// relatar conta o que está NO ESCOPO e, ao lado, o total da tabela.
//
// Contar só o total era o defeito: com `-host srv-02`, o dry-run mostrava as 17
// milhões de linhas da tabela inteira, e o operador não tinha como saber se o recorte
// casava 3 linhas ou 3 milhões antes de confirmar. Pior: um hostname digitado errado
// produzia um relatório IDÊNTICO ao do nome certo. Mostrar os dois números é o que
// permite conferir o recorte antes de executá-lo.
func relatar(ctx context.Context, ch *chquery.Client, alvos []string, cond []string) {
	escopoSQL := strings.Join(cond, " AND ")
	for _, t := range alvos {
		rows, err := ch.QueryJSON(ctx, fmt.Sprintf(
			`SELECT countIf(%[1]s) AS no_escopo, count() AS total,
			        toUnixTimestamp(minIf(ts, %[1]s)) AS mais_antigo,
			        toUnixTimestamp(maxIf(ts, %[1]s)) AS mais_novo
			 FROM %[2]s`, escopoSQL, t))
		if err != nil {
			log.Printf("  %-12s (não deu para contar: %v)", t, err)
			continue
		}
		if len(rows) == 0 {
			log.Printf("  %-12s vazia", t)
			continue
		}
		n, total := inteiro(rows[0]["no_escopo"]), inteiro(rows[0]["total"])
		if n == 0 {
			log.Printf("  %-12s %12d no escopo · %d na tabela  (nada a apagar aqui)", t, 0, total)
			continue
		}
		log.Printf("  %-12s %12d no escopo · %d na tabela   de %s até %s", t, n, total,
			quando(rows[0]["mais_antigo"]), quando(rows[0]["mais_novo"]))
	}
}

// esperarMutations bloqueia até as mutations pendentes deste banco terminarem, com
// teto de tempo. Sem isso o relatório "depois" mostraria o banco a meio caminho e o
// operador concluiria que o comando falhou.
// `latest_fail_reason = ”` e o filtro por tabela não são detalhe: uma mutation que
// falhou de forma PERMANENTE (part corrompida, disco cheio) fica em `is_done=0` para
// sempre. Sem os dois filtros, uma mutation morta de um expurgo LGPD antigo na tabela
// `logs` faria este comando girar 20 minutos e terminar avisando "ainda há pendentes"
// — com o apagamento dele já concluído. O operador leria isso como falha. É o mesmo
// defeito que logs/purge.go já diagnosticou e corrigiu em activeMutations.
func esperarMutations(ctx context.Context, ch *chquery.Client, alvos []string) error {
	emAlvo := make([]string, 0, len(alvos))
	for _, t := range alvos {
		emAlvo = append(emAlvo, aspas(t))
	}
	limite := time.Now().Add(20 * time.Minute)
	for {
		rows, err := ch.QueryJSON(ctx, fmt.Sprintf(
			`SELECT count() AS pendentes FROM system.mutations
			 WHERE database = currentDatabase() AND table IN (%s)
			   AND is_done = 0 AND latest_fail_reason = ''`, strings.Join(emAlvo, ",")))
		if err != nil {
			return fmt.Errorf("consultando system.mutations: %w", err)
		}
		if len(rows) == 0 || inteiro(rows[0]["pendentes"]) == 0 {
			return nil
		}
		if time.Now().After(limite) {
			return fmt.Errorf("ainda há %d mutation(s) pendente(s) após 20 min", inteiro(rows[0]["pendentes"]))
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

// inteiro extrai um inteiro do JSON do ClickHouse: UInt64/Int64 vêm como string
// (para não perder precisão), os menores vêm como número.
func inteiro(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case string:
		var n int64
		_, _ = fmt.Sscan(strings.TrimSpace(t), &n)
		return n
	}
	return 0
}

func quando(v any) string {
	n := inteiro(v)
	if n <= 0 {
		return "—"
	}
	return time.Unix(n, 0).UTC().Format("2006-01-02 15:04:05Z")
}
