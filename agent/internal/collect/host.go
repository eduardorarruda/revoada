// Package collect coleta métricas do host via gopsutil.
package collect

import (
	"context"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// Point é um ponto de métrica coletado.
type Point struct {
	Name    string
	Value   float64
	Counter bool              // true = monotônico (Sum), false = Gauge
	Labels  map[string]string // atributos por datapoint (ex.: {"mount": "/"}); nil = sem labels
	At      int64             // UnixNano do instante da leitura; 0 = hora do envio
}

// Carimbar marca com `em` os pontos ainda sem hora de leitura. Quem coleta chama
// logo depois de ler: o envio só acontece depois de TODOS os coletores (containers
// têm orçamento de 10 s), e carimbar ali punha o número segundos depois de medido.
func Carimbar(pts []Point, em time.Time) []Point {
	ns := em.UnixNano()
	for i := range pts {
		if pts[i].At == 0 {
			pts[i].At = ns
		}
	}
	return pts
}

// HostInfo resume o inventário do host (enviado como labels/atributos de recurso).
type HostInfo struct {
	Hostname string
	OS       string
	Platform string
	Kernel   string
	Arch     string
	Uptime   uint64
}

// Info devolve o inventário do host.
func Info(ctx context.Context) (HostInfo, error) {
	i, err := host.InfoWithContext(ctx)
	if err != nil {
		return HostInfo{}, err
	}
	return HostInfo{
		Hostname: i.Hostname, OS: i.OS, Platform: i.Platform,
		Kernel: i.KernelVersion, Arch: i.KernelArch, Uptime: i.Uptime,
	}, nil
}

// Host coleta um snapshot de métricas do host. Se allFilesystems for true, emite
// disco por montagem (todos os filesystems físicos); se false, apenas o `/`.
//
// `info` é o inventário que o chamador JÁ coletou neste ciclo (via Info). Ele entra
// aqui só para o `system.uptime`: antes esta função chamava `host.Info` de novo, e
// no Linux essa chamada faz um Readdirnames(-1) de /proc inteiro — e, em hosts sem
// /etc/lsb-release mas com /usr/bin/lsb_release (AlmaLinux/CloudLinux, o padrão em
// cPanel), ainda um fork+exec. Somada à varredura de PIDs, dava três passadas em
// /proc por ciclo num servidor que pode ter milhares de processos, para obter um
// número que o chamador já tinha na mão.
func Host(ctx context.Context, allFilesystems bool, info HostInfo) []Point {
	var pts []Point
	add := func(n string, v float64) { pts = append(pts, Point{Name: n, Value: v}) }
	addC := func(n string, v float64) { pts = append(pts, Point{Name: n, Value: v, Counter: true}) }

	// CPU: fração do tempo ocupada desde a coleta anterior — o intervalo inteiro,
	// não um instantâneo dentro dele. É o que faz o número bater com o `top` e com
	// o painel do provedor. Ver cpu.go para por que a janela curta não servia.
	//
	// `system.cpu.steal` sai da MESMA diferença: é a parcela desse mesmo intervalo
	// que o hipervisor levou.
	//
	// Desde a 0.8.1 as duas séries são DISJUNTAS: `utilization` é a CPU que a
	// máquina consumiu de fato (sem steal) e `steal` é o que ela pediu e não
	// recebeu. Antes `utilization` continha o steal (convenção do node_exporter).
	//
	// O motivo é comparabilidade: medido no srv-02, o painel publicava 13,9%
	// enquanto o painel do provedor mostrava ~10,4% para o mesmo instante, porque
	// 9,9 pontos eram steal. Dois painéis discordando sobre a mesma máquina fazem o
	// operador desconfiar dos dois. Quem quiser a leitura antiga ("quanto tempo meus
	// processos passaram sem poder rodar") soma as duas séries — elas saem do mesmo
	// intervalo, então a soma fecha.
	//
	// Sem base para o steal não sai NENHUMA das duas: publicar o ocupado sob o nome
	// `utilization` seria publicar o número velho com o rótulo novo, e a série
	// ficaria com pontos inflados que ninguém consegue identificar depois.
	// `system.cpu.utilization.total` é a SOMA das duas — o ocupado bruto, que é a
	// leitura que a 0.8.0 publicava como `utilization`. Ela existe como série
	// própria por um motivo prático, não por gosto de redundância: o motor de
	// alertas e os painéis aceitam UMA métrica por regra/painel, sem expressão.
	// Sem esta série, "me avise quando consumo + roubo passarem de X" não é
	// exprimível — e é justamente o alerta que protege contra o buraco aberto pela
	// 0.8.1: num VPS estrangulado a CPU consumida marca pouco enquanto a máquina
	// sufoca.
	if r, ok := defaultCPUMeter.ReadAll(ctx); ok {
		if pura, ok := r.Pura(); ok {
			add("system.cpu.utilization", pura)
			add("system.cpu.steal", r.Steal)
			add("system.cpu.utilization.total", r.Busy)
		}
	}
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		add("system.memory.utilization", vm.UsedPercent)
		add("system.memory.used", float64(vm.Used))
		add("system.memory.total", float64(vm.Total))
		add("system.memory.available", float64(vm.Available))
	}
	// Swap: só emite se a máquina tiver swap configurado (total>0), para não poluir.
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil && sw.Total > 0 {
		add("system.paging.utilization", sw.UsedPercent)
		add("system.paging.used", float64(sw.Used))
		add("system.paging.total", float64(sw.Total))
	}
	// Disco por montagem: itera os filesystems físicos (Partitions(false) já filtra
	// pseudo/efêmeros). Cada datapoint carrega o atributo `mount` = mountpoint.
	pts = append(pts, filesystems(ctx, allFilesystems)...)
	if l, err := load.AvgWithContext(ctx); err == nil {
		add("system.cpu.load_average.1m", l.Load1)
		add("system.cpu.load_average.5m", l.Load5)
		add("system.cpu.load_average.15m", l.Load15)
	}
	// Rede: só as interfaces FÍSICAS. Ver interfacesVirtuais para o porquê.
	if sent, recv, ok := trafegoFisico(ctx); ok {
		addC("system.network.io.bytes_sent", sent)
		addC("system.network.io.bytes_recv", recv)
	}
	// Contagem de processos: conta os PIDs visíveis do sistema.
	if pids, err := process.PidsWithContext(ctx); err == nil {
		add("system.processes.count", float64(len(pids)))
	}
	// Uptime vem do inventário que o chamador já coletou — nada de reler /proc.
	if info.Uptime > 0 {
		add("system.uptime", float64(info.Uptime))
	}
	return pts
}

