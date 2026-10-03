package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Download, verificação e estágio do binário novo.
//
// A ordem aqui é a política inteira, e ela é rígida: baixa para um temporário,
// confere o SHA-256, confere que o binário EXECUTA e diz ser a versão prometida,
// e só então o move para o nome que o promotor root procura. Enquanto qualquer
// um desses passos não passa, o arquivo não tem o nome que faz o systemd
// promovê-lo — o defeito nunca chega a /usr/local/bin.

const (
	// tamanhoMaximo é o teto absoluto do download. O binário do agente é da ordem
	// de dezenas de MB; 256 MB é folga larga e ao mesmo tempo impede que uma
	// resposta errada (redirect para um ISO, um proxy que devolve outra coisa)
	// encha o disco do host monitorado — encher o disco de um servidor de cliente
	// é dano pior do que ficar desatualizado.
	tamanhoMaximo = 256 << 20
	// timeoutDownload cobre link ruim sem prender a goroutine para sempre.
	timeoutDownload = 15 * time.Minute
	// timeoutSemProgresso é o prazo de SILÊNCIO tolerado no meio do corpo.
	//
	// Só havia o prazo total acima, e prazo total não descreve o defeito real: uma
	// conexão que trava depois do primeiro byte (rota que sumiu, NAT que esqueceu a
	// sessão, proxy que segura a resposta) segura o atualizador os 15 MINUTOS
	// inteiros — e enquanto isso um `revoada-agent.novo.parcial` de até 256 MB
	// fica ocupando o disco do servidor do cliente. Encher o disco de um host
	// monitorado é dano maior do que ficar uma versão atrás, que é o princípio de
	// todo este pacote.
	//
	// 60s é folgado para um link ruim de verdade (o teto por ciclo continua sendo o
	// total) e curto o bastante para o `.parcial` ser apagado e a tentativa recomeçar
	// dentro do mesmo intervalo de consulta.
	timeoutSemProgresso = 60 * time.Second
	// timeoutVersao/timeoutDoctor limitam a checagem de sanidade. Um binário que
	// trava ao responder `version` está tão quebrado quanto um que estoura, e
	// travar aqui prenderia a verificação indefinidamente.
	timeoutVersao = 30 * time.Second
	timeoutDoctor = 90 * time.Second
)

// baixarEEstagiar devolve o caminho do binário estagiado e verificado.
func (a *Atualizador) baixarEEstagiar(ctx context.Context, r Resposta) (string, error) {
	destino := filepath.Join(a.cfg.Dir, arqNovo)

	// Já temos este exato binário estagiado de uma tentativa anterior? Então não
	// baixamos de novo. Não é otimização: é o que impede um host cujo promotor
	// está lento (ou cujo reinício foi adiado) de rebaixar o mesmo artefato a cada
	// hora — vezes o tamanho da frota, contra o mesmo painel.
	if err := conferirArquivo(destino, r.SHA256); err == nil {
		if err := a.conferirSanidade(ctx, destino, r.Versao); err == nil {
			if err := a.gravarMeta(r); err != nil {
				return "", err
			}
			return destino, nil
		}
	}

	tmp := destino + ".parcial"
	// Um parcial de tentativa anterior nunca é reaproveitado: sem retomada por
	// range, continuar um arquivo truncado só produziria um checksum errado
	// depois de gastar a banda inteira de novo.
	_ = os.Remove(tmp)

	soma, tamanho, err := a.baixar(ctx, r, tmp)
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}

	// A verificação obrigatória. Checksum divergente é abortar e registrar —
	// nunca instalar, nunca "tentar assim mesmo". Um artefato meio escrito, um
	// dist trocado no meio do deploy e um binário adulterado no caminho são
	// indistinguíveis daqui, e todos os três terminam num agente que não é o que
	// o painel acha que é.
	if !strings.EqualFold(soma, r.SHA256) {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("SHA-256 não confere: esperado %s, baixado %s (%d bytes) — nada foi instalado", r.SHA256, soma, tamanho)
	}

	// Executável só DEPOIS do checksum: um arquivo em quarentena não deve ser
	// executável enquanto ainda pode ser lixo.
	if err := os.Chmod(tmp, 0o755); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("dando permissão de execução: %w", err)
	}

	if err := a.conferirSanidade(ctx, tmp, r.Versao); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}

	// Renomeia por último. Até esta linha o promotor root não enxerga nada: o
	// nome que ele procura só passa a existir quando o conteúdo já foi conferido
	// byte a byte e o binário já provou que roda.
	if err := os.Rename(tmp, destino); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("estagiando o binário: %w", err)
	}
	if err := a.gravarMeta(r); err != nil {
		return "", err
	}
	return destino, nil
}

