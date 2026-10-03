package selfupdate

import (
	"fmt"
	"strconv"
	"strings"
)

// Comparação de versão do agente.
//
// Só existe aqui para responder UMA pergunta: "a versão que o painel mandou é
// mais nova que a que estou rodando?". Não é um semver completo de propósito —
// as versões do agente são `MAIOR.MENOR.PATCH` numéricas, e um parser tolerante
// demais aceitaria lixo (`latest`, `v0.9.0-dirty`, uma página de erro do proxy)
// como se fosse versão e dispararia uma troca de binário em cima disso.

// versao é uma versão do agente já decomposta em números.
type versao struct{ maior, menor, patch int }

// parseVersao lê "0.8.0" ou "v0.8.0". Recusa qualquer outra coisa.
//
// O defeito concreto que a recusa evita: se um proxy mal configurado devolver
// HTML no lugar do JSON e algum campo virar "<!doctype html>", um parser
// permissivo poderia interpretá-lo como versão 0 e o agente concluiria coisas
// sobre uma versão que não existe. Aqui isso vira erro, e erro é ignorado — o
// agente segue coletando na versão atual.
func parseVersao(s string) (versao, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimPrefix(s, "v")
	if s == "" {
		return versao{}, fmt.Errorf("versão vazia")
	}
	partes := strings.Split(s, ".")
	if len(partes) != 3 {
		return versao{}, fmt.Errorf("versão %q não está no formato MAIOR.MENOR.PATCH", s)
	}
	var v versao
	destinos := []*int{&v.maior, &v.menor, &v.patch}
	for i, p := range partes {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return versao{}, fmt.Errorf("versão %q tem componente inválido %q", s, p)
		}
		*destinos[i] = n
	}
	return v, nil
}

// maisNovaQue diz se `alvo` é estritamente mais nova que `atual`.
//
// "Estritamente" é a regra inteira desta função e o motivo de ela existir
// separada. Duas coisas que ela impede, ambas capazes de derrubar a coleta:
//
//   - Downgrade automático. Se o painel publicar por engano um artefato antigo
//     (rollback de deploy, dist meio sincronizado), uma frota que aceita
//     "diferente = atualizar" volta inteira para trás sozinha. Voltar versão é
//     decisão de operador, feita com pin, nunca do agente.
//
//   - Reinstalar a MESMA versão. Como o agente estaga o binário e sai para o
//     systemd promovê-lo, "atualizar" para a versão em execução seria um laço de
//     reinício permanente: baixa, sai, sobe igual, baixa de novo. O host pararia
//     de coletar sem nunca mudar de versão.
func maisNovaQue(alvo, atual string) (bool, error) {
	va, err := parseVersao(alvo)
	if err != nil {
		return false, fmt.Errorf("versão desejada: %w", err)
	}
	vc, err := parseVersao(atual)
	if err != nil {
		return false, fmt.Errorf("versão em execução: %w", err)
	}
	switch {
	case va.maior != vc.maior:
		return va.maior > vc.maior, nil
	case va.menor != vc.menor:
		return va.menor > vc.menor, nil
	default:
		return va.patch > vc.patch, nil
	}
}
