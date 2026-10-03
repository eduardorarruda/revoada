package collect

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/shirou/gopsutil/v4/disk"
)

// infoDeTeste devolve o inventário do host como o chamador real faria: uma única
// leitura por ciclo, repassada a Host. É de lá que sai o `system.uptime`.
func infoDeTeste(t *testing.T) HostInfo {
	t.Helper()
	info, err := Info(context.Background())
	if err != nil {
		t.Skipf("sem inventário de host neste ambiente: %v", err)
	}
	return info
}

// findByName devolve os pontos com o nome dado.
func findByName(pts []Point, name string) []Point {
	var out []Point
	for _, p := range pts {
		if p.Name == name {
			out = append(out, p)
		}
	}
	return out
}

// Desde a 0.8.1 `system.cpu.utilization` e `system.cpu.steal` são DISJUNTAS: a
// primeira é o que a máquina consumiu, a segunda é o que o hipervisor levou. As
// duas andam juntas — se o steal não tem base, nenhuma das duas sai, senão a série
// ganharia pontos inflados (o ocupado sob o rótulo da pura) sem marca nenhuma.
func TestCPUEStealSaemJuntasEDisjuntas(t *testing.T) {
	// duas coletas: a primeira estabelece a base do medidor de intervalo.
	Host(context.Background(), false, infoDeTeste(t))
	pts := Host(context.Background(), false, infoDeTeste(t))

	util := findByName(pts, "system.cpu.utilization")
	steal := findByName(pts, "system.cpu.steal")
	total := findByName(pts, "system.cpu.utilization.total")
	if len(util) != len(steal) || len(util) != len(total) {
		t.Fatalf("as três séries têm de sair juntas: utilization=%d steal=%d total=%d",
			len(util), len(steal), len(total))
	}
	if len(util) == 0 {
		t.Skip("sem leitura de CPU neste ambiente")
	}
	if util[0].Value < 0 || util[0].Value > 100 {
		t.Errorf("utilization fora da faixa: %v", util[0].Value)
	}
	// A identidade que sustenta o alerta sobre a soma: total == pura + steal. Se ela
	// não fechar, a regra "me avise quando a máquina sufocar" mede outra coisa.
	if d := math.Abs(total[0].Value - (util[0].Value + steal[0].Value)); d > 0.001 {
		t.Errorf("total (%v) != utilization (%v) + steal (%v)", total[0].Value, util[0].Value, steal[0].Value)
	}
	// E o total é o teto: a pura nunca pode passar dele.
	if util[0].Value > total[0].Value+0.001 {
		t.Errorf("utilization (%v) passou do total (%v)", util[0].Value, total[0].Value)
	}
}

func TestHostFilesystemsHaveMountLabel(t *testing.T) {
	// Dentro de um container de build (docker run golang …) não há partição física
	// visível: a raiz é overlay e o resto são bind mounts de subdiretórios, que o
	// gopsutil (Partitions(false)) não lista. Sem partição não há o que medir — é o
	// ambiente, não o coletor. Foi a causa da falha "esperado ao menos um ponto" que
	// só acontecia ao rodar os testes no container.
	if ps, err := disk.PartitionsWithContext(context.Background(), false); err == nil && len(ps) == 0 {
		t.Skip("nenhuma partição física visível (container de build); nada a medir")
	}
	pts := Host(context.Background(), true, infoDeTeste(t))

	// Deve haver ao menos uma utilização de filesystem, e todo datapoint de
	// filesystem carrega o atributo `mount`.
	for _, name := range []string{
		"system.filesystem.utilization",
		"system.filesystem.used",
		"system.filesystem.total",
		"system.filesystem.available",
	} {
		fs := findByName(pts, name)
		if len(fs) == 0 {
			t.Fatalf("esperado ao menos um ponto %q", name)
		}
		for _, p := range fs {
			if p.Labels["mount"] == "" {
				t.Errorf("%s sem label mount: %+v", name, p)
			}
			if p.Counter {
				t.Errorf("%s não deveria ser counter", name)
			}
		}
	}
}

func TestHostRootOnlyFilesystem(t *testing.T) {
	pts := Host(context.Background(), false, infoDeTeste(t))
	fs := findByName(pts, "system.filesystem.utilization")
	if len(fs) == 0 {
		t.Skip("sem filesystem `/` no ambiente de teste")
	}
	for _, p := range fs {
		if p.Labels["mount"] != "/" {
			t.Errorf("modo root deveria emitir só `/`, veio mount=%q", p.Labels["mount"])
		}
	}
}