// interfacesVirtuais lista PREFIXOS de nome de interface cujo tráfego não é
// entrada/saída da máquina. Somar tudo (o que `IOCounters(pernic=false)` faz)
// inflou a medida 109x em produção: o painel reportou 12,2 MB/min de saída
// enquanto a eth0 real mandou 111 KB/min. A causa é dupla — o loopback entra
// inteiro, e cada pacote trocado entre containers é contado 2 a 4 vezes (veth do
// emissor + bridge + veth do receptor), tudo dentro do mesmo host.
//
// Por família, e por que cada uma entra na lista:
//   - lo      loopback: tráfego que nunca sai da máquina (inclui lo0 no BSD/macOS).
//   - docker  docker0, a bridge padrão do Docker; espelha o que os containers trocam.
//   - br-     bridges de redes definidas pelo usuário no Docker (`br-<hash>`).
//   - veth    a ponta hospedeira de cada par veth; é a MESMA passagem já contada
//     na bridge, e existem duas por conversa entre containers.
//   - virbr   bridges do libvirt (VMs locais em KVM/QEMU).
//   - tun/tap interfaces virtuais de VPN e de VM; o tráfego real já foi contado na
//     interface física por onde o túnel saiu — contá-las é contar duas vezes.
//   - tailscale / wg / zt  os três túneis que NÃO se chamam tun/tap: Tailscale cria
//     `tailscale0`, o WireGuard `wg0` e o ZeroTier `ztXXXXXXXX`. Mesma dupla
//     contagem dos tun/tap — o byte já passou pela interface física.
//   - cni     bridges do CNI (containerd/CRI-O e afins).
//   - flannel / cali / kube-  overlays e interfaces do Kubernetes (flannel.1,
//     caliXXXX da Calico, kube-bridge/kube-dummy-if do kube-router).
//   - nerdctl a bridge do nerdctl (containerd sem Docker).
//
// Deliberadamente NÃO entram `br0`, `bond`, `eth`, `en`, `wl`, `ppp` nem VLANs
// (`eth0.100`): essas carregam tráfego que realmente atravessa a máquina. O
// critério é sempre esse — se o byte cruzou a fronteira do servidor, conta.
var interfacesVirtuais = []string{
	"lo", "docker", "br-", "veth", "virbr",
	"tun", "tap", "cni", "flannel", "kube-", "cali", "nerdctl",
	"tailscale", "wg", "zt",
}

