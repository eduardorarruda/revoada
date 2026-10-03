package selfinstall

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// comTrailer monta o que o painel produz: binário + JSON + magic + tamanho.
// Espelha server/internal/installer/trailer.go — se os dois lados divergirem,
// este teste (e o de lá) quebram, que é exatamente o alarme que queremos.
func comTrailer(binario []byte, rec Recipe) []byte {
	js, err := json.Marshal(rec)
	if err != nil {
		panic(err)
	}
	var b bytes.Buffer
	b.Write(binario)
	b.Write(js)
	b.WriteString(Magic)
	_ = binary.Write(&b, binary.BigEndian, uint64(len(js)))
	return b.Bytes()
}

func TestLeReceitaEPreservaOFimDoBinario(t *testing.T) {
	binario := []byte("\x7fELF isto finge ser o agente")
	quero := Recipe{GatewayURL: "https://painel.exemplo", Key: "chave-123", PanelURL: "https://painel.exemplo", IntervalSeconds: 20, MemoryLimitMB: 220, MaxProcs: 2}
	arq := comTrailer(binario, quero)

	got, fim, err := ReadFrom(bytes.NewReader(arq), int64(len(arq)))
	if err != nil {
		t.Fatalf("erro inesperado: %v", err)
	}
	if got != quero {
		t.Errorf("receita = %+v, queria %+v", got, quero)
	}
	// O offset devolvido tem de recortar exatamente o binário original — é dele
	// que sai a cópia instalada, sem a chave grudada no fim.
	if fim != int64(len(binario)) {
		t.Errorf("fim do binário = %d, queria %d", fim, len(binario))
	}
	if !bytes.Equal(arq[:fim], binario) {
		t.Errorf("recorte %q != binário original %q", arq[:fim], binario)
	}
}

func TestBinarioComumNaoTemReceita(t *testing.T) {
	// O agente instalado é o binário puro: tem de sair por ErrSemReceita, em
	// silêncio, para seguir direto ao loop de coleta.
	arq := bytes.Repeat([]byte("agente"), 500)
	if _, _, err := ReadFrom(bytes.NewReader(arq), int64(len(arq))); !errors.Is(err, ErrSemReceita) {
		t.Fatalf("erro = %v, queria ErrSemReceita", err)
	}
}

func TestArquivoMenorQueOTrailerNaoQuebra(t *testing.T) {
	arq := []byte("oi")
	if _, _, err := ReadFrom(bytes.NewReader(arq), int64(len(arq))); !errors.Is(err, ErrSemReceita) {
		t.Fatalf("erro = %v, queria ErrSemReceita", err)
	}
}

func TestDownloadTruncadoAcusaEmVezDeSilenciar(t *testing.T) {
	// Magic presente mas o JSON não cabe no arquivo: é um download pela metade.
	// Tratar como "binário comum" faria o agente subir sem config e confundir
	// quem instalou; tem de doer.
	var b bytes.Buffer
	b.WriteString("binário")
	b.WriteString(Magic)
	_ = binary.Write(&b, binary.BigEndian, uint64(999999))
	arq := b.Bytes()

	_, _, err := ReadFrom(bytes.NewReader(arq), int64(len(arq)))
	if err == nil || errors.Is(err, ErrSemReceita) {
		t.Fatalf("erro = %v, queria uma queixa de truncamento", err)
	}
	if !strings.Contains(err.Error(), "truncado") {
		t.Errorf("mensagem sem pista do que fazer: %q", err)
	}
}

func TestReceitaSemChaveEhRejeitada(t *testing.T) {
	arq := comTrailer([]byte("binário"), Recipe{GatewayURL: "https://painel.exemplo"})
	_, _, err := ReadFrom(bytes.NewReader(arq), int64(len(arq)))
	if err == nil || errors.Is(err, ErrSemReceita) {
		t.Fatalf("erro = %v, queria rejeição por receita incompleta", err)
	}
}

func TestYAMLDoWindowsSaiUtilizavel(t *testing.T) {
	y := yamlWindows(Recipe{GatewayURL: "https://painel.exemplo", Key: "k1", MemoryLimitMB: 220, MaxProcs: 2}, "PC-DA-RECEPCAO")
	for _, esperado := range []string{
		"gateway_url: https://painel.exemplo",
		"key: k1",
		"hostname: PC-DA-RECEPCAO",
		"interval_seconds: 15",            // receita sem intervalo cai no default do agente
		`buffer_dir: 'C:\revoada\buffer'`, // aspas simples: a contrabarra é literal em YAML
		"memory_limit_mb: 220",
	} {
		if !strings.Contains(y, esperado) {
			t.Errorf("agent.yaml sem %q:\n%s", esperado, y)
		}
	}
	// Coletores de log do Linux não entram no Windows — ligá-los só geraria erro
	// repetido no log de um recurso que não existe.
	if strings.Contains(y, "collect_journald") {
		t.Errorf("agent.yaml do Windows não deveria citar journald:\n%s", y)
	}
}
