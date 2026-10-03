package copia

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/bits"
	"strconv"
	"strings"

	"github.com/eduardorarruda/revoada/core/migracao/esquema"
	"github.com/eduardorarruda/revoada/core/migracao/modelo"
	"github.com/eduardorarruda/revoada/core/migracao/plano"
	"github.com/eduardorarruda/revoada/core/migracao/transformar"
)

// ---------------------------------------------------------------- soma de conteúdo

// soma256 é a soma (mod 2^256) do sha256 de cada parcela: não depende da ordem das
// linhas, então o motor calcula ao gravar (em lotes, retomável) e de novo relendo o
// destino de uma vez, e as duas têm de bater. 256 bits: colisão por acaso é
// desprezível mesmo com bilhões de linhas.
type soma256 [4]uint64

func (s *soma256) add(h [32]byte) {
	var c uint64
	for i := 3; i >= 0; i-- {
		s[i], c = bits.Add64(s[i], binary.BigEndian.Uint64(h[i*8:]), c)
	}
}

func (s soma256) String() string {
	var b [32]byte
	for i := range s {
		binary.BigEndian.PutUint64(b[i*8:], s[i])
	}
	return hex.EncodeToString(b[:])
}

func lerSoma256(t string) (soma256, error) {
	var s soma256
	b, err := hex.DecodeString(t)
	if err != nil || len(b) != 32 {
		return s, fmt.Errorf("soma de conteúdo inválida no checkpoint")
	}
	for i := range s {
		s[i] = binary.BigEndian.Uint64(b[i*8:])
	}
	return s, nil
}

// parcela liga o valor à SUA linha: sha256(chave canônica || 0x00 || valor canônico).
// Com isso, trocar o valor de duas linhas entre si também muda a soma.
func parcela(chave, valor string) [32]byte {
	h := sha256.New()
	h.Write([]byte(chave))
	h.Write([]byte{0})
	h.Write([]byte(valor))
	var out [32]byte
	h.Sum(out[:0])
	return out
}

// ---------------------------------------------------------------- regras de uma tabela

// conferencia junta, para uma tabela, como canonizar cada coluna gravada e qual é a
// chave que identifica a linha no destino.
type conferencia struct {
	regras []regra
	// chave: posições (em p.Map.Colunas) da chave da linha no destino. Vazia = a
	// tabela não tem chave comparável: as somas viram "multiconjunto por coluna"
	// (acusam QUAL coluna mudou, não qual linha).
	chave []int
}

func novaConferencia(p plano.Passo, dest []esquema.Coluna, casasTempo map[string]int) conferencia {
	c := conferencia{regras: make([]regra, len(dest))}
	for i, d := range dest {
		c.regras[i] = novaRegra(d, casasTempo)
	}
	c.chave = chaveDaLinha(p, c.regras)
	return c
}

// chaveDaLinha: a chave primária do destino (toda gravada pela migração), senão a
// coluna de destino da coluna de lote da origem — só se ela é ÚNICA na origem, vai
// sem transformação (uma transformação pode juntar valores) e é única no destino ou
// a tabela nasceu na migração. Chave repetida faria duas linhas virarem uma na
// comparação: melhor cair no caminho "sem chave", que diz o que garante. Todas as
// partes precisam ser comparáveis (senão a chave relida não bate com a gravada).
func chaveDaLinha(p plano.Passo, regras []regra) []int {
	idx := indicesPK(p)
	if len(idx) == 0 && p.Map.ColunaLote != "" && p.Origem.Unica(p.Map.ColunaLote) {
		for i, mc := range p.Map.Colunas {
			direta := mc.Transformacao == modelo.Nenhuma || mc.Transformacao == modelo.ConverterTipo || mc.Transformacao == ""
			if mc.ColunaOrigem == p.Map.ColunaLote && direta && (p.Criar || p.Destino.Unica(mc.ColunaDestino)) {
				idx = []int{i}
				break
			}
		}
	}
	for _, i := range idx {
		if !regras[i].comparavel {
			return nil
		}
	}
	return idx
}

// chaveCanonica é a chave da linha em forma canônica (partes com tamanho na frente:
// não há separador que um texto não possa conter). ok=false se alguma parte nasce
// no destino (DEFAULT) — aí não dá para saber a chave do lado de quem grava.
func (c conferencia) chaveCanonica(vals []any) (string, bool) {
	if len(c.chave) == 0 {
		return "", true
	}
	var b strings.Builder
	for _, i := range c.chave {
		if vals[i] == transformar.UsarPadrao {
			return "", false
		}
		s := c.regras[i].canonico(vals[i])
		b.WriteString(strconv.Itoa(len(s)))
		b.WriteByte(':')
		b.WriteString(s)
	}
	return b.String(), true
}

// ---------------------------------------------------------------- estado (checkpoint)

// versaoConteudo do formato gravado em revoada_controle.conteudo.
const versaoConteudo = 1

