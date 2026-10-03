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

// Verificar é a conferência sob demanda (ARQUITETURA §9.4): relê a origem em ordem de chave,
// converte cada linha EXATAMENTE como a execução converteu, acha a mesma chave no
// destino e compara coluna a coluna pela forma canônica (canonico.go). Só SELECT nos
// dois lados. Depois da conclusão compara com as tabelas reais — que podem ter
// linhas que já estavam lá (estratégia staging): só as chaves da origem contam.
// O relatório aponta as linhas pela chave e as colunas pelo nome — nunca os valores.
func Verificar(ctx context.Context, bruto []byte, r Relator) (any, error) {
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
	g := destino{db: dbDest, d: novoDialeto(esp.Destino, 0)}
	res := &plano.ResumoVerificacao{Execucao: esp.Execucao, VerificadoEm: time.Now().UTC()}
	defer func() { res.DuracaoMS = time.Since(inicio).Milliseconds() }()

	man, err := g.manifestoSeExistir(ctx, esp.Execucao)
	if err != nil {
		return res, fmt.Errorf("lendo o manifesto no destino: %w", err)
	}
	switch {
	case man == nil:
		return res, errors.New("o destino não tem o manifesto desta execução: ela não chegou a gravar nada (ou o controle foi apagado)")
	case man.Fase == "revertido":
		return res, errors.New("esta execução foi revertida: o destino voltou ao que era e não há o que verificar")
	}
	res.Fase = man.Fase
	cmp := comparador{origem: origem, dOrigem: novoDialeto(esp.Origem, esp.EsquemaOrigem.Dialeto), g: g, lote: esp.Lote, r: r}
	for _, p := range passos {
		n, err := novoLeitor(origem, cmp.dOrigem, p).contar(ctx)
		if err != nil {
			return res, err
		}
		cmp.total += n
	}
	cmp.progresso = func(p plano.Passo, linhas int64) {
		r.Evento("verificar", agentev1.EventoTarefa_INFO, fmt.Sprintf("%s → %s: %d linhas comparadas", p.Origem.Nome, p.Destino.Nome, linhas),
			pct(cmp.feitas, cmp.total), map[string]float64{"linhas": float64(linhas)})
	}
	r.Evento("verificar", agentev1.EventoTarefa_INFO, fmt.Sprintf("%d tabelas, %d linhas da origem a comparar com o destino (só leitura)",
		len(passos), cmp.total), 0, nil)
	res.ConteudoOK = true
	textoRodou, textoFalhou := false, false
	var divTexto int64
	for _, p := range passos {
		vt, err := cmp.verificarTabela(ctx, p, man)
		res.Tabelas = append(res.Tabelas, vt)
		res.Linhas += vt.Linhas
		res.Identicas += vt.Identicas
		res.Divergentes += vt.Divergentes
		res.Ausentes += vt.Ausentes
		res.ConteudoOK = res.ConteudoOK && vt.ConteudoOK
		if tx := vt.Texto; tx != nil && len(tx.Colunas)+len(tx.Parciais) > 0 {
			textoRodou = true
			textoFalhou = textoFalhou || !tx.OK
			res.ColunasTexto += len(tx.Colunas)
			divTexto += tx.Divergentes + tx.Ausentes
		}
		if err != nil {
			res.ConteudoOK = false
			return res, err
		}
	}
	res.TextoOK = textoRodou && !textoFalhou
	nivel, msg := agentev1.EventoTarefa_INFO, fmt.Sprintf("verificação concluída: %d linhas comparadas, todas idênticas entre origem e destino; "+
		"%d colunas conferidas também pelo texto dos próprios bancos", res.Linhas, res.ColunasTexto)
	switch {
	case !res.ConteudoOK:
		nivel = agentev1.EventoTarefa_AVISO
		msg = fmt.Sprintf("verificação concluída: %d linhas comparadas — %d divergentes, %d ausentes no destino", res.Linhas, res.Divergentes, res.Ausentes)
	case textoFalhou:
		nivel = agentev1.EventoTarefa_AVISO
		msg = fmt.Sprintf("verificação concluída: a comparação canônica bateu, mas o texto dos próprios bancos diverge em %d linhas — "+
			"provável erro de leitura/driver; veja as colunas", divTexto)
	}
	r.Evento("verificar", nivel, msg, 100, map[string]float64{"linhas": float64(res.Linhas), "divergentes": float64(res.Divergentes),
		"ausentes": float64(res.Ausentes)})
	return res, nil
}

