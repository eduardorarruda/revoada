package copia

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/plano"
	"github.com/eduardorarruda/revoada/core/migracao/transformar"
	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
)

// Lote adaptativo: um lote que demora demais segura transação e trava; um rápido
// demais desperdiça ida e volta.
const (
	loteLento  = 4 * time.Second
	loteRapido = time.Second
	loteMinimo = 500
	loteMaximo = 50_000
)

type execucao struct {
	esp     plano.Especificacao
	r       Relator
	origem  *sql.DB
	dOrigem dialeto
	g       destino
	man     *plano.Manifesto
	total   int64
	feitas  int64
}

// Executar copia de verdade (ARQUITETURA §9.4): cada lote numa transação, com o checkpoint
// gravado na MESMA transação; pausar/retomar continua do último lote confirmado.
// Tabela que já existia no destino é carregada numa staging e só entra na tabela
// real numa troca transacional no fim (todas juntas, na ordem das FKs). O resumo leva o manifesto de rollback — inclusive quando falha.
func Executar(ctx context.Context, bruto []byte, r Relator) (any, error) {
	esp, err := lerEspecificacao(bruto)
	if err != nil {
		return nil, err
	}
	inicio := time.Now()
	passos, _, err := plano.Passos(esp)
	if err != nil {
		return nil, err
	}
	origem, err := abrirBanco(ctx, esp.Origem, r, "origem")
	if err != nil {
		return nil, err
	}
	defer origem.Close()
	dbDest, err := abrirBanco(ctx, esp.Destino, r, "destino")
	if err != nil {
		return nil, err
	}
	defer dbDest.Close()
	x := &execucao{esp: esp, r: r, origem: origem, dOrigem: novoDialeto(esp.Origem, esp.EsquemaOrigem.Dialeto),
		g: destino{db: dbDest, d: novoDialeto(esp.Destino, 0)}}
	res := &plano.ResumoExecucao{Execucao: esp.Execucao}
	defer func() {
		if x.man != nil {
			res.Manifesto = *x.man
		}
		res.DuracaoMS = time.Since(inicio).Milliseconds()
	}()

	if err := x.g.garantirControle(ctx); err != nil {
		return res, err
	}
	if err := x.preparar(ctx, passos); err != nil {
		return res, err
	}
	for _, p := range passos {
		n, err := novoLeitor(origem, x.dOrigem, p).contar(ctx)
		if err != nil {
			return res, err
		}
		x.total += n
	}
	r.Evento("executar", agentev1.EventoTarefa_INFO, fmt.Sprintf("%d tabelas, %d linhas a copiar", len(passos), x.total), 0, nil)
	for _, p := range passos {
		rt, err := x.carregar(ctx, p)
		res.Tabelas = append(res.Tabelas, rt)
		res.Linhas += rt.Gravadas
		if err != nil {
			return res, err
		}
	}
	if err := x.trocar(ctx, passos); err != nil {
		return res, err
	}
	res.Avisos = x.finalizar(ctx, passos)
	r.Evento("executar", agentev1.EventoTarefa_INFO,
		resumoFinal(res), 100,
		map[string]float64{"linhas": float64(res.Linhas)})
	return res, nil
}

