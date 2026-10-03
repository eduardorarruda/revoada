package logtail

import (
	"context"
	"encoding/json"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

// Journald coleta o journal do systemd (source=journald) via
// `journalctl -o json --follow`. Cada linha do stdout é um objeto JSON = 1
// registro (não precisa de costura multiline). Best-effort: se journalctl não
// existe ou falha, emite um warn e encerra o coletor sem derrubar o agente.
type Journald struct {
	sink *sink
	log  *slog.Logger
}

func NewJournald(gatewayURL, key, host string, log *slog.Logger) *Journald {
	return &Journald{sink: newSink(gatewayURL, key, host), log: log}
}

// arqCursorJournald / chaveCursor: o journald guarda UM cursor só, mas usa o mesmo
// cursorStore do docker — um formato de estado a menos para manter.
const (
	arqCursorJournald = "journald.cursor"
	chaveCursor       = "cursor"
)

// intervaloSalvarCursor é de quanto em quanto tempo o cursor vai ao disco. Não é por
// registro: num host movimentado isso seria uma escrita por linha de log. O que se
// arrisca perdendo até 5s de posição é reenviar até 5s de journal depois de uma queda
// abrupta — repetição, que o painel mostra e o operador entende, e não silêncio.
const intervaloSalvarCursor = 5 * time.Second

func (j *Journald) Run(ctx context.Context) {
	if _, err := exec.LookPath("journalctl"); err != nil {
		j.log.Warn("journald: journalctl não encontrado, coletor desativado", "err", err)
		return
	}

	// Retoma de onde parou. Sem isto, `--since now` fazia toda parada do agente —
	// inclusive a auto-atualização, que é rotina e acontece sozinha — abrir um buraco
	// no journal deste host. Buraco que, no painel, é indistinguível de um servidor
	// que ficou quieto.
	cur := novoCursor(arqCursorJournald)
	guardado := cur.ler(chaveCursor)
	args := []string{"-o", "json", "--follow", "--no-pager"}
	if guardado != "" {
		// --after-cursor: retoma na entrada SEGUINTE à última já enviada (não na
		// mesma), que é o que evita duplicar uma linha a cada reinício.
		args = append(args, "--after-cursor", guardado)
	} else {
		args = append(args, "--since", "now")
	}

	cmd := exec.CommandContext(ctx, "journalctl", args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		j.log.Warn("journald: stdout pipe falhou, coletor desativado", "err", err)
		return
	}
	if err := cmd.Start(); err != nil {
		j.log.Warn("journald: journalctl não iniciou (permissão?), coletor desativado", "err", err)
		return
	}

	var lidos atomic.Int64
	recs := make(chan record, 4096)
	go func() {
		defer close(recs)
		// lerLinhas em vez de bufio.Scanner: o Scanner ABORTA o stream inteiro numa
		// linha maior que o buffer, e o Supervise reergueria o coletor para ele morrer
		// na mesma linha — laço de reinício por causa de UMA entrada comprida.
		lerLinhas(stdout, func(linha []byte) bool {
			lidos.Add(1)
			r, cursor, ok := parseJournal(linha)
			if !ok {
				return true
			}
			// O cursor viaja COM o registro e só é gravado quando o gateway aceita o
			// lote (ver drainCom). Gravá-lo aqui, na leitura, deixaria o cursor à frente
			// das linhas que ainda não saíram — e o encerramento do agente (toda
			// auto-atualização é um) perderia justamente a última janela, que é o que
			// este cursor existe para não perder.
			r.cursor = cursor
			select {
			case recs <- r:
				return true
			case <-ctx.Done():
				return false
			}
		})
	}()
	ultimoSalvo := time.Now()
	j.sink.drainCom(ctx, recs, func(ultimo record) {
		cur.gravar(chaveCursor, ultimo.cursor)
		// Não grava em disco a cada lote: num host movimentado seria uma escrita por
		// par de segundos. Perder até `intervaloSalvarCursor` de posição numa queda
		// abrupta significa REPETIR até 5s de journal, não perdê-lo.
		if time.Since(ultimoSalvo) >= intervaloSalvarCursor {
			cur.salvar()
			ultimoSalvo = time.Now()
		}
	})
	cur.salvar()
	err = cmd.Wait()

	// O journalctl recusou o cursor guardado (journal rotacionado/vacuum, host
	// restaurado de snapshot, /var/log/journal apagado) e não entregou nada? Então o
	// cursor é lixo e insistir nele deixaria o coletor num laço de falha com o
	// Supervise. Esquecer faz a próxima tentativa cair em `--since now`: perder o
	// histórico é ruim, parar de coletar é pior.
	if err != nil && guardado != "" && lidos.Load() == 0 && ctx.Err() == nil {
		j.log.Warn("journald: o cursor guardado foi recusado; recomeçando do momento atual (o histórico anterior a agora não será coletado)", "err", err)
		cur.esquecer(chaveCursor)
	}
}

// parseJournal converte um objeto JSON do journalctl num registro. Extrai
// MESSAGE→body, PRIORITY→severity e _SYSTEMD_UNIT (ou SYSLOG_IDENTIFIER)→unit/service.
// Devolve também o `__CURSOR` da entrada — é ele que o coletor persiste para retomar
// no ponto certo depois de uma parada do agente (ver cursor.go).
func parseJournal(line []byte) (record, string, bool) {
	var e map[string]any
	if err := json.Unmarshal(line, &e); err != nil {
		return record{}, "", false
	}
	cursor := jstr(e["__CURSOR"])
	body := jstr(e["MESSAGE"])
	if body == "" {
		// Entrada sem MESSAGE não vira registro, mas o cursor dela ainda vale: sem
		// devolvê-lo, uma sequência de entradas assim faria o coletor retomar antes
		// delas e relê-las a cada reinício.
		return record{}, cursor, false
	}
	prio := 6 // default: info
	if p, err := strconv.Atoi(strings.TrimSpace(jstr(e["PRIORITY"]))); err == nil {
		prio = p
	}
	unit := jstr(e["_SYSTEMD_UNIT"])
	if unit == "" {
		unit = jstr(e["SYSLOG_IDENTIFIER"])
	}
	service := unit
	if service == "" {
		service = "journald"
	}
	labels := map[string]string{"source": "journald"}
	if unit != "" {
		labels["unit"] = unit
	}
	return record{service: service, severity: severityFromPriority(prio), body: body, labels: labels}, cursor, true
}

// jstr coage um valor JSON do journald para string. Campos como MESSAGE podem vir
// como string ou como array de bytes (quando não-UTF8); PRIORITY vem como string.
func jstr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return strconv.FormatFloat(t, 'f', -1, 64)
	case []any:
		b := make([]byte, 0, len(t))
		for _, x := range t {
			if f, ok := x.(float64); ok {
				b = append(b, byte(int(f)))
			}
		}
		return string(b)
	default:
		return ""
	}
}
