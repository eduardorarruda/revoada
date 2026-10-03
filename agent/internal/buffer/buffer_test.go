package buffer

import (
	"path/filepath"
	"testing"
)

func TestPutListDrainOrder(t *testing.T) {
	dir := t.TempDir()
	b, err := New(dir, 100)
	if err != nil {
		t.Fatal(err)
	}
	// grava 3 lotes com timestamps crescentes.
	for i, ts := range []int64{100, 200, 300} {
		if err := b.Put(ts, []byte{byte(i)}); err != nil {
			t.Fatal(err)
		}
	}
	files, err := b.List()
	if err != nil || len(files) != 3 {
		t.Fatalf("esperava 3 arquivos, obteve %d (%v)", len(files), err)
	}
	// ordem cronológica pelo nome (prefixo do timestamp).
	prev := ""
	for _, f := range files {
		if base := filepath.Base(f); base < prev {
			t.Errorf("fora de ordem: %s < %s", base, prev)
		} else {
			prev = base
		}
	}
	// remove um e confirma.
	if err := b.Remove(files[0]); err != nil {
		t.Fatal(err)
	}
	if files, _ = b.List(); len(files) != 2 {
		t.Fatalf("esperava 2 após remover, obteve %d", len(files))
	}
}

func TestEnforceCap(t *testing.T) {
	dir := t.TempDir()
	b, err := New(dir, 3) // teto de 3 lotes
	if err != nil {
		t.Fatal(err)
	}
	for ts := int64(1); ts <= 10; ts++ {
		if err := b.Put(ts, []byte("x")); err != nil {
			t.Fatal(err)
		}
	}
	files, _ := b.List()
	if len(files) != 3 {
		t.Fatalf("cap deveria manter 3, obteve %d", len(files))
	}
}