// preparar cria (ou relê, ao retomar) o manifesto. Toda a preparação — CREATE das
// tabelas novas, cópia prévia e staging das que têm dados — é UMA transação junto
// com o manifesto: ou nasce tudo registrado, ou nada.
func (x *execucao) preparar(ctx context.Context, passos []plano.Passo) error {
	man, err := x.g.lerManifesto(ctx, x.g.db, x.esp.Execucao)
	if err != nil {
		return fmt.Errorf("lendo o manifesto: %w", err)
	}
	if man != nil {
		switch man.Fase {
		case "revertido":
			return fmt.Errorf("esta execução foi revertida; comece uma nova")
		case "concluido":
			return fmt.Errorf("esta execução já foi concluída")
		}
		x.man = man
		x.r.Evento("executar", agentev1.EventoTarefa_INFO, "retomando do último lote confirmado", 0, nil)
		return nil
	}
	man = &plano.Manifesto{Execucao: x.esp.Execucao, Esquema: x.g.d.esquema, Fase: "carregando"}
	tx, err := x.g.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // depois do Commit é no-op
	// o manifesto segue a ordem das FKs do DESTINO: é a da troca, e o reverter apaga
	// na ordem inversa
	for _, p := range plano.OrdemDestino(passos) {
		est, err := estrategia(ctx, x.g, p)
		if err != nil {
			return err
		}
		it := plano.ItemManifesto{Tabela: p.Destino.Nome, Estrategia: est}
		switch est {
		case plano.EstrategiaApagarTabela:
			if _, err := tx.ExecContext(ctx, plano.DDLCriarPostgres(x.g.d.esquema, p.Destino)); err != nil {
				return fmt.Errorf("criando %s: %w", p.Destino.Nome, err)
			}
		case plano.EstrategiaStaging, plano.EstrategiaEsvaziar:
			real, _ := x.g.d.tabela(p.Destino.Nome)
			if est == plano.EstrategiaStaging {
				it.Copia = nomeAuxiliar("bkp", x.esp.Execucao, p.Destino.Nome)
				bkp, _ := x.g.d.tabela(it.Copia)
				if _, err := tx.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s AS TABLE %s", bkp, real)); err != nil {
					return fmt.Errorf("cópia prévia de %s: %w", p.Destino.Nome, err)
				}
			}
			it.Staging = nomeAuxiliar("stg", x.esp.Execucao, p.Destino.Nome)
			stg, _ := x.g.d.tabela(it.Staging)
			if _, err := tx.ExecContext(ctx, fmt.Sprintf("CREATE TABLE %s (LIKE %s INCLUDING DEFAULTS)", stg, real)); err != nil {
				return fmt.Errorf("staging de %s: %w", p.Destino.Nome, err)
			}
		}
		man.Itens = append(man.Itens, it)
	}
	if err := x.g.gravarManifesto(ctx, tx, *man); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	x.man = man
	return nil
}

// alvo é onde os lotes são gravados: a staging ou a própria tabela.
func (x *execucao) alvo(p plano.Passo) string {
	if it, ok := x.man.Item(p.Destino.Nome); ok && it.Staging != "" {
		return it.Staging
	}
	return p.Destino.Nome
}