// manifestoSeExistir lê o manifesto; nil se nem a tabela de controle existe.
func (g destino) manifestoSeExistir(ctx context.Context, execucao string) (*plano.Manifesto, error) {
	var existe bool
	if err := g.db.QueryRowContext(ctx, "SELECT to_regclass($1) IS NOT NULL", g.manifesto()).Scan(&existe); err != nil {
		return nil, err
	}
	if !existe {
		return nil, nil
	}
	return g.lerManifesto(ctx, g.db, execucao)
}

// verificarTabela escolhe onde comparar (staging antes da troca, tabela real depois)
// e como (linha a linha pela chave; sem chave, pela soma do conteúdo).
func (cmp *comparador) verificarTabela(ctx context.Context, p plano.Passo, man *plano.Manifesto) (plano.ResumoVerificacaoTabela, error) {
	alvo, onde, estrategia := p.Destino.Nome, "tabela", ""
	if it, ok := man.Item(p.Destino.Nome); ok {
		estrategia = it.Estrategia
		if man.Fase == "carregando" && it.Staging != "" && !it.Trocada {
			alvo, onde = it.Staging, "staging"
		}
	}
	vt := plano.ResumoVerificacaoTabela{Origem: p.Origem.Nome, Destino: p.Destino.Nome, Onde: onde}
	casas, err := cmp.g.casasTempo(ctx, alvo)
	if err != nil {
		return vt, err
	}
	dest := colunasDestino(p)
	conf := novaConferencia(p, dest, casas)
	if len(conf.chave) == 0 {
		vt, err = cmp.porSomas(ctx, p, alvo, conf, dest, estrategia == plano.EstrategiaStaging && onde == "tabela")
		vt.Onde = onde
		vt.Texto = &plano.ConferenciaTexto{Motivo: "a tabela não tem chave para achar cada linha: a conferência pelo texto precisa dela"}
		return vt, err
	}
	// segunda conferência, independente da primeira: o texto renderizado por cada
	// banco, lido junto na mesma passada
	pt := novoPlanoTexto(p, cmp.dOrigem, cmp.g.d, conf, dest, casas, nomeDoFuso())
	feitas := cmp.feitas
	vt, err = cmp.tabela(ctx, p, alvo, conf, dest, pt)
	if err != nil && pt.ativo() && !cancelada(err) {
		// se foi a parte do texto que o banco recusou (uma expressão que esta versão
		// não aceita), a primeira conferência não pode ficar sem resposta: refaz sem o
		// texto e registra o motivo
		cmp.r.Evento("verificar", agentev1.EventoTarefa_AVISO, fmt.Sprintf("%s: refazendo sem a conferência pelo texto: %v", p.Destino.Nome, err),
			pct(cmp.feitas, cmp.total), nil)
		cmp.feitas = feitas
		vt, err = cmp.tabela(ctx, p, alvo, conf, dest, nil)
		vt.Texto = &plano.ConferenciaTexto{NaoConferidas: pt.ct.NaoConferidas,
			Motivo: "a conferência pelo texto falhou nesta tabela (o banco recusou a renderização do texto; detalhe na tela ao vivo)"}
	}
	vt.Onde = onde
	return vt, err
}

// ---------------------------------------------------------------- linha a linha

// comparador compara a origem (convertida) com uma tabela do destino.
type comparador struct {
	origem  *sql.DB
	dOrigem dialeto
	g       destino
	lote    int
	r       Relator
	// pararNaAmostra: para ao juntar a amostra de divergências (localização dentro
	// da execução, que já vai falhar; a contagem fica parcial).
	pararNaAmostra bool
	total, feitas  int64
	progresso      func(p plano.Passo, linhas int64)
}

// linhaOrigem é uma linha da origem já convertida, esperando a busca no destino.
type linhaOrigem struct {
	chaveTexto string // como aparece no relatório (a chave da ORIGEM, igual às amostras)
	chave      string // canônica, para achar no destino
	vals       []any
	textos     []*string // dupla conferência: o valor como a ORIGEM o renderizou
}