// interfaceFisica diz se o tráfego de uma interface deve ser contado como
// entrada/saída da máquina (função pura, testável).
func interfaceFisica(nome string) bool {
	n := strings.ToLower(strings.TrimSpace(nome))
	if n == "" {
		return false
	}
	for _, p := range interfacesVirtuais {
		if strings.HasPrefix(n, p) {
			return false
		}
	}
	return true
}

// trafegoFisico soma os contadores das interfaces físicas. Devolve ok=false quando
// nenhuma interface física foi encontrada: emitir 0 num Counter seria lido como
// reinício do contador e produziria um pico falso no gráfico de taxa.
func trafegoFisico(ctx context.Context) (sent, recv float64, ok bool) {
	ios, err := net.IOCountersWithContext(ctx, true)
	if err != nil {
		return 0, 0, false
	}
	for _, io := range ios {
		if !interfaceFisica(io.Name) {
			continue
		}
		sent += float64(io.BytesSent)
		recv += float64(io.BytesRecv)
		ok = true
	}
	return sent, recv, ok
}

// pseudoFSTypes são filesystems read-only/virtuais que NÃO representam disco do
// host e sempre reportam ~100% de uso — inclui-los faria o "disco" do card do Wall
// bater 100% em qualquer host com snaps (squashfs de /dev/loopN é o caso típico do
// Ubuntu). Ficam de fora da coleta de disco.
var pseudoFSTypes = map[string]bool{
	"squashfs": true, "iso9660": true, "udf": true,
	"overlay": true, "tmpfs": true, "devtmpfs": true, "ramfs": true,
	"autofs": true, "fuse.snapfuse": true,
}

// filesystems coleta uso de disco por montagem. Em modo root, mantém apenas `/`.
// Emite UMA montagem por dispositivo físico: subvolumes btrfs e bind mounts
// compartilham o mesmo device e reportam o mesmo uso — emitir todos poluiria o
// painel com linhas idênticas. Escolhe-se o mountpoint mais curto por device (ex.:
// "/" em vez de "/home"); discos genuinamente distintos (ex.: "/boot") são mantidos.
// Filesystems pseudo/read-only (squashfs de snaps etc.) e total==0 são ignorados.
func filesystems(ctx context.Context, allFilesystems bool) []Point {
	parts, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return nil
	}
	// device -> montagem escolhida; montagens sem device são mantidas à parte.
	chosen := map[string]string{}
	mounts := map[string]bool{}
	for _, part := range parts {
		mp := part.Mountpoint
		if !allFilesystems && mp != "/" {
			continue
		}
		// Descarta pseudo-FS (por fstype) e loop devices de snap (por device).
		if pseudoFSTypes[strings.ToLower(part.Fstype)] || strings.HasPrefix(part.Device, "/dev/loop") {
			continue
		}
		if part.Device == "" {
			mounts[mp] = true
			continue
		}
		if cur, ok := chosen[part.Device]; !ok || betterMount(mp, cur) {
			chosen[part.Device] = mp
		}
	}
	for _, mp := range chosen {
		mounts[mp] = true
	}
	var pts []Point
	for mp := range mounts {
		du, err := disk.UsageWithContext(ctx, mp)
		if err != nil || du.Total == 0 {
			continue
		}
		lbl := map[string]string{"mount": mp}
		// `available` é o par que faltava para as três séries fecharem entre si.
		// `utilization` (du.UsedPercent) é Used/(Used+Available) — a mesma conta do
		// `df`, que ignora os blocos reservados ao root. Já `total` é o tamanho BRUTO
		// do device, reservados incluídos. Quem recalculava used/total na tela obtinha
		// outro número: em /boot deu 13,50% contra os 14,51% de `utilization`, no
		// mesmo ciclo. Com `available` a tela pode fazer used/(used+available) e
		// bater exatamente com a porcentagem. `total` e `utilization` seguem como
		// estavam — a série histórica não pode mudar de significado.
		pts = append(pts,
			Point{Name: "system.filesystem.utilization", Value: du.UsedPercent, Labels: lbl},
			Point{Name: "system.filesystem.used", Value: float64(du.Used), Labels: lbl},
			Point{Name: "system.filesystem.total", Value: float64(du.Total), Labels: lbl},
			Point{Name: "system.filesystem.available", Value: float64(du.Free), Labels: lbl},
		)
	}
	return pts
}

// betterMount decide se `a` é preferível a `b` como representante de um device:
// o mountpoint mais curto vence (ex.: "/" sobre "/home"); empate → lexicográfico.
func betterMount(a, b string) bool {
	if len(a) != len(b) {
		return len(a) < len(b)
	}
	return a < b
}
