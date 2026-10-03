package copia

import (
	"context"
	"database/sql"
	"fmt"
	"hash/fnv"
	"time"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/plano"
	"github.com/eduardorarruda/revoada/core/migracao/transformar"
	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
)

// fatorGravacao: gravar costuma custar ~2x ler (índices, WAL). Só para a estimativa.
const fatorGravacao = 2

// Simular é o dry-run (ARQUITETURA §9.4): lê a origem, aplica as transformações e confere
// tudo contra o destino — tipos, tamanhos, NOT NULL, chave única e chave estrangeira
// — SEM GRAVAR NADA. Na origem e no destino só roda SELECT.
func Simular(ctx context.Context, bruto []byte, r Relator) (any, error) {
	esp, err := lerEspecificacao(bruto)
	if err != nil {
		return nil, err
	}
	inicio := time.Now()
	passos, ciclos, err := plano.Passos(esp)
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

	s := &simulacao{esp: esp, r: r, origem: origem, dOrigem: novoDialeto(esp.Origem, esp.EsquemaOrigem.Dialeto),
		g: destino{db: dbDest, d: novoDialeto(esp.Destino, 0)}, referenciadas: map[string]map[string]bool{},
		carregadas: map[string]map[string]map[uint64]struct{}{}}
	for _, p := range passos { // colunas de pai apontadas por FK de uma coluna de alguma filha
		for _, fk := range fksSimples(p, colunasDestino(p)) {
			if s.referenciadas[fk.pai] == nil {
				s.referenciadas[fk.pai] = map[string]bool{}
			}
			s.referenciadas[fk.pai][fk.colunaPai] = true
		}
	}
	rel := &plano.RelatorioSimulacao{HashMapeamento: esp.HashMapeamento, Ciclos: ciclos}
	totais := make([]int64, len(passos))
	for i, p := range passos {
		rel.Ordem = append(rel.Ordem, p.Origem.Nome)
		if totais[i], err = novoLeitor(origem, s.dOrigem, p).contar(ctx); err != nil {
			return rel, err
		}
		s.total += totais[i]
	}
	r.Evento("simular", agentev1.EventoTarefa_INFO,
		fmt.Sprintf("%d tabelas, %d linhas a conferir — nada será gravado", len(passos), s.total), 0, nil)
	var leitura time.Duration
	for i, p := range passos {
		rt, dur, err := s.tabela(ctx, p, totais[i])
		leitura += dur
		rel.Tabelas = append(rel.Tabelas, rt)
		rel.TotalLinhas += rt.Linhas
		rel.Bloqueantes += rt.ComProblema
		for _, n := range rt.Perdas {
			rel.Perdas += n
		}
		if err != nil {
			return rel, err
		}
	}
	rel.DuracaoMS = time.Since(inicio).Milliseconds()
	rel.TempoEstimadoS = int64(leitura.Seconds()*fatorGravacao) + 1
	nivel, msg := agentev1.EventoTarefa_INFO, fmt.Sprintf("simulação concluída: %d linhas, nenhuma seria recusada", rel.TotalLinhas)
	if rel.Bloqueantes > 0 {
		nivel, msg = agentev1.EventoTarefa_AVISO, fmt.Sprintf("simulação concluída: %d de %d linhas seriam recusadas — corrija o mapeamento", rel.Bloqueantes, rel.TotalLinhas)
	}
	r.Evento("simular", nivel, msg, 100, map[string]float64{"linhas": float64(rel.TotalLinhas), "bloqueantes": float64(rel.Bloqueantes)})
	return rel, nil
}

type simulacao struct {
	esp     plano.Especificacao
	r       Relator
	origem  *sql.DB
	dOrigem dialeto
	g       destino
	total   int64
	feitas  int64
	// referenciadas: tabela de destino → colunas apontadas por FK de uma coluna.
	referenciadas map[string]map[string]bool
	// carregadas: tabela → coluna referenciada → hash dos valores que a migração vai
	// gravar (as filhas conferem a FK contra isto e contra o destino). Vale para PK
	// composta também: o que importa é a coluna que a FK aponta.
	carregadas map[string]map[string]map[uint64]struct{}
}

func hashChave(s string) uint64 {
	h := fnv.New64a()
	_, _ = h.Write([]byte(s))
	return h.Sum64()
}

// linhaOK é uma linha que passou nas conferências por valor e ainda vai passar
// pelas conferências de conjunto (PK já existente no destino, FK).
type linhaOK struct {
	chave string
	pk    string
	fks   []string          // valor da coluna de cada FK simples (vazio = nulo/sem FK)
	refs  map[string]string // valor das colunas desta tabela que alguma filha referencia
}

