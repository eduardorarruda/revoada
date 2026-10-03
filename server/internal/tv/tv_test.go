package tv

import (
	"testing"

	"github.com/eduardorarruda/revoada/server/internal/authz"
	"github.com/eduardorarruda/revoada/server/internal/dashboards"
	"github.com/eduardorarruda/revoada/server/internal/store"
)

// TestHostsDoModeloPrendeAoHost: um dashboard "Visão do Host — X" tem filtro de host
// em TODO painel, então o token que aponta para ele fica preso a X. Era esta a
// promessa do modo TV ("somente-leitura por dashboard") que não estava sendo cumprida:
// medido em dev, um token do dashboard de UM host devolvia 64 cartões no /api/tv/wall,
// 64 alertas no /api/tv/status e séries de 6 hosts no /api/tv/query.
func TestHostsDoModeloPrendeAoHost(t *testing.T) {
	m := dashboards.StarterHost("dev-agent-host", "")
	hosts, restrito := hostsDoModelo(m, nil)
	if !restrito {
		t.Fatal("dashboard por-servidor tem de gerar escopo restrito")
	}
	if len(hosts) != 1 || hosts[0] != "dev-agent-host" {
		t.Fatalf("escopo esperado [dev-agent-host], veio %v", hosts)
	}
}

// TestHostsDoModeloServicePack: o starter de serviço (svc-<kind>-<host>) também prende
// ao host — inclusive nos painéis de CPU/RAM do host, que usam o mesmo filtro.
func TestHostsDoModeloServicePack(t *testing.T) {
	m, ok := dashboards.ServicePack("agent-test-host", "mysql")
	if !ok {
		t.Fatal("ServicePack(mysql) deveria existir")
	}
	hosts, restrito := hostsDoModelo(m, nil)
	if !restrito || len(hosts) != 1 || hosts[0] != "agent-test-host" {
		t.Fatalf("esperado escopo [agent-test-host] restrito, veio %v restrito=%v", hosts, restrito)
	}
}

// TestHostsDoModeloGenericoNaoRestringe: o dashboard genérico usa o placeholder $host
// (o servidor é escolhido na tela), logo não é de servidor nenhum em particular.
// Restringi-lo apagaria todos os gráficos de uma TV legítima.
func TestHostsDoModeloGenericoNaoRestringe(t *testing.T) {
	if _, restrito := hostsDoModelo(dashboards.StarterHostGeneric(), nil); restrito {
		t.Error("dashboard genérico ($host) não pode virar escopo restrito")
	}
}

// TestHostsDoModeloUmPainelDeFrotaLibera: basta UM painel sem filtro de host para a
// tela ser de frota. Restringir nesse caso esvaziaria um painel que o operador pôs ali
// de propósito — a regra erra para o lado de não quebrar a TV, e quem quer restrição
// aponta a TV para um dashboard por-servidor.
func TestHostsDoModeloUmPainelDeFrotaLibera(t *testing.T) {
	m := dashboards.Model{
		Panels: []dashboards.Panel{
			{ID: 1, Query: dashboards.PanelQuery{Metric: "system.cpu.utilization", Filters: map[string]string{"host": "a"}}},
			{ID: 2, Query: dashboards.PanelQuery{Metric: "system.memory.utilization"}}, // sem filtro: frota
		},
	}
	if _, restrito := hostsDoModelo(m, nil); restrito {
		t.Error("painel sem filtro de host torna a tela de frota")
	}
}

// TestEscopoNegaOutroHost: o Scope montado a partir do alvo do token bloqueia
// qualquer host que não seja o da TV — é o mesmo objeto que os construtores de SQL do
// ClickHouse consultam, então o bloqueio vale para toda consulta, não só para a tela.
func TestEscopoNegaOutroHost(t *testing.T) {
	scope := authz.NewScope(0, []store.ServerPerm{{Hostname: "dev-agent-host", CanView: true}})
	if !scope.CanView("dev-agent-host") {
		t.Error("a TV precisa ver o próprio host")
	}
	if scope.CanView("notebook-dev") {
		t.Error("a TV NÃO pode ver outro servidor")
	}
	if pred := scope.HostPredicate("labels['host']"); pred != "labels['host'] IN ('dev-agent-host')" {
		t.Errorf("predicado inesperado: %s", pred)
	}
}

// TestEscopoVazioBloqueiaTudo: alvo sem host reconhecível (dashboard apagado, por
// exemplo) gera escopo VAZIO, e escopo vazio é 1=0 — a TV não recebe dado de ninguém.
// Fail-closed: melhor uma TV em branco que uma TV com a frota inteira.
func TestEscopoVazioBloqueiaTudo(t *testing.T) {
	scope := authz.NewScope(0, nil)
	if scope.CanView("qualquer") {
		t.Error("escopo vazio não pode liberar host nenhum")
	}
	if pred := scope.HostPredicate("labels['host']"); pred != "1=0" {
		t.Errorf("escopo vazio deveria bloquear tudo, veio %q", pred)
	}
}
