package canal

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/eduardorarruda/revoada/core/seguranca/selo"
	agentev1 "github.com/eduardorarruda/revoada/proto/revoada/agente/v1"
)

// ErrEsquemaTempo: o agente não respondeu a tempo.
var ErrEsquemaTempo = errors.New("o agente não respondeu a leitura do schema a tempo")

// PedidoLeitura descreve o banco a ser lido pelo agente.
type PedidoLeitura struct {
	AgenteID string
	Motor    string
	Endereco string
	Banco    string
	Usuario  string
	Senha    []byte // decifrada em memória pelo chamador; aqui é selada para o agente
	Opcoes   map[string]string
}

// validadeSeloEsquema: a senha selada só serve para esta leitura, por pouco tempo.
const validadeSeloEsquema = 2 * time.Minute

// PedirEsquema pede ao agente que leia a ESTRUTURA do banco e devolve o JSON do
// schema neutro. A senha viaja selada só para a chave X25519 daquele agente.
func (s *Servico) PedirEsquema(ctx context.Context, p PedidoLeitura) ([]byte, error) {
	ag, err := s.repo.AgentePorID(ctx, p.AgenteID)
	if err != nil {
		return nil, err
	}
	if ag.Revogado {
		return nil, ErrAgenteRevogado
	}
	ses := s.sessaoConectada(ag.ID)
	if ses == nil {
		return nil, ErrAgenteOffline
	}
	id := novoID("esq")
	sl, err := selo.Selar(ag.ChaveSelo, p.Senha, "esquema:"+id, validadeSeloEsquema, s.agora())
	if err != nil {
		return nil, fmt.Errorf("selando a credencial: %w", err)
	}
	pedido := &agentev1.PedidoEsquema{
		PedidoId: id, Motor: p.Motor, Endereco: p.Endereco, Banco: p.Banco, Usuario: p.Usuario, Opcoes: p.Opcoes,
		Credencial: &agentev1.CredencialSelada{Nome: "banco", Efemera: sl.Efemera, Nonce: sl.Nonce, Cifrado: sl.Cifrado, ExpiraEm: sl.ExpiraEm},
	}
	if err := s.ca.AssinarPedidoEsquema(pedido); err != nil {
		return nil, err
	}
	resp := make(chan *agentev1.RespostaEsquema, 1)
	s.mu.Lock()
	s.esperas[id] = resp
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.esperas, id)
		s.mu.Unlock()
	}()
	if !s.enviar(ses, &agentev1.MsgPainel{Corpo: &agentev1.MsgPainel_PedidoEsquema{PedidoEsquema: pedido}}) {
		return nil, ErrAgenteOffline
	}
	espera, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	select {
	case <-espera.Done():
		return nil, ErrEsquemaTempo
	case r := <-resp:
		if r.GetErro() != "" {
			return nil, fmt.Errorf("o agente não conseguiu ler o banco: %s", r.GetErro())
		}
		return r.GetEsquema(), nil
	}
}
