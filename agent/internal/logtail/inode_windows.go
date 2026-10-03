//go:build windows

package logtail

import "os"

// fileInode: no Windows não há inode estável e barato de obter pelo os.FileInfo.
// Devolver 0 é explícito — a detecção de rotação cai de volta para o critério de
// tamanho, que é o que o agente já fazia. Nada regride; só não ganha o critério
// melhor. O coletor de logs em Windows segue arquivos de aplicação, onde a
// rotação por cópia+truncamento é rara.
func fileInode(os.FileInfo) uint64 { return 0 }