func (x *execucao) carregar(ctx context.Context, p plano.Passo) (rt plano.ResumoTabela, err error) {
	inicio := time.Now()
	rt = plano.ResumoTabela{Origem: p.Origem.Nome, Destino: p.Destino.Nome}
	defer func() { rt.DuracaoMS = time.Since(inicio).Milliseconds() }()
	alvo := x.alvo(p)
	cp, err := x.g.lerCheckpoint(ctx, x.esp.Execucao, p.Destino.Nome)
	if err != nil {
		return rt, err
	}
	dest := colunasDestino(p)
	iSoma := colunaSoma(p)
	casas, err := x.g.casasTempo(ctx, alvo)
	if err != nil {
		return rt, err
	}
	conf := novaConferencia(p, dest, casas)
	if cp.concluida {
		rt.Linhas, rt.Gravadas, rt.ChecksumOK = cp.linhas, cp.linhas, cp.soma != somaInvalida
		x.feitas += cp.linhas
		vereditoDoCheckpoint(&rt, cp, conf)
		return rt, nil
	}
	cont := novasSomas(len(dest))
	if cp.linhas > 0 {
		anterior, _, errConteudo := decodificarSomas(cp.conteudo, conf)
		switch {
		case p.Map.ColunaLote == "":
			// Sem coluna de lote não dá para saber onde parou: recomeça a tabela. É seguro
			// porque o alvo só tem linhas desta execução (criada, vazia ou staging).
			if err := x.recomecar(ctx, p, alvo); err != nil {
				return rt, err
			}
			cp = checkpoint{}
		case errConteudo != nil:
			// Checkpoint de um agente que ainda não somava o conteúdo: continuar daqui
			// deixaria as primeiras linhas sem conferência. Recomeça a tabela (mesmo
			// motivo de segurança acima) — nunca um "conteúdo conferido" falso.
			x.r.Evento("executar", agentev1.EventoTarefa_AVISO, fmt.Sprintf(
				"%s: o checkpoint não tem a soma do conteúdo (agente anterior); recomeçando a tabela para conferir o conteúdo", p.Destino.Nome), pct(x.feitas, x.total), nil)
			if err := x.recomecar(ctx, p, alvo); err != nil {
				return rt, err
			}
			cp = checkpoint{}
		default:
			cont = anterior
		}
	}
	l := novoLeitor(x.origem, x.dOrigem, p)
	defer l.fechar()
	l.retomarDe(cp.chave)
	s, linhas, lote := lerSoma(cp.soma), cp.linhas, x.esp.Lote
	conferirSoma := cp.soma != somaInvalida
	x.feitas += linhas
	for {
		if err := x.r.Pausa(ctx); err != nil {
			return rt, err
		}
		t0 := time.Now()
		ls, err := l.proximo(ctx, lote)
		if err != nil {
			return rt, err
		}
		if len(ls) == 0 {
			break
		}
		vals := make([][]any, len(ls))
		somaLote, contLote := s, cont.clonar()
		for i, ln := range ls {
			v, _, prob := converter(p, dest, ln)
			if prob != nil {
				return rt, fmt.Errorf("%s, linha de chave %s: %s (a simulação não viu isso: o dado mudou? rode a simulação de novo)",
					p.Origem.Nome, l.chaveDe(ln), prob.vio.Mensagem)
			}
			vals[i] = v
			if v[iSoma] == transformar.UsarPadrao {
				conferirSoma = false // o valor nasce no destino: confere só a contagem
			}
			somaLote.add(chaveNormal(v[iSoma], dest[iSoma]))
			contLote.add(conf, v)
		}
		novo := checkpoint{chave: l.chaveDe(ls[len(ls)-1]), linhas: linhas + int64(len(ls)), soma: somaLote.String(),
			conteudo: contLote.codificar(conf, false)}
		if !conferirSoma {
			novo.soma = somaInvalida
		}
		if p.Map.ColunaLote == "" {
			novo.chave = ""
		}
		if err := x.gravarLote(ctx, p, alvo, dest, vals, novo); err != nil {
			return rt, err
		}
		s, cont, linhas = somaLote, contLote, novo.linhas
		x.feitas += int64(len(ls))
		x.r.Checkpoint(p.Destino.Nome, novo.chave, linhas)
		x.r.Evento("executar", agentev1.EventoTarefa_INFO,
			fmt.Sprintf("%s → %s: %d linhas gravadas", p.Origem.Nome, p.Destino.Nome, linhas), pct(x.feitas, x.total),
			map[string]float64{"linhas": float64(linhas), "lote": float64(len(ls))})
		lote = ajustarLote(lote, time.Since(t0))
	}
	rt.Linhas, rt.Gravadas = linhas, linhas
	if ganchoAntesDeReler != nil {
		ganchoAntesDeReler(ctx, x.g.db, alvo, p.Destino.Nome)
	}
	// Validação depois de tudo: relê o alvo UMA vez e confere contagem, soma das
	// chaves e a soma do conteúdo de cada coluna contra o que foi gravado.
	n, sDest, relido, err := x.g.reler(ctx, alvo, dest, iSoma, conf)
	if err != nil {
		return rt, fmt.Errorf("conferindo %s: %w", p.Destino.Nome, err)
	}
	if n != linhas || (conferirSoma && sDest != s) {
		return rt, fmt.Errorf("%s: a conferência não bateu (gravadas %d, no destino %d); nada foi trocado — reverta e investigue", p.Destino.Nome, linhas, n)
	}
	rt.ChecksumOK = conferirSoma
	veredito(&rt, conf, cont, linhas)
	if div := colunasDivergentes(conf, cont, relido); len(div) > 0 {
		rt.ConteudoOK = false
		return rt, x.conteudoDivergente(ctx, p, alvo, conf, dest, div, &rt)
	}
	fim := checkpoint{chave: "", linhas: linhas, soma: s.String(), concluida: true, conteudo: cont.codificar(conf, true)}
	if !conferirSoma {
		fim.soma = somaInvalida
	}
	return rt, x.g.gravarCheckpoint(ctx, x.g.db, x.esp.Execucao, p.Destino.Nome, fim)
}

