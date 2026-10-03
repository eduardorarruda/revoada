package sitecheck

import (
	"fmt"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/server/internal/store"
)

// EVIDÊNCIA DA SONDAGEM — por que o painel afirma que o alvo está fora.
//
// O aviso que chegava ao celular dizia só "está fora do ar". Quem recebia abria o
// endereço no próprio telefone, via a página carregar e respondia "errado" — sem ter
// como saber que o painel tinha tentado duas vezes seguidas e ficado 10 s pendurado
// no handshake TLS. Uma afirmação sem evidência não é verificável, e alerta que não
// se pode conferir é alerta em que se para de acreditar.
//
// Esta frase acrescenta ao aviso o que foi medido: quantas tentativas, em que fase a
// conexão morreu e com que régua. Ela NÃO entra nos labels — label vira fingerprint,
// e um número que muda a cada ciclo quebraria a identidade do alerta entre o disparo
// e a recuperação.
func evidenciaDaSondagem(chk *store.SiteCheck, res Result, state string) string {
	if state == "resolved" {
		if res.Status > 0 {
			return fmt.Sprintf("a sondagem voltou a responder HTTP %d em %s.", res.Status, fmtDur(res.TotalMs))
		}
		return fmt.Sprintf("a sondagem voltou a responder em %s.", fmtDur(res.TotalMs))
	}

	motivo := motivoDaFalha(chk, res)

	// Consenso multi-sonda: quem conta a história é o número de sondas que falharam,
	// não as tentativas de uma delas.
	if strings.HasPrefix(res.Diagnosis, "down em ") {
		return motivo + "."
	}

	tentativas := chk.ConsecutiveFails
	if tentativas < 1 {
		tentativas = 1
	}
	plural := "tentativas seguidas do nosso teste falharam"
	if tentativas == 1 {
		plural = "tentativa do nosso teste falhou"
	}
	return fmt.Sprintf("%d %s, %s.%s", tentativas, plural, motivo, origemDaMedicao(chk))
}

// origemDaMedicao declara DE ONDE o painel olhou. É a parte que faltava no aviso que
// gerou o "errado": com uma sonda só, "não alcancei" não é a mesma coisa que "está
// fora para todo mundo" — ainda mais quando o alvo está atrás de CDN, onde uma borda
// ruim atinge o nosso datacenter e ninguém mais. Dizer isso na própria mensagem é o
// que separa uma afirmação verificável de um palpite com emoji vermelho.
func origemDaMedicao(chk *store.SiteCheck) string {
	if len(chk.ProbeLocations) == 0 {
		return " Medido de uma sonda só (o servidor do painel): pode estar no ar por outro caminho de rede."
	}
	return " Sondas designadas: " + strings.Join(chk.ProbeLocations, ", ") + " (além da central)."
}

// motivoDaFalha diz EM QUE FASE a conexão morreu, em português. O rótulo técnico
// sozinho ("connect_timeout") engana: ele sai igual quando o TCP nem foi respondido
// e quando o TCP conectou em 1 ms e foi o TLS que ficou mudo por 10 s — que é o caso
// típico de borda de CDN ruim. Os tempos por fase é que separam os dois.
func motivoDaFalha(chk *store.SiteCheck, res Result) string {
	esperado := orInt(chk.ExpectStatus, 200)

	switch res.Diagnosis {
	case DiagDNSError:
		return "o nome do endereço não resolveu no DNS"
	case DiagConnRefused:
		return "a porta respondeu \"fechada\" na hora"
	case DiagConnError:
		return "a conexão caiu no meio"
	case DiagHTTP5xx:
		return fmt.Sprintf("o servidor respondeu HTTP %d, erro dele, não nosso", res.Status)
	case DiagHTTPStatus:
		return fmt.Sprintf("respondeu HTTP %d quando esperávamos %d", res.Status, esperado)
	case DiagKeywordAusent:
		return "a página respondeu, mas sem a palavra-chave esperada"
	case DiagRedirectLoop:
		return "o endereço devolveu o pedido em laço de redirecionamento"
	case DiagSlow:
		return fmt.Sprintf("respondeu em %s, acima do limite configurado", fmtDur(res.TotalMs))
	case DiagBloqueado:
		return "a sondagem foi recusada pelo próprio painel (endereço não permitido)"
	case DiagSemResposta:
		return fmt.Sprintf("a conexão e o TLS abriram, mas a página não veio em %s (servidor ocupado ou travado)",
			fmtDur(res.TotalMs))
	}

	if strings.HasPrefix(res.Diagnosis, "down em ") {
		// "down em 2 sonda(s): central, gcloud"
		return "o alvo falhou em mais de uma sonda (" + strings.TrimPrefix(res.Diagnosis, "down em ") + ")"
	}

	// Timeout e TLS: a fase é que decide a frase.
	switch {
	case res.TLSMs > 0 && res.TTFBMs == 0:
		return fmt.Sprintf("a conexão abriu em %s, mas a camada segura (TLS) não completou em %s",
			fmtDur(res.ConnectMs), fmtDur(res.TLSMs))
	case res.Diagnosis == DiagTLS:
		return "o certificado ou o handshake TLS foi recusado"
	case res.ConnectMs == 0:
		return fmt.Sprintf("o pedido de conexão não foi respondido em %s", fmtDur(res.TotalMs))
	case res.TTFBMs == 0:
		return fmt.Sprintf("a conexão abriu, mas nenhuma resposta chegou em %s", fmtDur(res.TotalMs))
	case res.Diagnosis == DiagConnTimeout:
		return fmt.Sprintf("o alvo não respondeu dentro de %s", fmtDur(res.TotalMs))
	}
	return fmt.Sprintf("falha não classificada na sondagem (%s)", res.Diagnosis)
}