// fkSimples é uma FK de uma coluna cuja coluna está mapeada.
type fkSimples struct {
	idx       int // posição em p.Map.Colunas
	coluna    esquema.Coluna
	pai       string
	colunaPai string
}

func fksSimples(p plano.Passo, dest []esquema.Coluna) []fkSimples {
	if p.Criar {
		return nil // nasce sem FK; as FKs são religadas no fim da execução
	}
	var out []fkSimples
	for _, fk := range p.Destino.Estrangeiras {
		if len(fk.Colunas) != 1 || len(fk.ColunasRef) != 1 {
			continue
		}
		for i, mc := range p.Map.Colunas {
			if mc.ColunaDestino == fk.Colunas[0] {
				out = append(out, fkSimples{idx: i, coluna: dest[i], pai: fk.TabelaRef, colunaPai: fk.ColunasRef[0]})
			}
		}
	}
	return out
}

func indicesPK(p plano.Passo) []int {
	var idx []int
	for _, c := range p.Destino.ChavePrimaria {
		achou := false
		for i, mc := range p.Map.Colunas {
			if mc.ColunaDestino == c {
				idx, achou = append(idx, i), true
			}
		}
		if !achou {
			return nil // a PK não é toda gravada pela migração: não dá para conferir
		}
	}
	return idx
}

func (s *simulacao) tabela(ctx context.Context, p plano.Passo, total int64) (plano.RelatorioTabela, time.Duration, error) {
	inicio := time.Now()
	rt := plano.RelatorioTabela{Origem: p.Origem.Nome, Destino: p.Destino.Nome, Acao: string(p.Map.Acao),
		Violacoes: map[string]int64{}, Perdas: map[string]int64{}}
	est, err := estrategia(ctx, s.g, p)
	if err != nil {
		return rt, 0, err
	}
	rt.Estrategia = est
	dest := colunasDestino(p)
	pkIdx := indicesPK(p)
	fks := fksSimples(p, dest)
	vistas := map[uint64]struct{}{}
	l := novoLeitor(s.origem, s.dOrigem, p)
	defer l.fechar()
	var leitura time.Duration
	anotar := func(chave, coluna, tipo, msg string) {
		rt.ComProblema++
		rt.Violacoes[tipo]++
		if len(rt.Amostras) < s.esp.Amostras {
			rt.Amostras = append(rt.Amostras, plano.Amostra{Chave: chave, Coluna: coluna, Tipo: tipo, Mensagem: msg})
		}
	}
	for {
		if err := s.r.Pausa(ctx); err != nil {
			return rt, leitura, err
		}
		t0 := time.Now()
		lote, err := l.proximo(ctx, s.esp.Lote)
		leitura += time.Since(t0)
		if err != nil {
			return rt, leitura, err
		}
		if len(lote) == 0 {
			break
		}
		var oks []linhaOK
		for _, ln := range lote {
			rt.Linhas++
			chave := l.chaveDe(ln)
			vals, perdas, prob := converter(p, dest, ln)
			if prob != nil {
				anotar(chave, prob.coluna, prob.vio.Tipo, prob.vio.Mensagem)
				continue
			}
			for _, pe := range perdas {
				rt.Perdas[pe]++
			}
			ok := linhaOK{chave: chave}
			if len(pkIdx) > 0 {
				for _, i := range pkIdx {
					ok.pk += chaveNormal(vals[i], dest[i]) + "\x1f"
				}
				h := hashChave(ok.pk)
				if _, rep := vistas[h]; rep {
					anotar(chave, p.Destino.ChavePrimaria[0], transformar.VioUnica, "a chave primária se repete dentro da própria migração")
					continue
				}
				vistas[h] = struct{}{}
			}
			for _, fk := range fks {
				v := ""
				if vals[fk.idx] != nil && vals[fk.idx] != transformar.UsarPadrao {
					v = chaveNormal(vals[fk.idx], fk.coluna)
				}
				ok.fks = append(ok.fks, v)
			}
			for i, mc := range p.Map.Colunas {
				if s.referenciadas[p.Destino.Nome][mc.ColunaDestino] && vals[i] != nil && vals[i] != transformar.UsarPadrao {
					if ok.refs == nil {
						ok.refs = map[string]string{}
					}
					ok.refs[mc.ColunaDestino] = chaveNormal(vals[i], dest[i])
				}
			}
			oks = append(oks, ok)
		}
		recusadas, err := s.conferirConjunto(ctx, p, est, pkIdx, fks, oks, anotar)
		if err != nil {
			return rt, leitura, err
		}
		for i, o := range oks { // só o que vai mesmo entrar conta como pai das filhas
			if recusadas[i] {
				continue
			}
			for col, v := range o.refs {
				s.lembrar(p.Destino.Nome, col, v)
			}
		}
		s.feitas += int64(len(lote))
		s.r.Evento("simular", agentev1.EventoTarefa_INFO,
			fmt.Sprintf("%s → %s: %d de %d linhas conferidas", p.Origem.Nome, p.Destino.Nome, rt.Linhas, total),
			pct(s.feitas, s.total), map[string]float64{"linhas": float64(rt.Linhas), "problemas": float64(rt.ComProblema)})
	}
	rt.LinhasOK = rt.Linhas - rt.ComProblema
	if len(rt.Violacoes) == 0 {
		rt.Violacoes = nil
	}
	if len(rt.Perdas) == 0 {
		rt.Perdas = nil
	}
	rt.DuracaoMS = time.Since(inicio).Milliseconds()
	return rt, leitura, nil
}