// ganchoAntesDeReler só existe para os testes de integração: mexe no alvo entre a
// carga e a releitura (simula um destino que guardou outra coisa). nil em produção.
var ganchoAntesDeReler func(ctx context.Context, db *sql.DB, alvo, tabela string)

// veredito preenche o resultado da conferência de conteúdo de uma tabela.
func veredito(rt *plano.ResumoTabela, conf conferencia, cont somasConteudo, linhas int64) {
	rt.ColunasConferidas, rt.ColunasNaoConferidas = colunasDoVeredito(conf, cont)
	switch {
	case cont.chaveInvalida:
		rt.ConteudoOK, rt.ConteudoMotivo = false, motivoChaveInvalida
	case len(rt.ColunasConferidas) == 0 && linhas > 0:
		rt.ConteudoOK, rt.ConteudoMotivo = false, "nenhuma coluna desta tabela pôde ser comparada (veja os motivos por coluna)"
	default:
		rt.ConteudoOK = true
	}
}

// vereditoDoCheckpoint refaz o veredito de uma tabela já concluída (retomada).
// Checkpoint sem conteúdo (agente antigo) NUNCA vira conteúdo conferido.
func vereditoDoCheckpoint(rt *plano.ResumoTabela, cp checkpoint, conf conferencia) {
	cont, conferido, err := decodificarSomas(cp.conteudo, conf)
	if err != nil || !conferido {
		rt.ConteudoOK = false
		rt.ConteudoMotivo = "tabela carregada por uma versão anterior do agente, que só conferia contagem e chaves: " +
			"rode “Verificar de novo” depois da conclusão para comparar o conteúdo"
		return
	}
	veredito(rt, conf, cont, cp.linhas)
}

// conteudoDivergente monta o erro (e a amostra no resumo) quando a soma do conteúdo
// relido não bate: compara linha a linha para dizer QUAIS linhas e colunas.
func (x *execucao) conteudoDivergente(ctx context.Context, p plano.Passo, alvo string, conf conferencia, dest []esquema.Coluna,
	cols []string, rt *plano.ResumoTabela) error {
	msg := fmt.Sprintf("%s: o conteúdo relido do destino não confere com o que foi gravado nas colunas %s; nada foi trocado — reverta e investigue",
		p.Destino.Nome, strings.Join(cols, ", "))
	if len(conf.chave) == 0 {
		return errors.New(msg + " (a tabela não tem chave: não dá para apontar as linhas)")
	}
	x.r.Evento("executar", agentev1.EventoTarefa_AVISO, fmt.Sprintf("%s: conteúdo divergente nas colunas %s; localizando as linhas",
		p.Destino.Nome, strings.Join(cols, ", ")), pct(x.feitas, x.total), nil)
	cmp := comparador{origem: x.origem, dOrigem: x.dOrigem, g: x.g, lote: x.esp.Lote, r: x.r, pararNaAmostra: true}
	vt, err := cmp.tabela(ctx, p, alvo, conf, dest, nil)
	if err != nil {
		return fmt.Errorf("%s (e a localização das linhas falhou: %v)", msg, err)
	}
	rt.Divergencias = vt.Divergencias
	chaves := make([]string, 0, len(vt.Divergencias))
	for _, d := range vt.Divergencias {
		chaves = append(chaves, d.Chave)
	}
	if len(chaves) == 0 {
		return errors.New(msg + " (a comparação linha a linha não achou a diferença: o dado da origem mudou durante a carga?)")
	}
	return fmt.Errorf("%s. Divergências na %s%s", msg, plano.MarcadorChave, strings.Join(chaves, ", "))
}