// tabela compara linha a linha. Precisa de conf.chave.
//
// pt (opcional) é a dupla conferência pelo texto: as expressões que fazem cada banco
// renderizar o valor como texto vão NA MESMA leitura da origem e NA MESMA busca no
// destino de cada lote — a segunda conferência não custa outra ida e volta.
func (cmp *comparador) tabela(ctx context.Context, p plano.Passo, alvo string, conf conferencia, dest []esquema.Coluna,
	pt *planoTexto) (vt plano.ResumoVerificacaoTabela, err error) {
	inicio := time.Now()
	vt = plano.ResumoVerificacaoTabela{Origem: p.Origem.Nome, Destino: p.Destino.Nome, PorColuna: map[string]int64{}}
	defer func() { vt.DuracaoMS = time.Since(inicio).Milliseconds() }()
	padrao := make([]int64, len(dest))  // células que nasceram no destino (DEFAULT), por coluna
	var chaveNoDestino, repetidas int64 // linhas que não dá para achar no destino
	l := novoLeitor(cmp.origem, cmp.dOrigem, p)
	defer l.fechar()
	extras, err := cmp.g.colunasEntreAspas(dest)
	if err != nil {
		return vt, err
	}
	if pt.ativo() {
		if err := pt.estender(l, cmp.dOrigem); err != nil {
			return vt, err
		}
		extras = append(extras, pt.expressoesDestino(cmp.g.d)...)
	}
	divergiu := func(chave string, cols []string, ausente bool) {
		if len(vt.Divergencias) < plano.AmostraDivergencias {
			vt.Divergencias = append(vt.Divergencias, plano.Divergencia{Chave: chave, Colunas: cols, Ausente: ausente})
		}
	}
	for {
		if err := cmp.r.Pausa(ctx); err != nil {
			return vt, err
		}
		ls, err := l.proximo(ctx, cmp.lote)
		if err != nil {
			return vt, err
		}
		if len(ls) == 0 {
			break
		}
		pend := make([]linhaOrigem, 0, len(ls))
		for _, ln := range ls {
			ct := l.chaveDe(ln)
			v, _, prob := converter(p, dest, ln)
			if prob != nil { // a origem mudou desde a execução e a linha nem converte mais
				vt.Divergentes++
				vt.PorColuna[prob.coluna]++
				divergiu(ct, []string{prob.coluna}, false)
				continue
			}
			k, ok := conf.chaveCanonica(v)
			if !ok {
				chaveNoDestino++
				pt.naoLocalizada()
				continue
			}
			pend = append(pend, linhaOrigem{chaveTexto: ct, chave: k, vals: v, textos: pt.textosDaOrigem(ln)})
		}
		achadas, duplas, err := cmp.g.buscarPor(ctx, alvo, dest, conf, pend, extras)
		if err != nil {
			return vt, fmt.Errorf("buscando as linhas de %s no destino: %w", p.Destino.Nome, err)
		}
		for _, o := range pend {
			d, ok := achadas[o.chave]
			if duplas[o.chave] { // duas linhas no destino com a mesma chave: qual é a migrada?
				repetidas++
				divergiu(o.chaveTexto, nomesDaChave(conf), false)
				pt.naoLocalizada()
				continue
			}
			if !ok {
				vt.Ausentes++
				divergiu(o.chaveTexto, nil, true)
				pt.ausente(o.chaveTexto)
				continue
			}
			pt.comparar(o.chaveTexto, o.textos, d[len(dest):])
			var cols []string
			for i, rg := range conf.regras {
				switch {
				case !rg.comparavel:
				case o.vals[i] == transformar.UsarPadrao:
					padrao[i]++
				case rg.canonico(o.vals[i]) != rg.canonico(d[i]):
					cols = append(cols, rg.nome)
					vt.PorColuna[rg.nome]++
				}
			}
			if len(cols) > 0 {
				vt.Divergentes++
				divergiu(o.chaveTexto, cols, false)
			} else {
				vt.Identicas++
			}
		}
		vt.Linhas += int64(len(ls))
		cmp.feitas += int64(len(ls))
		if cmp.progresso != nil {
			cmp.progresso(p, vt.Linhas)
		}
		if cmp.pararNaAmostra && len(vt.Divergencias) >= plano.AmostraDivergencias {
			break
		}
	}
	for i, rg := range conf.regras {
		switch {
		case !rg.comparavel:
			vt.ColunasNaoConferidas = append(vt.ColunasNaoConferidas, plano.ColunaNaoConferida{Coluna: rg.nome, Motivo: rg.motivo})
		case padrao[i] > 0:
			vt.ColunasNaoConferidas = append(vt.ColunasNaoConferidas, plano.ColunaNaoConferida{Coluna: rg.nome,
				Motivo: fmt.Sprintf("em %d linhas o valor nasceu no destino (DEFAULT) e não foi comparado; nas demais, foi", padrao[i])})
		default:
			vt.ColunasConferidas = append(vt.ColunasConferidas, rg.nome)
		}
	}
	var motivos []string
	if chaveNoDestino > 0 {
		motivos = append(motivos, fmt.Sprintf("%d linhas têm a chave gerada pelo destino (DEFAULT) e não puderam ser localizadas", chaveNoDestino))
	}
	if repetidas > 0 {
		motivos = append(motivos, fmt.Sprintf("%d chaves aparecem repetidas no destino: não dá para saber qual linha é a migrada", repetidas))
	}
	vt.Motivo = strings.Join(motivos, "; ")
	vt.NaoLocalizadas = chaveNoDestino + repetidas
	if len(vt.PorColuna) == 0 {
		vt.PorColuna = nil
	}
	vt.Texto = pt.fechar()
	vt.ConteudoOK = vt.Divergentes == 0 && vt.Ausentes == 0 && vt.NaoLocalizadas == 0 &&
		(len(vt.ColunasConferidas) > 0 || vt.Linhas == 0)
	return vt, nil
}