// evidenciaComTentativas é a evidência completa do disparo: a frase de sempre mais
// a lista das tentativas que falharam (hora e fase de cada uma). É o que torna o
// aviso conferível: "3 tentativas falharam" sem dizer quando e como é uma
// afirmação, não uma prova.
func evidenciaComTentativas(chk *store.SiteCheck, res Result, state string, ultimas []store.SiteCheckResultView) string {
	ev := evidenciaDaSondagem(chk, res, state)
	if state != "firing" {
		return ev
	}
	if lista := listaDeTentativas(ultimas, chk.ConsecutiveFails); lista != "" {
		return ev + " " + lista
	}
	return ev
}

// fusoBR é fixo em -03:00: sem horário de verão desde 2019, e a imagem do servidor
// não traz a base de fusos (LoadLocation falharia calado e a hora sairia em UTC).
var fusoBR = time.FixedZone("BRT", -3*60*60)

// listaDeTentativas devolve "Tentativas: 17:07:03 conexão sem resposta; …" com as
// últimas `n` falhas em ordem cronológica (o histórico chega do mais novo para o
// mais velho). Vazio quando não há falha registrada.
func listaDeTentativas(ultimas []store.SiteCheckResultView, n int) string {
	if n <= 0 {
		n = 1
	}
	var falhas []store.SiteCheckResultView
	for _, v := range ultimas {
		if v.OK {
			continue
		}
		falhas = append(falhas, v)
		if len(falhas) == n {
			break
		}
	}
	if len(falhas) == 0 {
		return ""
	}
	partes := make([]string, 0, len(falhas))
	for i := len(falhas) - 1; i >= 0; i-- {
		partes = append(partes, horaBR(falhas[i].TS)+" "+faseCurta(falhas[i]))
	}
	return "Tentativas: " + strings.Join(partes, "; ") + "."
}

// horaBR formata o TS do histórico (RFC 3339) como "15:04:05" em Brasília; se o
// texto não for uma data, devolve-o como veio, sem inventar.
func horaBR(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		if t, err = time.Parse(time.RFC3339, ts); err != nil {
			return ts
		}
	}
	return t.In(fusoBR).Format("15:04:05")
}

// faseCurta é o motivo de UMA tentativa em três ou quatro palavras, para a lista.
func faseCurta(v store.SiteCheckResultView) string {
	switch v.Diagnosis {
	case DiagSemResposta:
		return "conectou, mas a página não veio"
	case DiagDNSError:
		return "DNS não resolveu"
	case DiagConnRefused:
		return "porta fechada"
	case DiagConnError:
		return "conexão caiu"
	case DiagHTTP5xx:
		return fmt.Sprintf("HTTP %d", v.Status)
	case DiagHTTPStatus:
		return fmt.Sprintf("HTTP %d", v.Status)
	case DiagTLS:
		return "TLS recusado"
	case DiagKeywordAusent:
		return "sem a palavra-chave"
	case DiagSlow:
		return "lento"
	case DiagConnTimeout:
		if v.ConnectMs > 0 {
			return "TLS travou"
		}
		return "conexão sem resposta"
	}
	if v.Diagnosis == "" {
		return "falhou"
	}
	return v.Diagnosis
}

// fmtDur escreve a duração sem casa decimal — segundos acima de 1 s, milissegundos
// abaixo. Evita de propósito o separador decimal: a mesma frase vai para WhatsApp,
// Telegram e e-mail, e "10 s" não deixa dúvida em nenhum deles.
func fmtDur(ms float64) string {
	if ms >= 1000 {
		return fmt.Sprintf("%.0f s", ms/1000)
	}
	return fmt.Sprintf("%.0f ms", ms)
}
