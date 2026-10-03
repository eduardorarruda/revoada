//go:build !windows

package logtail

import (
	"os"
	"syscall"
)

// fileInode devolve o inode do arquivo, ou 0 se o SO não o expõe. É o que permite
// distinguir "mesmo arquivo, cresceu" de "arquivo novo no mesmo caminho" — a
// diferença entre continuar de onde parou e reler do zero após uma rotação.
func fileInode(fi os.FileInfo) uint64 {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok || st == nil {
		return 0
	}
	return uint64(st.Ino)
}
