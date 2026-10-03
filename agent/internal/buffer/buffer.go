// Package buffer é um WAL simples em disco: guarda lotes OTLP quando o gateway
// está fora e os reenvia quando volta (aceite: 10 min de gateway fora sem perda).
package buffer

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
)

type Buffer struct {
	dir     string
	maxFile int // teto de lotes guardados (descarta os mais antigos)
	seq     int64
}

func New(dir string, maxFiles int) (*Buffer, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if maxFiles <= 0 {
		maxFiles = 5000
	}
	return &Buffer{dir: dir, maxFile: maxFiles}, nil
}

// Put grava um lote (bytes OTLP protobuf). ts em nanos serve de ordenação.
func (b *Buffer) Put(tsNanos int64, data []byte) error {
	b.seq++
	name := strconv.FormatInt(tsNanos, 10) + "-" + strconv.FormatInt(b.seq, 10) + ".otlp"
	tmp := filepath.Join(b.dir, "."+name)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(b.dir, name)); err != nil {
		return err
	}
	b.enforceCap()
	return nil
}

// List devolve os arquivos de lote em ordem cronológica.
func (b *Buffer) List() ([]string, error) {
	entries, err := os.ReadDir(b.dir)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, e := range entries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".otlp" {
			files = append(files, filepath.Join(b.dir, e.Name()))
		}
	}
	sort.Strings(files) // nomes começam pelo timestamp → ordem cronológica
	return files, nil
}

func (b *Buffer) Read(path string) ([]byte, error) { return os.ReadFile(path) }
func (b *Buffer) Remove(path string) error         { return os.Remove(path) }

// enforceCap descarta os lotes mais antigos além do teto (evita encher o disco).
func (b *Buffer) enforceCap() {
	files, err := b.List()
	if err != nil {
		return
	}
	for len(files) > b.maxFile {
		_ = os.Remove(files[0])
		files = files[1:]
	}
}
