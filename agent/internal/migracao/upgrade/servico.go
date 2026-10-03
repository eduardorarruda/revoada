// Package upgrade roda o upgrade de versão do Firebird no agente (ARQUITETURA §9.5):
// diagnóstico (só leitura + ensaio de metadados no FB5), upgrade (backup pelo
// serviço da versão antiga → restore pelo serviço do FB5 → validação) e descarte do
// banco novo. O banco original NUNCA é alterado.
package upgrade

import (
	"context"
	"fmt"
	"net"
	"strings"

	"github.com/eduardorarruda/revoada/agent/internal/migracao/copia"
	"github.com/eduardorarruda/revoada/core/migracao/plano"
	fb "github.com/nakagami/firebirdsql"
)

// Relator é o mesmo do motor de cópia (eventos, pausa, credenciais).
type Relator = copia.Relator

// Constantes do Services API (consts_pub.h do Firebird). O driver não expõe o
// -FIX_FSS do gbak; montamos o pedido do restore aqui.
const (
	acaoBackup  = 1
	acaoRestore = 2

	spbDbname   = 106
	spbVerbose  = 107
	spbOptions  = 108
	spbBkpFile  = 5
	spbPageSize = 10
	spbParallel = 21 // isc_spb_res_parallel_workers (FB5)
	spbFixData  = 13 // isc_spb_res_fix_fss_data
	spbFixMeta  = 14 // isc_spb_res_fix_fss_metadata

	bkpMetadataOnly = 0x04
	bkpNoGarbage    = 0x08
	resCreate       = 0x2000 // NUNCA replace: o banco novo não pode existir
)

// endereco põe a porta padrão do Firebird quando falta.
func endereco(b plano.Banco) string {
	if _, _, err := net.SplitHostPort(b.Endereco); err != nil {
		return b.Endereco + ":3050"
	}
	return b.Endereco
}

func opcoesServico(b plano.Banco) fb.ServiceManagerOptions {
	o := fb.GetDefaultServiceManagerOptions().WithoutWireCrypt()
	if strings.EqualFold(b.Opcoes["wire_crypt"], "true") {
		o = o.WithWireCrypt()
	}
	return o
}

// servico executa um pedido do Services API e entrega as linhas de saída (o
// "verbose" do gbak) a `linha`. O gbak pelo serviço termina "com sucesso" mesmo
// quando falha: quem decide é a saída — linha com "ERROR" vira erro.
func servico(ctx context.Context, b plano.Banco, senha string, spb []byte, linha func(string)) error {
	sm, err := fb.NewServiceManagerContext(ctx, endereco(b), b.Usuario, senha, opcoesServico(b))
	if err != nil {
		return fmt.Errorf("conectando ao serviço do Firebird em %s: %w", b.Endereco, err)
	}
	defer sm.Close()
	if err := sm.ServiceStartContext(ctx, spb); err != nil {
		return err
	}
	saida := make(chan string, 64)
	fim := make(chan error, 1)
	go func() {
		fim <- sm.WaitStringsContext(ctx, saida)
		close(saida)
	}()
	var erros []string
	for l := range saida {
		for _, s := range strings.Split(strings.TrimRight(l, "\n"), "\n") {
			if s = strings.TrimSpace(s); s == "" {
				continue
			}
			if strings.Contains(strings.ToUpper(s), "ERROR") {
				erros = append(erros, s)
			}
			if linha != nil {
				linha(s)
			}
		}
	}
	if err := <-fim; err != nil {
		return err
	}
	if len(erros) > 0 {
		return &ErroServico{Linhas: erros}
	}
	return nil
}

// ErroServico junta as linhas de erro do gbak.
type ErroServico struct{ Linhas []string }

func (e *ErroServico) Error() string {
	const maximo = 5
	ls := e.Linhas
	if len(ls) > maximo {
		ls = append(ls[:maximo:maximo], fmt.Sprintf("… e mais %d", len(e.Linhas)-maximo))
	}
	return "gbak: " + strings.Join(ls, " | ")
}

func spbBackup(banco, arquivo string, soMetadados bool) []byte {
	opcoes := int32(bkpNoGarbage) // o original não é tocado: nem a coleta de lixo roda
	if soMetadados {
		opcoes |= bkpMetadataOnly
	}
	w := fb.NewXPBWriterFromTag(acaoBackup)
	w.PutString(spbDbname, banco)
	w.PutString(spbBkpFile, arquivo)
	w.PutInt32(spbOptions, opcoes)
	w.PutTag(spbVerbose)
	return w.Bytes()
}

func spbRestore(arquivo, novo, charsetFix string, workers int) []byte {
	w := fb.NewXPBWriterFromTag(acaoRestore)
	w.PutString(spbDbname, novo)
	w.PutString(spbBkpFile, arquivo)
	w.PutInt32(spbOptions, resCreate)
	w.PutTag(spbVerbose)
	if workers > 1 {
		w.PutInt32(spbParallel, int32(workers))
	}
	if charsetFix != "" {
		w.PutString(spbFixData, charsetFix)
		w.PutString(spbFixMeta, charsetFix)
	}
	return w.Bytes()
}

// juntar monta o caminho no servidor (o separador segue o diretório informado).
func juntar(dir, nome string) string {
	if strings.Contains(dir, `\`) && !strings.Contains(dir, "/") {
		return strings.TrimRight(dir, `\`) + `\` + nome
	}
	return strings.TrimRight(dir, "/") + "/" + nome
}