// somasConteudo é o que vai junto do checkpoint, NA MESMA TRANSAÇÃO do lote: a soma
// de cada coluna até ali e quais colunas receberam DEFAULT em alguma linha.
type somasConteudo struct {
	somas         []soma256
	padrao        []bool // a coluna recebeu DEFAULT em alguma linha: fica fora da comparação
	chaveInvalida bool   // a chave da linha recebeu DEFAULT: nada da tabela é comparável
}

func novasSomas(n int) somasConteudo {
	return somasConteudo{somas: make([]soma256, n), padrao: make([]bool, n)}
}

// add soma uma linha (os valores convertidos, prontos para gravar, ou os relidos).
func (s *somasConteudo) add(c conferencia, vals []any) {
	chave, ok := c.chaveCanonica(vals)
	if !ok {
		s.chaveInvalida = true
	}
	for i, v := range vals {
		if v == transformar.UsarPadrao {
			s.padrao[i] = true
			continue
		}
		if !c.regras[i].comparavel {
			continue
		}
		s.somas[i].add(parcela(chave, c.regras[i].canonico(v)))
	}
}

// clonar: o lote só passa a valer se a transação confirmar.
func (s somasConteudo) clonar() somasConteudo {
	return somasConteudo{somas: append([]soma256(nil), s.somas...), padrao: append([]bool(nil), s.padrao...),
		chaveInvalida: s.chaveInvalida}
}

type estadoJSON struct {
	Versao        int               `json:"v"`
	Somas         map[string]string `json:"somas"`
	Padrao        []string          `json:"padrao,omitempty"`
	ChaveInvalida bool              `json:"chave_invalida,omitempty"`
	// Conferido: a releitura do destino bateu (gravado só no checkpoint final).
	Conferido bool `json:"conferido,omitempty"`
}

func (s somasConteudo) codificar(c conferencia, conferido bool) string {
	e := estadoJSON{Versao: versaoConteudo, Somas: map[string]string{}, ChaveInvalida: s.chaveInvalida, Conferido: conferido}
	for i, r := range c.regras {
		e.Somas[r.nome] = s.somas[i].String()
		if s.padrao[i] {
			e.Padrao = append(e.Padrao, r.nome)
		}
	}
	b, _ := json.Marshal(e)
	return string(b)
}

// decodificarSomas lê o estado do checkpoint. Erro = formato desconhecido ou de
// outro mapeamento (coluna faltando): quem chama trata como checkpoint antigo.
func decodificarSomas(t string, c conferencia) (somasConteudo, bool, error) {
	var e estadoJSON
	if err := json.Unmarshal([]byte(t), &e); err != nil || e.Versao != versaoConteudo {
		return somasConteudo{}, false, fmt.Errorf("formato de conteúdo desconhecido no checkpoint")
	}
	s := novasSomas(len(c.regras))
	s.chaveInvalida = e.ChaveInvalida
	padrao := map[string]bool{}
	for _, n := range e.Padrao {
		padrao[n] = true
	}
	for i, r := range c.regras {
		h, ok := e.Somas[r.nome]
		if !ok {
			return somasConteudo{}, false, fmt.Errorf("o checkpoint não tem a soma da coluna %s", r.nome)
		}
		v, err := lerSoma256(h)
		if err != nil {
			return somasConteudo{}, false, err
		}
		s.somas[i], s.padrao[i] = v, padrao[r.nome]
	}
	return s, e.Conferido, nil
}

// ---------------------------------------------------------------- veredito

// motivoChaveInvalida explica por que nada da tabela foi comparado.
const motivoChaveInvalida = "a chave da linha recebeu o valor padrão do destino (DEFAULT) em alguma linha: sem ela não dá para ligar cada valor à sua linha"

// colunasDoVeredito separa as colunas conferidas das que ficaram de fora (e por quê).
func colunasDoVeredito(c conferencia, s somasConteudo) ([]string, []plano.ColunaNaoConferida) {
	var ok []string
	var fora []plano.ColunaNaoConferida
	for i, r := range c.regras {
		switch {
		case s.chaveInvalida:
			fora = append(fora, plano.ColunaNaoConferida{Coluna: r.nome, Motivo: motivoChaveInvalida})
		case !r.comparavel:
			fora = append(fora, plano.ColunaNaoConferida{Coluna: r.nome, Motivo: r.motivo})
		case s.padrao[i]:
			fora = append(fora, plano.ColunaNaoConferida{Coluna: r.nome,
				Motivo: "em alguma linha o valor nasceu no destino (DEFAULT): o valor real só existe lá"})
		default:
			ok = append(ok, r.nome)
		}
	}
	return ok, fora
}

// colunasDivergentes compara as somas gravadas com as relidas, só nas conferidas.
func colunasDivergentes(c conferencia, gravado, relido somasConteudo) []string {
	if gravado.chaveInvalida {
		return nil
	}
	var out []string
	for i, r := range c.regras {
		if !r.comparavel || gravado.padrao[i] {
			continue
		}
		if gravado.somas[i] != relido.somas[i] {
			out = append(out, r.nome)
		}
	}
	return out
}
