package logtail

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// Provas do balde do tamanho do ciclo (validação de 02/10/2026).
//
// O defeito: o balde guardava só 2 s de cota, mas o Tailer de arquivos manda um
// lote com 10 s de linhas. Ao vivo, 1500 linhas/s escritas num arquivo (30% do teto
// de 5000) perderam 33% no painel, e uma rajada de 20 mil linhas logo depois do
// boot chegou com 5 mil.

// PROVA: um ciclo de arquivo dentro da taxa passa inteiro, ciclo após ciclo.
func TestLoteDeUmCicloDentroDaTaxaPassaInteiro(t *testing.T) {
	c := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	rl := newRateLimiter(5000, c.now)
	const porCiclo = 1500 * 10 // 1500 linhas/s × 10 s
	for ciclo := 0; ciclo < 5; ciclo++ {
		c.add(cicloDeLeitura)
		if got := rl.admit(porCiclo); got != porCiclo {
			t.Fatalf("ciclo %d: 1500 linhas/s (30%% do teto) perdeu linhas: passaram %d de %d", ciclo, got, porCiclo)
		}
	}
}

// PROVA: o primeiro lote depois do boot também encontra o ciclo inteiro de cota —
// a recomposição conta desde a criação do balde, não desde o primeiro admit.
func TestPrimeiroLoteDepoisDoBootNaoEhPenalizado(t *testing.T) {
	c := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	rl := newRateLimiter(5000, c.now)
	c.add(cicloDeLeitura)
	if got := rl.admit(20000); got != 20000 {
		t.Fatalf("rajada de 20 mil linhas num ciclo (2000/s) cortada para %d", got)
	}
}

// PROVA: a MÉDIA continua limitada — o balde maior não vira cota infinita.
func TestTaxaSustentadaAcimaDoTetoContinuaCortada(t *testing.T) {
	c := &fakeClock{t: time.Unix(1_700_000_000, 0)}
	rl := newRateLimiter(5000, c.now)
	total := 0
	const ciclos = 6
	for i := 0; i < ciclos; i++ {
		c.add(cicloDeLeitura)
		total += rl.admit(10000 * 10) // 10 mil linhas/s, o dobro do teto
	}
	// Teto: 1 s de cota inicial + 5000/s durante o tempo decorrido.
	limite := 5000 + 5000*ciclos*10
	if total > limite {
		t.Fatalf("passaram %d linhas em %d s; o teto de 5000/s permite no máximo %d", total, ciclos*10, limite)
	}
	if total < 5000*ciclos*10 {
		t.Fatalf("cortou abaixo da taxa: %d linhas em %d s (esperado ≥ %d)", total, ciclos*10, 5000*ciclos*10)
	}
}

// PROVA: o mesmo vale para o teto de bytes — 300 KiB/s (30% de 1 MiB/s) num lote de
// 10 s passa inteiro.
func TestTetoDeBytesComportaUmCicloDentroDaTaxa(t *testing.T) {
	agora := time.Unix(1_700_000_000, 0)
	bl := newByteLimiter(1<<20, relogioFixo(&agora))
	lote := make([]record, 300) // 300 × ~10 KiB ≈ 3 MiB = 300 KiB/s durante 10 s
	for i := range lote {
		lote[i] = linhaDe(10 << 10)
	}
	for ciclo := 0; ciclo < 3; ciclo++ {
		agora = agora.Add(cicloDeLeitura)
		if k := bl.admit(lote); k != len(lote) {
			t.Fatalf("ciclo %d: 300 KiB/s perdeu linhas: passaram %d de %d", ciclo, k, len(lote))
		}
	}
}

// PROVA: nenhum envio passa dos tetos do gateway, e a ordem e a contagem fecham.
func TestFatiarEnviosRespeitaTetosDoGateway(t *testing.T) {
	recs := make([]record, 12001)
	for i := range recs {
		recs[i] = record{service: "app", severity: "INFO", body: "linha " + strings.Repeat("x", i%7)}
	}
	envios := fatiarEnvios("h", recs, false)
	if len(envios) != 3 {
		t.Fatalf("12001 registros deviam virar 3 envios de até %d, viraram %d", maxRegistrosPorEnvio, len(envios))
	}
	total := 0
	for _, e := range envios {
		if e.registros > maxRegistrosPorEnvio {
			t.Fatalf("envio com %d registros (teto %d)", e.registros, maxRegistrosPorEnvio)
		}
		if n := bytes.Count(e.corpo, []byte("\n")); n != e.registros {
			t.Fatalf("envio declara %d registros e tem %d linhas NDJSON", e.registros, n)
		}
		total += e.registros
	}
	if total != len(recs) {
		t.Fatalf("perdeu registros ao fatiar: %d de %d", total, len(recs))
	}

	grandes := make([]record, 10)
	for i := range grandes {
		grandes[i] = linhaDe(1 << 20)
	}
	for _, e := range fatiarEnvios("h", grandes, false) {
		if len(e.corpo) > maxBytesPorEnvio+(1<<20)+512 {
			t.Fatalf("envio de %d bytes passa do teto de %d", len(e.corpo), maxBytesPorEnvio)
		}
	}
}

// PROVA (fim a fim no sink): um lote acima do teto de linhas por requisição do
// gateway chega inteiro, em ordem, contra um coletor que recusa >20 mil linhas
// com 413 exatamente como o gateway real.
func TestSinkLoteAcimaDoTetoDoGatewayChegaInteiro(t *testing.T) {
	zeraPerdas(t)
	var mu sync.Mutex
	var recebidas []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var linhas []string
		sc := bufio.NewScanner(r.Body)
		for sc.Scan() {
			linhas = append(linhas, sc.Text())
		}
		if len(linhas) > 20000 {
			w.WriteHeader(http.StatusRequestEntityTooLarge)
			return
		}
		mu.Lock()
		recebidas = append(recebidas, linhas...)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := sinkDeTeste(srv.URL)
	s.bytes = nil
	recs := make([]record, 25000)
	for i := range recs {
		recs[i] = record{service: "app", severity: "INFO", body: "linha " + itoaTeste(i)}
	}
	if !s.post(context.Background(), recs) {
		t.Fatal("post devolveu falha para um lote que o gateway aceitaria fatiado")
	}
	if len(recebidas) != len(recs) {
		t.Fatalf("chegaram %d de %d linhas", len(recebidas), len(recs))
	}
	for i, l := range recebidas {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("linha %d não é JSON: %v", i, err)
		}
		if m["body"] != "linha "+itoaTeste(i) {
			t.Fatalf("ordem quebrada na posição %d: %v", i, m["body"])
		}
	}
}

func itoaTeste(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}
