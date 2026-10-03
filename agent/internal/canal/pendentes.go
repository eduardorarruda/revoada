package canal

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
	"google.golang.org/protobuf/encoding/protojson"
)

// Pendentes guarda em disco os eventos e resultados que o painel ainda não confirmou
// (ARQUITETURA §7: entrega pelo menos uma vez). Uma queda de rede ou um reinício do agente
// não perde nada: ao reconectar, tudo é reenviado e o painel ignora os repetidos.
type Pendentes struct {
	dir string
	mu  sync.Mutex
	// por tarefa, em ordem de seq
	fila map[string][]*agentev1.MsgAgente
}

// AbrirPendentes carrega o que ficou de antes (um arquivo .jsonl por tarefa).
func AbrirPendentes(dir string) (*Pendentes, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	p := &Pendentes{dir: dir, fila: map[string][]*agentev1.MsgAgente{}}
	entradas, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	for _, e := range entradas {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		msgs, err := lerJSONL(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("pendentes %s: %w", e.Name(), err)
		}
		if len(msgs) > 0 {
			p.fila[strings.TrimSuffix(e.Name(), ".jsonl")] = msgs
		}
	}
	return p, nil
}

func lerJSONL(caminho string) ([]*agentev1.MsgAgente, error) {
	b, err := os.ReadFile(caminho)
	if err != nil {
		return nil, err
	}
	var out []*agentev1.MsgAgente
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		if len(bytes.TrimSpace(sc.Bytes())) == 0 {
			continue
		}
		m := &agentev1.MsgAgente{}
		if err := protojson.Unmarshal(sc.Bytes(), m); err != nil {
			continue // linha cortada por uma queda no meio da escrita: descarta só ela
		}
		out = append(out, m)
	}
	return out, sc.Err()
}

// tarefaESeq extrai a chave de idempotência de uma mensagem guardável.
func tarefaESeq(m *agentev1.MsgAgente) (string, int64, error) {
	switch c := m.GetCorpo().(type) {
	case *agentev1.MsgAgente_Evento:
		return c.Evento.GetTarefaId(), c.Evento.GetSeq(), nil
	case *agentev1.MsgAgente_Checkpoint:
		return c.Checkpoint.GetTarefaId(), c.Checkpoint.GetSeq(), nil
	case *agentev1.MsgAgente_Resultado:
		return c.Resultado.GetTarefaId(), c.Resultado.GetSeq(), nil
	}
	return "", 0, errors.New("mensagem sem tarefa/seq não vai para os pendentes")
}

// Adicionar grava a mensagem antes de ela sair pela rede.
func (p *Pendentes) Adicionar(m *agentev1.MsgAgente) error {
	tarefa, _, err := tarefaESeq(m)
	if err != nil {
		return err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fila[tarefa] = append(p.fila[tarefa], m)
	return p.anexar(tarefa, m)
}

func (p *Pendentes) anexar(tarefa string, m *agentev1.MsgAgente) error {
	b, err := protojson.Marshal(m)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(p.caminho(tarefa), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(b, '\n')); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Confirmar remove tudo até `seq` daquela tarefa (o painel já gravou).
func (p *Pendentes) Confirmar(tarefa string, seq int64) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	fila := p.fila[tarefa]
	resto := fila[:0]
	for _, m := range fila {
		if _, s, _ := tarefaESeq(m); s > seq {
			resto = append(resto, m)
		}
	}
	if len(resto) == 0 {
		delete(p.fila, tarefa)
		if err := os.Remove(p.caminho(tarefa)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return nil
	}
	p.fila[tarefa] = resto
	return p.regravar(tarefa, resto)
}

func (p *Pendentes) regravar(tarefa string, msgs []*agentev1.MsgAgente) error {
	var buf bytes.Buffer
	for _, m := range msgs {
		b, err := protojson.Marshal(m)
		if err != nil {
			return err
		}
		buf.Write(append(b, '\n'))
	}
	tmp := p.caminho(tarefa) + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p.caminho(tarefa))
}

// Todos devolve o que falta confirmar, por tarefa e em ordem de seq (para reenviar).
func (p *Pendentes) Todos() []*agentev1.MsgAgente {
	p.mu.Lock()
	defer p.mu.Unlock()
	tarefas := make([]string, 0, len(p.fila))
	for t := range p.fila {
		tarefas = append(tarefas, t)
	}
	sort.Strings(tarefas)
	var out []*agentev1.MsgAgente
	for _, t := range tarefas {
		out = append(out, p.fila[t]...)
	}
	return out
}

// Quantos devolve quantas mensagens esperam confirmação.
func (p *Pendentes) Quantos() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := 0
	for _, f := range p.fila {
		n += len(f)
	}
	return n
}

// caminho: o id da tarefa vem do painel, mas é sempre [a-z_0-9]; ainda assim só
// o nome base é usado (nada de "../" escapar do diretório).
func (p *Pendentes) caminho(tarefa string) string {
	return filepath.Join(p.dir, filepath.Base(tarefa)+".jsonl")
}
