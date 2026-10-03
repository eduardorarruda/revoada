// Package entrada lê e responde JSON nos handlers novos de forma estrita: campo
// desconhecido é erro (pega erro de digitação e tentativa de injetar campo), JSON com
// lixo depois do objeto é erro, e erros de validação voltam com o campo.
package entrada

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/eduardorarruda/revoada/core/validacao"
)

// LerJSON decodifica o corpo em `dst` de forma estrita. Em erro, já respondeu 400.
func LerJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		var grande *http.MaxBytesError
		if errors.As(err, &grande) {
			http.Error(w, "corpo grande demais", http.StatusRequestEntityTooLarge)
			return false
		}
		http.Error(w, "JSON inválido: "+err.Error(), http.StatusBadRequest)
		return false
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		http.Error(w, "JSON inválido: conteúdo depois do objeto", http.StatusBadRequest)
		return false
	}
	return true
}

// ResponderJSON escreve `v` com o status.
func ResponderJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// ErroValidacao responde 422 com o campo e a mensagem (a tela marca o campo).
// Devolve false se o erro não era de validação (o chamador trata).
func ErroValidacao(w http.ResponseWriter, err error) bool {
	var ev validacao.Erro
	if !errors.As(err, &ev) {
		return false
	}
	ResponderJSON(w, http.StatusUnprocessableEntity, ev)
	return true
}