// nomesDaChave: as colunas da chave da linha (para apontar chave repetida).
func nomesDaChave(conf conferencia) []string {
	out := make([]string, len(conf.chave))
	for i, j := range conf.chave {
		out[i] = conf.regras[j].nome
	}
	return out
}

func (g destino) colunasEntreAspas(dest []esquema.Coluna) ([]string, error) {
	out := make([]string, len(dest))
	for i, c := range dest {
		q, err := g.d.id(c.Nome)
		if err != nil {
			return nil, err
		}
		out[i] = q
	}
	return out, nil
}

// buscarPor lê do destino as linhas com as chaves pedidas (IN com parâmetros, em
// pedaços que cabem no limite do PostgreSQL) e devolve chave canônica → valores das
// colunas gravadas (na ordem de dest), mais as chaves que aparecem em MAIS de uma
// linha (nunca são dadas como idênticas). Os parâmetros são os valores CONVERTIDOS —
// os mesmos que o INSERT mandou, então o PostgreSQL infere o tipo pela coluna
// exatamente como na gravação.
//
// Os itens do SELECT são escolhidos por quem chama (colunas ou
// expressões montadas pelo motor, como o texto renderizado pelo próprio banco na
// dupla conferência). A chave da linha vem junto, para achar a linha da origem.
func (g destino) buscarPor(ctx context.Context, tabela string, dest []esquema.Coluna, conf conferencia,
	linhas []linhaOrigem, extras []string) (map[string][]any, map[string]bool, error) {
	out := make(map[string][]any, len(linhas))
	duplas := map[string]bool{}
	if len(linhas) == 0 {
		return out, duplas, nil
	}
	t, err := g.d.tabela(tabela)
	if err != nil {
		return nil, nil, err
	}
	ks := make([]string, len(conf.chave))
	for i, j := range conf.chave {
		if ks[i], err = g.d.id(dest[j].Nome); err != nil {
			return nil, nil, err
		}
	}
	sel := strings.Join(append(append([]string(nil), ks...), extras...), ", ")
	lhs := ks[0]
	if len(ks) > 1 {
		lhs = "(" + strings.Join(ks, ", ") + ")"
	}
	porComando := max(limiteParametros/len(ks), 1)
	for ini := 0; ini < len(linhas); ini += porComando {
		pedaco := linhas[ini:min(ini+porComando, len(linhas))]
		var b strings.Builder
		args := make([]any, 0, len(pedaco)*len(ks))
		fmt.Fprintf(&b, "SELECT %s FROM %s WHERE %s IN (", sel, t, lhs)
		for i, o := range pedaco {
			if i > 0 {
				b.WriteString(", ")
			}
			if len(ks) > 1 {
				b.WriteByte('(')
			}
			for j, k := range conf.chave {
				if j > 0 {
					b.WriteString(", ")
				}
				args = append(args, o.vals[k])
				fmt.Fprintf(&b, "$%d", len(args))
			}
			if len(ks) > 1 {
				b.WriteByte(')')
			}
		}
		b.WriteByte(')')
		if err := g.lerLinhas(ctx, b.String(), args, len(ks)+len(extras), func(vals []any) {
			chave := make([]any, len(dest))
			for i, j := range conf.chave {
				chave[j] = vals[i]
			}
			if k, ok := conf.chaveCanonica(chave); ok {
				if _, ja := out[k]; ja {
					duplas[k] = true
				}
				out[k] = vals[len(ks):]
			}
		}); err != nil {
			return nil, nil, err
		}
	}
	return out, duplas, nil
}