// baixar escreve o corpo em `destino` e devolve o SHA-256 em hexadecimal.
// Hash e escrita saem do MESMO fluxo de bytes (io.TeeReader): reler o arquivo
// para calcular o hash deixaria uma janela entre o que foi verificado e o que
// está no disco.
func (a *Atualizador) baixar(ctx context.Context, r Resposta, destino string) (string, int64, error) {
	base, err := a.baseValida()
	if err != nil {
		return "", 0, err
	}

	ctx, cancel := context.WithTimeout(ctx, timeoutDownload)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, r.URL, nil)
	if err != nil {
		return "", 0, err
	}
	// Este cabeçalho é a IDENTIDADE deste servidor no painel. Ele só pode sair daqui
	// para a origem do panel_url — ver clientePainel/CheckRedirect em httpcli.go, que
	// é o que impede um 302 de entregá-lo a outro host.
	req.Header.Set("X-Revoada-Key", a.cfg.Key)

	resp, err := clientePainel(base).Do(req)
	if err != nil {
		return "", 0, fmt.Errorf("baixando %s: %w", r.URL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", 0, fmt.Errorf("download respondeu %d", resp.StatusCode)
	}

	f, err := os.OpenFile(destino, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return "", 0, err
	}
	// Rede de segurança para os caminhos de ERRO. O caminho de sucesso fecha
	// explicitamente logo abaixo e confere o resultado: aqui o arquivo está sendo
	// ESCRITO, e um Close que falha (disco cheio, erro de E/S) deixaria em disco um
	// binário incompleto enquanto o checksum — calculado do fluxo lido, não do que foi
	// gravado — continuaria batendo. O segundo Close do defer devolve erro e é
	// deliberadamente ignorado.
	defer func() { _ = f.Close() }()

	h := sha256.New()
	// Teto de leitura com UM byte de folga sobre o tamanho anunciado: se vier mais
	// que isso, a resposta não é o artefato que o painel descreveu e paramos sem
	// gravar o excedente. Sem o teto, um Content-Length mentiroso encheria o disco.
	limite := r.Tamanho
	if limite <= 0 || limite > tamanhoMaximo {
		limite = tamanhoMaximo
	}
	// Vigia de progresso: cancela a requisição se o corpo parar de andar. Ver
	// timeoutSemProgresso.
	vig := novoVigia(cancel)
	defer vig.parar()
	n, err := io.Copy(f, io.TeeReader(io.LimitReader(vig.envolver(resp.Body), limite+1), h))
	if err != nil {
		if vig.travou() {
			return "", n, fmt.Errorf("o download parou de progredir por mais de %s (%d bytes recebidos) — abortado para não segurar o disco com um arquivo parcial", timeoutSemProgresso, n)
		}
		return "", n, fmt.Errorf("gravando o download: %w", err)
	}
	if n > limite {
		return "", n, fmt.Errorf("o download passou do tamanho anunciado (%d bytes)", r.Tamanho)
	}
	if n != r.Tamanho {
		return "", n, fmt.Errorf("tamanho divergente: painel anunciou %d bytes, chegaram %d", r.Tamanho, n)
	}
	if err := f.Sync(); err != nil {
		return "", n, fmt.Errorf("sincronizando o download em disco: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", n, fmt.Errorf("fechando o download: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

// conferirSanidade exige que o binário baixado RODE e se identifique como a
// versão prometida.
//
// Checksum prova integridade, não funcionamento: um artefato íntegro compilado
// para a arquitetura errada, ligado a uma libc que este host não tem, ou
// truncado ainda na origem passa no SHA-256 e não executa. Promover um binário
// assim troca "host desatualizado" por "host que não sobe" — com Restart=always,
// vira um restart-loop que só termina com alguém entrando por SSH, que é
// exatamente o que este pacote existe para não precisar.
func (a *Atualizador) conferirSanidade(ctx context.Context, caminho, esperada string) error {
	ctxV, cancel := context.WithTimeout(ctx, timeoutVersao)
	defer cancel()

	saida, err := exec.CommandContext(ctxV, caminho, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("o binário baixado não executou (%v): %s", err, primeiraLinha(saida))
	}
	obtida := versaoDaSaida(string(saida))
	if obtida == "" {
		return fmt.Errorf("o binário baixado não respondeu uma versão reconhecível: %q", primeiraLinha(saida))
	}
	if obtida != strings.TrimSpace(esperada) {
		return fmt.Errorf("o binário baixado diz ser %q, mas o painel anunciou %q — não vou promovê-lo", obtida, esperada)
	}

	// `doctor` é uma segunda opinião, não um veto.
	//
	// Ele exercita config, coleta e rede de verdade, então um binário que estoura
	// ao fazer trabalho real é pego aqui. Mas ele também sai != 0 por motivos que
	// não têm nada a ver com o binário — gateway fora do ar, chave revogada,
	// buffer com lote pendente. Tratar isso como veto travaria a frota inteira
	// justamente durante um incidente de rede. Por isso só a FALHA DE EXECUÇÃO
	// (não conseguiu rodar, morreu por sinal) conta; código de saída != 0 vira log.
	if a.cfg.ConfigPath == "" {
		return nil
	}
	ctxD, cancelD := context.WithTimeout(ctx, timeoutDoctor)
	defer cancelD()
	saidaD, errD := exec.CommandContext(ctxD, caminho, "-config", a.cfg.ConfigPath, "doctor").CombinedOutput()
	var saiu *exec.ExitError
	switch {
	case errD == nil:
		// tudo certo
	case errors.As(errD, &saiu):
		a.log.Info("auto-atualização: o binário novo rodou o doctor com queixas (não impede a troca)", "versao", esperada, "saida", primeiraLinha(saidaD))
	default:
		return fmt.Errorf("o binário baixado não conseguiu rodar o doctor: %w", errD)
	}
	return nil
}

// versaoDaSaida extrai o número de "revoada-agent 0.9.0".
func versaoDaSaida(s string) string {
	campos := strings.Fields(strings.TrimSpace(s))
	if len(campos) == 0 {
		return ""
	}
	cand := campos[len(campos)-1]
	if _, err := parseVersao(cand); err != nil {
		return ""
	}
	return cand
}

func primeiraLinha(b []byte) string {
	s := strings.TrimSpace(string(b))
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// conferirArquivo recalcula o SHA-256 de um arquivo já em disco.
func conferirArquivo(caminho, esperado string) error {
	f, err := os.Open(caminho)
	if err != nil {
		return err
	}
	// Só leitura: um Close que falhe não muda o checksum já calculado.
	defer func() { _ = f.Close() }()
	h := sha256.New()
	if _, err := io.Copy(h, io.LimitReader(f, tamanhoMaximo)); err != nil {
		return err
	}
	if soma := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(soma, esperado) {
		return fmt.Errorf("SHA-256 divergente")
	}
	return nil
}

// gravarMeta escreve o arquivo que o promotor root lê.
//
// Formato de duas linhas `chave=valor` em vez de JSON porque quem consome é um
// script /bin/sh rodando como root no boot do serviço: `grep`+`cut` não tem
// dependência, não tem parser para dar errado e não precisa de um interpretador
// que pode não estar instalado. Menos código rodando como root é menos código
// que pode falhar como root.
func (a *Atualizador) gravarMeta(r Resposta) error {
	conteudo := fmt.Sprintf("sha256=%s\nversao=%s\n", strings.ToLower(r.SHA256), r.Versao)
	return gravarAtomico(filepath.Join(a.cfg.Dir, arqMeta), []byte(conteudo), 0o644)
}