func TestHostProcessesCount(t *testing.T) {
	pts := Host(context.Background(), true, infoDeTeste(t))
	proc := findByName(pts, "system.processes.count")
	if len(proc) != 1 {
		t.Fatalf("esperado exatamente 1 ponto system.processes.count, veio %d", len(proc))
	}
	if proc[0].Value <= 0 {
		t.Errorf("contagem de processos deveria ser > 0, veio %v", proc[0].Value)
	}
	if proc[0].Labels != nil {
		t.Errorf("system.processes.count não deveria ter labels")
	}
}

func TestHostSwapOnlyWhenPresent(t *testing.T) {
	pts := Host(context.Background(), true, infoDeTeste(t))
	total := findByName(pts, "system.paging.total")
	util := findByName(pts, "system.paging.utilization")
	used := findByName(pts, "system.paging.used")

	// Swap é opcional: ou os três pontos existem (máquina com swap), ou nenhum.
	if len(total) == 0 {
		if len(util) != 0 || len(used) != 0 {
			t.Errorf("sem swap total mas com util/used: util=%d used=%d", len(util), len(used))
		}
		return
	}
	if total[0].Value <= 0 {
		t.Errorf("system.paging.total emitido deveria ser > 0, veio %v", total[0].Value)
	}
	if len(util) == 0 || len(used) == 0 {
		t.Errorf("com swap total, util e used deveriam existir: util=%d used=%d", len(util), len(used))
	}
}

// A porcentagem de disco tem de FECHAR com os bytes publicados no mesmo ciclo.
// `utilization` é used/(used+available) — a conta do `df`, que ignora os blocos
// reservados ao root — enquanto `total` é o tamanho bruto do device. Antes de
// existir `available`, quem recalculava used/total na tela obtinha outro número
// (13,50% contra 14,51% em /boot, medido em produção).
func TestFilesystemPorcentagemFechaComOsBytes(t *testing.T) {
	pts := Host(context.Background(), true, infoDeTeste(t))

	tipo := map[string]map[string]float64{} // mount -> nome curto -> valor
	for _, p := range pts {
		curto, ok := strings.CutPrefix(p.Name, "system.filesystem.")
		if !ok {
			continue
		}
		mp := p.Labels["mount"]
		if tipo[mp] == nil {
			tipo[mp] = map[string]float64{}
		}
		tipo[mp][curto] = p.Value
	}
	if len(tipo) == 0 {
		t.Skip("sem filesystem no ambiente de teste")
	}
	for mp, v := range tipo {
		base := v["used"] + v["available"]
		if base <= 0 {
			t.Errorf("%s: used+available deveria ser > 0, veio %v", mp, base)
			continue
		}
		calc := v["used"] / base * 100
		if diff := math.Abs(calc - v["utilization"]); diff > 0.01 {
			t.Errorf("%s: used/(used+available)=%.4f%% não fecha com utilization=%.4f%% (%.4f pp)",
				mp, calc, v["utilization"], diff)
		}
		// E `total` continua sendo o tamanho BRUTO: >= used+available (a diferença
		// são os blocos reservados ao root). A série antiga não pode ter mudado.
		if v["total"]+1 < base {
			t.Errorf("%s: total=%v deveria ser >= used+available=%v", mp, v["total"], base)
		}
	}
}

