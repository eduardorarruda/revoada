package config

import "testing"

func ptr(n int) *int { return &n }

// TestLogByteRateLimit fixa a salvaguarda de VOLUME de log.
//
// O defeito que ela corrige: a garantia declarada sempre foi em bytes ("~1 MB/s a
// 200 B/linha"), mas o único teto implementado era de LINHAS/s. Uma aplicação que
// loga JSON de 4 KB por linha passa folgada em 5000 linhas/s e manda 20 MB/s —
// 1,7 TB/dia saindo do link do servidor do cliente, com o agente convencido de que
// está dentro do limite.
func TestLogByteRateLimit(t *testing.T) {
	casos := []struct {
		nome string
		cfg  Config
		want int
	}{
		{"ausente usa o default de volume", Config{}, defaultLogByteRate},
		{"0 explícito desliga (escolha do operador)", Config{LogMaxBytesPerSec: ptr(0)}, 0},
		{"valor do operador é respeitado", Config{LogMaxBytesPerSec: ptr(256 << 10)}, 256 << 10},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			if got := c.cfg.LogByteRateLimit(); got != c.want {
				t.Fatalf("LogByteRateLimit() = %d, quero %d", got, c.want)
			}
		})
	}
}

// TestOsDoisTetosSaoIndependentes: o teto de linhas não pode responder pelo de
// bytes nem vice-versa. Desligar um deixa o outro de pé — é o que garante que
// `log_max_lines_per_sec: 0` (feito para liberar um host tagarela de linhas curtas)
// não abre a porteira do volume junto.
func TestOsDoisTetosSaoIndependentes(t *testing.T) {
	c := Config{LogMaxLinesPerSec: ptr(0)}
	if c.LogRateLimit() != 0 {
		t.Fatalf("linhas/s deveria estar desligado, veio %d", c.LogRateLimit())
	}
	if c.LogByteRateLimit() != defaultLogByteRate {
		t.Fatalf("desligar linhas/s não pode desligar bytes/s: veio %d", c.LogByteRateLimit())
	}

	c = Config{LogMaxBytesPerSec: ptr(0)}
	if c.LogByteRateLimit() != 0 {
		t.Fatalf("bytes/s deveria estar desligado, veio %d", c.LogByteRateLimit())
	}
	if c.LogRateLimit() != defaultLogRate {
		t.Fatalf("desligar bytes/s não pode desligar linhas/s: veio %d", c.LogRateLimit())
	}
}

// TestDefaultDeVolumeCobreOCasoQuePassavaBatido é a aritmética do defeito, escrita
// como teste para ninguém "arredondar" o default para cima sem perceber o que
// está liberando.
func TestDefaultDeVolumeCobreOCasoQuePassavaBatido(t *testing.T) {
	const bytesPorLinhaJSON = 4096
	volumeQuePassava := defaultLogRate * bytesPorLinhaJSON // 20 MB/s
	if volumeQuePassava <= defaultLogByteRate {
		t.Fatalf("o default de bytes/s (%d) não corta o caso de 4 KB/linha a %d linhas/s (%d B/s) — a garantia de volume voltou a ser fictícia",
			defaultLogByteRate, defaultLogRate, volumeQuePassava)
	}
}

// TestLogRateLimitMantemComportamento: a adição do teto de bytes não pode ter
// mexido no de linhas (regressão nos hosts já instalados).
func TestLogRateLimitMantemComportamento(t *testing.T) {
	if got := (Config{}).LogRateLimit(); got != defaultLogRate {
		t.Fatalf("LogRateLimit() default = %d, quero %d", got, defaultLogRate)
	}
	if got := (Config{LogMaxLinesPerSec: ptr(10)}).LogRateLimit(); got != 10 {
		t.Fatalf("LogRateLimit() = %d, quero 10", got)
	}
}
