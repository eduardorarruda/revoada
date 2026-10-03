package cofre

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
)

// chaveiro é o conjunto versionado de chaves mestras de um provedor local.
type chaveiro struct {
	mu     sync.RWMutex
	ativa  int
	chaves map[int][]byte
}

func (k *chaveiro) VersaoAtiva() int {
	k.mu.RLock()
	defer k.mu.RUnlock()
	return k.ativa
}

func (k *chaveiro) chave(versao int) ([]byte, error) {
	k.mu.RLock()
	defer k.mu.RUnlock()
	c, ok := k.chaves[versao]
	if !ok {
		return nil, ErrVersaoDesconhecida
	}
	return c, nil
}

func (k *chaveiro) Embrulhar(_ context.Context, versao int, dek []byte) ([]byte, error) {
	kek, err := k.chave(versao)
	if err != nil {
		return nil, err
	}
	return SelarAESGCM(kek, dek, aadDEK(versao))
}

func (k *chaveiro) Desembrulhar(_ context.Context, versao int, dekCifrada []byte) ([]byte, error) {
	kek, err := k.chave(versao)
	if err != nil {
		return nil, err
	}
	return AbrirAESGCM(kek, dekCifrada, aadDEK(versao))
}

// girar cria uma versão nova e a torna ativa. As antigas continuam para desembrulhar.
func (k *chaveiro) girar() error {
	nova := make([]byte, tamanhoChave)
	if _, err := rand.Read(nova); err != nil {
		return fmt.Errorf("cofre: gerando chave mestra: %w", err)
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	k.ativa++
	k.chaves[k.ativa] = nova
	return nil
}

// aadDEK amarra a DEK cifrada à versão da KEK: trocar o número da versão no banco
// para forçar outra chave faz o desembrulho falhar, em vez de usar a chave errada.
func aadDEK(versao int) []byte { return []byte("revoada/dek/v" + strconv.Itoa(versao)) }

// ProvedorMemoria guarda a chave mestra só em memória. Serve para testes e para o
// modo efêmero; ao reiniciar, tudo que foi cifrado com ele fica ilegível.
type ProvedorMemoria struct{ chaveiro }

// NovoProvedorMemoria cria um provedor com uma chave mestra aleatória.
func NovoProvedorMemoria() (*ProvedorMemoria, error) {
	p := &ProvedorMemoria{chaveiro{chaves: map[int][]byte{}}}
	if err := p.girar(); err != nil {
		return nil, err
	}
	return p, nil
}

// Girar cria uma versão nova da chave mestra (rotação).
func (p *ProvedorMemoria) Girar() error { return p.girar() }

// ProvedorArquivo guarda a chave mestra num arquivo FORA do banco da aplicação, com
// permissão restrita ao dono (0600). É o provedor padrão do painel em servidor.
type ProvedorArquivo struct {
	chaveiro
	caminho string
}

type arquivoChaves struct {
	VersaoAtiva int               `json:"versao_ativa"`
	Chaves      map[string]string `json:"chaves"` // versão → chave em base64
}

// ErrPermissaoAberta: o arquivo da chave mestra pode ser lido por outros usuários.
var ErrPermissaoAberta = errors.New("cofre: o arquivo da chave mestra está legível por outros usuários; ajuste para 0600")

// AbrirOuCriarArquivo carrega o arquivo de chaves; se não existir, cria um novo com
// uma chave aleatória. `criado` avisa o chamador para registrar isso de forma visível
// (perder esse arquivo = perder todos os segredos).
func AbrirOuCriarArquivo(caminho string) (p *ProvedorArquivo, criado bool, err error) {
	p = &ProvedorArquivo{chaveiro: chaveiro{chaves: map[int][]byte{}}, caminho: caminho}
	b, err := os.ReadFile(caminho)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err := p.girar(); err != nil {
			return nil, false, err
		}
		if err := p.salvar(); err != nil {
			return nil, false, err
		}
		return p, true, nil
	case err != nil:
		return nil, false, fmt.Errorf("cofre: lendo %s: %w", caminho, err)
	}
	if err := conferirPermissao(caminho); err != nil {
		return nil, false, err
	}
	if err := p.carregar(b); err != nil {
		return nil, false, err
	}
	return p, false, nil
}

// Girar cria uma versão nova da chave mestra e grava o arquivo.
func (p *ProvedorArquivo) Girar() error {
	if err := p.girar(); err != nil {
		return err
	}
	return p.salvar()
}

func (p *ProvedorArquivo) carregar(b []byte) error {
	var a arquivoChaves
	if err := json.Unmarshal(b, &a); err != nil {
		return fmt.Errorf("cofre: arquivo de chaves corrompido: %w", err)
	}
	for v, enc := range a.Chaves {
		n, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("cofre: versão inválida %q no arquivo de chaves", v)
		}
		k, err := base64.StdEncoding.DecodeString(enc)
		if err != nil || len(k) != tamanhoChave {
			return fmt.Errorf("cofre: chave da versão %d inválida", n)
		}
		p.chaves[n] = k
	}
	if _, ok := p.chaves[a.VersaoAtiva]; !ok {
		return fmt.Errorf("cofre: versão ativa %d não existe no arquivo de chaves", a.VersaoAtiva)
	}
	p.ativa = a.VersaoAtiva
	return nil
}

// salvar grava de forma atômica (arquivo temporário + rename) e com permissão 0600.
func (p *ProvedorArquivo) salvar() error {
	p.mu.RLock()
	a := arquivoChaves{VersaoAtiva: p.ativa, Chaves: map[string]string{}}
	for v, k := range p.chaves {
		a.Chaves[strconv.Itoa(v)] = base64.StdEncoding.EncodeToString(k)
	}
	p.mu.RUnlock()

	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(p.caminho)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("cofre: criando %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".chave-mestra-*")
	if err != nil {
		return fmt.Errorf("cofre: %w", err)
	}
	defer os.Remove(tmp.Name()) // no sucesso o rename já o moveu; no erro, limpa
	if err := tmp.Chmod(0o600); err != nil && runtime.GOOS != "windows" {
		tmp.Close()
		return fmt.Errorf("cofre: %w", err)
	}
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("cofre: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("cofre: %w", err)
	}
	if err := os.Rename(tmp.Name(), p.caminho); err != nil {
		return fmt.Errorf("cofre: gravando %s: %w", p.caminho, err)
	}
	return nil
}

// conferirPermissao recusa arquivo legível por grupo/outros (Unix). No Windows a
// proteção vem da ACL do diretório de dados do serviço.
func conferirPermissao(caminho string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	st, err := os.Stat(caminho)
	if err != nil {
		return fmt.Errorf("cofre: %w", err)
	}
	if st.Mode().Perm()&0o077 != 0 {
		return ErrPermissaoAberta
	}
	return nil
}