func (s *simulacao) lembrar(tabela, coluna, valor string) {
	if s.carregadas[tabela] == nil {
		s.carregadas[tabela] = map[string]map[uint64]struct{}{}
	}
	if s.carregadas[tabela][coluna] == nil {
		s.carregadas[tabela][coluna] = map[uint64]struct{}{}
	}
	s.carregadas[tabela][coluna][hashChave(valor)] = struct{}{}
}

func (s *simulacao) vaiCarregar(tabela, coluna, valor string) bool {
	_, ok := s.carregadas[tabela][coluna][hashChave(valor)]
	return ok
}

// conferirConjunto confere o que só dá para ver olhando o destino: chave primária
// que já existe lá (tabela com dados) e FK apontando para pai inexistente. Devolve
// quais linhas seriam recusadas.
func (s *simulacao) conferirConjunto(ctx context.Context, p plano.Passo, est string, pkIdx []int, fks []fkSimples,
	oks []linhaOK, anotar func(chave, coluna, tipo, msg string)) ([]bool, error) {
	if len(oks) == 0 {
		return nil, nil
	}
	recusada := make([]bool, len(oks))
	if est == plano.EstrategiaStaging && len(pkIdx) == 1 {
		pks := make([]string, len(oks))
		for i, o := range oks {
			pks[i] = o.pk[:len(o.pk)-1]
		}
		existem, err := s.existentes(ctx, p.Destino.Nome, p.Destino.ChavePrimaria[0], pks)
		if err != nil {
			return nil, err
		}
		for i, v := range pks {
			if existem[v] {
				recusada[i] = true
				anotar(oks[i].chave, p.Destino.ChavePrimaria[0], transformar.VioUnica, "a chave primária já existe no destino")
			}
		}
	}
	for j, fk := range fks {
		var buscar []string
		for i, o := range oks {
			v := o.fks[j]
			if recusada[i] || v == "" {
				continue
			}
			if s.vaiCarregar(fk.pai, fk.colunaPai, v) {
				continue
			}
			buscar = append(buscar, v)
		}
		if len(buscar) == 0 {
			continue
		}
		existem, err := s.existentes(ctx, fk.pai, fk.colunaPai, buscar)
		if err != nil {
			return nil, err
		}
		for i, o := range oks {
			v := o.fks[j]
			if recusada[i] || v == "" {
				continue
			}
			if s.vaiCarregar(fk.pai, fk.colunaPai, v) || existem[v] {
				continue
			}
			recusada[i] = true
			anotar(o.chave, fk.coluna.Nome, transformar.VioEstrangeira,
				fmt.Sprintf("%s aponta para um registro de %s que não existe (nem na migração, nem no destino)", fk.coluna.Nome, fk.pai))
		}
	}
	return recusada, nil
}

// existentes devolve quais valores existem na coluna de uma tabela do destino.
// Comparação por texto: serve para inteiro, texto e uuid (os casos de chave).
func (s *simulacao) existentes(ctx context.Context, tabela, coluna string, valores []string) (map[string]bool, error) {
	t, err := s.g.d.tabela(tabela)
	if err != nil {
		return nil, err
	}
	c, err := s.g.d.id(coluna)
	if err != nil {
		return nil, err
	}
	rows, err := s.g.db.QueryContext(ctx, fmt.Sprintf("SELECT %s::text FROM %s WHERE %s::text = ANY($1::text[])", c, t, c), valores)
	if err != nil {
		return nil, fmt.Errorf("conferindo %s no destino: %w", tabela, err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return nil, err
		}
		out[v] = true
	}
	return out, rows.Err()
}