func (g destino) lerLinhas(ctx context.Context, q string, args []any, ncols int, cada func([]any)) error {
	rows, err := g.db.QueryContext(ctx, q, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		vals := make([]any, ncols)
		ptrs := make([]any, ncols)
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return err
		}
		cada(vals)
	}
	return rows.Err()
}

// ---------------------------------------------------------------- sem chave

// porSomas: a tabela não tem chave para achar cada linha. Se o destino só tem linhas
// desta migração, compara contagem e a soma do conteúdo de cada coluna (multiconjunto:
// diz QUAL coluna diverge, não qual linha). Se a tabela real já tinha linhas antes,
// não dá para separar as migradas — fica registrado como não conferida.
func (cmp *comparador) porSomas(ctx context.Context, p plano.Passo, alvo string, conf conferencia, dest []esquema.Coluna,
	tinhaDados bool) (vt plano.ResumoVerificacaoTabela, err error) {
	inicio := time.Now()
	vt = plano.ResumoVerificacaoTabela{Origem: p.Origem.Nome, Destino: p.Destino.Nome, SemChave: true}
	defer func() { vt.DuracaoMS = time.Since(inicio).Milliseconds() }()
	if tinhaDados {
		vt.Motivo = "a tabela não tem chave e já tinha linhas antes da migração: não dá para separar as migradas das que já estavam"
		for _, rg := range conf.regras {
			vt.ColunasNaoConferidas = append(vt.ColunasNaoConferidas, plano.ColunaNaoConferida{Coluna: rg.nome, Motivo: vt.Motivo})
		}
		return vt, nil
	}
	gravado := novasSomas(len(dest))
	l := novoLeitor(cmp.origem, cmp.dOrigem, p)
	defer l.fechar()
	for {
		if err := cmp.r.Pausa(ctx); err != nil {
			return vt, err
		}
		ls, err := l.proximo(ctx, cmp.lote)
		if err != nil {
			return vt, err
		}
		if len(ls) == 0 {
			break
		}
		for _, ln := range ls {
			v, _, prob := converter(p, dest, ln)
			if prob != nil {
				vt.Divergentes++
				continue
			}
			gravado.add(conf, v)
		}
		vt.Linhas += int64(len(ls))
		cmp.feitas += int64(len(ls))
		if cmp.progresso != nil {
			cmp.progresso(p, vt.Linhas)
		}
	}
	n, _, relido, err := cmp.g.reler(ctx, alvo, dest, colunaSoma(p), conf)
	if err != nil {
		return vt, fmt.Errorf("relendo %s: %w", p.Destino.Nome, err)
	}
	vt.ColunasConferidas, vt.ColunasNaoConferidas = colunasDoVeredito(conf, gravado)
	div := colunasDivergentes(conf, gravado, relido)
	switch {
	case n != vt.Linhas:
		vt.Motivo = fmt.Sprintf("a contagem não bate: %d linhas na origem, %d no destino", vt.Linhas, n)
	case len(div) > 0:
		vt.Motivo = "sem chave: a soma do conteúdo não bateu — dá para saber a coluna, não a linha"
		vt.Divergencias = []plano.Divergencia{{Colunas: div}}
	case vt.Divergentes == 0:
		vt.Identicas = vt.Linhas
	}
	vt.ConteudoOK = vt.Motivo == "" && vt.Divergentes == 0 && !gravado.chaveInvalida && (len(vt.ColunasConferidas) > 0 || vt.Linhas == 0)
	return vt, nil
}