// Rede tem de contar só o que atravessa a fronteira da máquina. Somar tudo (o que
// IOCounters(pernic=false) fazia) inflou a medida 109x em produção.
func TestInterfaceFisica(t *testing.T) {
	casos := []struct {
		nome   string
		quero  bool
		porque string
	}{
		{"eth0", true, "interface física clássica"},
		{"eth0.100", true, "VLAN sobre a física: o byte sai da máquina"},
		{"enp3s0", true, "nome previsível do systemd"},
		{"ens18", true, "nome previsível em VM"},
		{"wlan0", true, "wifi também sai da máquina"},
		{"bond0", true, "agregação de físicas"},
		{"br0", true, "bridge do host feita à mão NÃO é do Docker; carrega tráfego real"},
		{"ppp0", true, "link discado/PPPoE sai da máquina"},
		{"lo", false, "loopback nunca sai da máquina"},
		{"lo0", false, "loopback do BSD/macOS"},
		{"docker0", false, "bridge padrão do Docker: espelha o que os containers trocam"},
		{"br-1a2b3c4d5e6f", false, "bridge de rede definida pelo usuário no Docker"},
		{"veth1a2b3c4", false, "ponta hospedeira do par veth: dupla contagem por conversa"},
		{"virbr0", false, "bridge do libvirt"},
		{"tun0", false, "VPN: o tráfego real já foi contado na física por onde o túnel saiu"},
		{"tap0", false, "interface virtual de VM"},
		{"cni0", false, "bridge do CNI"},
		{"flannel.1", false, "overlay do flannel"},
		{"kube-bridge", false, "interface do kube-router"},
		{"cali1234abcd", false, "veth da Calico"},
		{"nerdctl0", false, "bridge do nerdctl"},
		{"tailscale0", false, "túnel do Tailscale: não se chama tun, mas conta duas vezes igual"},
		{"wg0", false, "túnel do WireGuard: mesmo caso do tailscale0"},
		{"ztabcdef12", false, "túnel do ZeroTier"},
		{"wlp2s0", true, "wifi previsível NÃO pode cair no prefixo 'wg'"},
		{"zram0", true, "não é interface de rede, mas se aparecer não pode cair no prefixo 'zt'"},
		{"DOCKER0", false, "o nome vem do kernel; a comparação não pode depender de caixa"},
		{"  eth0  ", true, "espaço em volta não muda a natureza da interface"},
		{"", false, "nome vazio não é interface"},
	}
	for _, c := range casos {
		t.Run(c.nome, func(t *testing.T) {
			if got := interfaceFisica(c.nome); got != c.quero {
				t.Errorf("interfaceFisica(%q) = %v, quero %v — %s", c.nome, got, c.quero, c.porque)
			}
		})
	}
}

// As duas séries de rede continuam existindo, com os mesmos nomes e como Counter.
func TestRedeMantemNomeETipoCounter(t *testing.T) {
	pts := Host(context.Background(), true, infoDeTeste(t))
	for _, nome := range []string{"system.network.io.bytes_sent", "system.network.io.bytes_recv"} {
		got := findByName(pts, nome)
		if len(got) == 0 {
			t.Skipf("sem interface física em %s neste ambiente", nome)
		}
		if len(got) != 1 {
			t.Fatalf("%s deveria ser um único ponto somado, veio %d", nome, len(got))
		}
		if !got[0].Counter {
			t.Errorf("%s tem de continuar Counter (acumulado), não Gauge", nome)
		}
	}
}

// system.uptime agora vem do HostInfo que o chamador já coletou — nada de reler
// /proc uma segunda vez só para obter um número que já estava na mão.
func TestUptimeVemDoHostInfo(t *testing.T) {
	info := HostInfo{Hostname: "teste", Uptime: 4242}
	pts := Host(context.Background(), false, info)

	up := findByName(pts, "system.uptime")
	if len(up) != 1 {
		t.Fatalf("esperado exatamente 1 ponto system.uptime, veio %d", len(up))
	}
	if up[0].Value != 4242 {
		t.Errorf("uptime deveria vir do HostInfo (4242), veio %v", up[0].Value)
	}
	// Sem inventário válido não há o que afirmar: melhor faltar que publicar 0,
	// que na tela viraria "a máquina acabou de reiniciar".
	if got := findByName(Host(context.Background(), false, HostInfo{}), "system.uptime"); len(got) != 0 {
		t.Errorf("sem HostInfo não deveria emitir system.uptime, veio %+v", got)
	}
}

func TestHostCoreMetricsNoLabels(t *testing.T) {
	pts := Host(context.Background(), true, infoDeTeste(t))
	// As métricas de host inteiro (não-disco) não devem carregar labels de datapoint.
	for _, p := range pts {
		switch p.Name {
		case "system.filesystem.utilization", "system.filesystem.used",
			"system.filesystem.total", "system.filesystem.available":
			// disco usa labels — ok
		default:
			if p.Labels != nil {
				t.Errorf("%s não deveria ter labels: %+v", p.Name, p.Labels)
			}
		}
	}
}