func ajustarLote(lote int, dur time.Duration) int {
	switch {
	case dur > loteLento && lote > loteMinimo:
		return max(lote/2, loteMinimo)
	case dur < loteRapido && lote < loteMaximo:
		return min(lote*2, loteMaximo)
	}
	return lote
}

func (x *execucao) gravarLote(ctx context.Context, p plano.Passo, alvo string, dest []esquema.Coluna, vals [][]any, cp checkpoint) error {
	tx, err := x.g.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if err := x.g.inserir(ctx, tx, alvo, dest, vals); err != nil {
		return err
	}
	if err := x.g.gravarCheckpoint(ctx, tx, x.esp.Execucao, p.Destino.Nome, cp); err != nil {
		return err
	}
	return tx.Commit()
}

func (x *execucao) recomecar(ctx context.Context, p plano.Passo, alvo string) error {
	t, err := x.g.d.tabela(alvo)
	if err != nil {
		return err
	}
	tx, err := x.g.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	if _, err := tx.ExecContext(ctx, "DELETE FROM "+t); err != nil {
		return err
	}
	if err := x.g.gravarCheckpoint(ctx, tx, x.esp.Execucao, p.Destino.Nome, checkpoint{}); err != nil {
		return err
	}
	return tx.Commit()
}

// trocar move as stagings para as tabelas reais numa transação só, na ordem das FKs.
func (x *execucao) trocar(ctx context.Context, passos []plano.Passo) error {
	tx, err := x.g.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck
	novo := *x.man      // só vale se a transação confirmar
	novo.Itens = append([]plano.ItemManifesto(nil), x.man.Itens...)
	houve := false
	for _, p := range plano.OrdemDestino(passos) { // pais antes de filhos, pelas FKs do destino
		it, ok := novo.Item(p.Destino.Nome)
		if !ok || it.Staging == "" || it.Trocada {
			continue
		}
		real, _ := x.g.d.tabela(p.Destino.Nome)
		stg, _ := x.g.d.tabela(it.Staging)
		cols := make([]string, 0, len(p.Map.Colunas))
		for _, mc := range p.Map.Colunas {
			c, err := x.g.d.id(mc.ColunaDestino)
			if err != nil {
				return err
			}
			cols = append(cols, c)
		}
		lista := strings.Join(cols, ", ")
		if _, err := tx.ExecContext(ctx, fmt.Sprintf("INSERT INTO %s (%s) OVERRIDING SYSTEM VALUE SELECT %s FROM %s",
			real, lista, lista, stg)); err != nil {
			return fmt.Errorf("trocando a staging de %s (nada foi alterado na tabela real): %w", p.Destino.Nome, err)
		}
		it.Trocada, houve = true, true
	}
	if !houve {
		return nil
	}
	novo.Fase = "trocado"
	if err := x.g.gravarManifesto(ctx, tx, novo); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	x.man = &novo
	x.r.Evento("executar", agentev1.EventoTarefa_INFO, "stagings trocadas pelas tabelas reais (uma transação)", 99, nil)
	return nil
}

// resumoFinal é a última linha da tela ao vivo: o que foi conferido, sem rodeio.
func resumoFinal(res *plano.ResumoExecucao) string {
	colunas, fora, semConteudo := 0, 0, 0
	for _, t := range res.Tabelas {
		colunas += len(t.ColunasConferidas)
		fora += len(t.ColunasNaoConferidas)
		if !t.ConteudoOK {
			semConteudo++
		}
	}
	msg := fmt.Sprintf("migração concluída: %d linhas em %d tabelas; contagem, chaves e conteúdo conferidos (%d colunas idênticas entre o gravado e o relido)",
		res.Linhas, len(res.Tabelas), colunas)
	if fora > 0 {
		msg += fmt.Sprintf("; %d colunas não puderam ser comparadas (veja o motivo por tabela)", fora)
	}
	if semConteudo > 0 {
		msg += fmt.Sprintf("; %d tabelas sem conferência de conteúdo", semConteudo)
	}
	return msg
}
