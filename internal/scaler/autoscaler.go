package scaler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/Pzharyuk/proxmox-autoscaler/internal/proxmox"
	"github.com/Pzharyuk/proxmox-autoscaler/internal/talos"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	metricsv "k8s.io/metrics/pkg/client/clientset/versioned"
)

type Autoscaler struct {
	cfg          Config
	pve          *proxmox.Client
	talos        *talos.Manager
	k8s          kubernetes.Interface
	metrics      metricsv.Interface
	pendingSince map[string]time.Time // pod UID -> first seen
	idleSince    map[string]time.Time // node name -> first seen idle
	mu           sync.Mutex
	scalingUp    bool
	lastScaleUp  time.Time
	lastScaleDn  time.Time
}

func New(cfg Config) (*Autoscaler, error) {
	pve := proxmox.NewClient(cfg.ProxmoxHost, cfg.ProxmoxTokenID, cfg.ProxmoxTokenSecret, cfg.ProxmoxVerifySSL)

	talosMgr, err := talos.NewManager(talos.Config{
		WorkerConfigPath: cfg.TalosWorkerConfigPath,
		IPMask:           cfg.IPMask,
		Gateway:          cfg.IPGateway,
		Nameserver:       cfg.IPNameserver,
	})
	if err != nil {
		return nil, fmt.Errorf("talos manager: %w", err)
	}

	k8sCfg, err := rest.InClusterConfig()
	if err != nil {
		return nil, fmt.Errorf("k8s config: %w", err)
	}

	k8sClient, err := kubernetes.NewForConfig(k8sCfg)
	if err != nil {
		return nil, fmt.Errorf("k8s client: %w", err)
	}

	metricsClient, err := metricsv.NewForConfig(k8sCfg)
	if err != nil {
		return nil, fmt.Errorf("metrics client: %w", err)
	}

	return &Autoscaler{
		cfg:          cfg,
		pve:          pve,
		talos:        talosMgr,
		k8s:          k8sClient,
		metrics:      metricsClient,
		pendingSince: make(map[string]time.Time),
		idleSince:    make(map[string]time.Time),
	}, nil
}

func (a *Autoscaler) Run(ctx context.Context) {
	slog.Info("autoscaler starting",
		"min", a.cfg.MinWorkers,
		"max", a.cfg.MaxWorkers,
		"scaleUpAfter", a.cfg.ScaleUpPendingSeconds,
		"scaleDownAfter", a.cfg.ScaleDownIdleSeconds,
		"utilizationThreshold", a.cfg.ScaleDownUtilizationPct,
		"nodes", a.cfg.ProxmoxNodes,
		"poll", a.cfg.PollInterval,
	)

	ticker := time.NewTicker(time.Duration(a.cfg.PollInterval) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			slog.Info("autoscaler stopped")
			return
		case <-ticker.C:
			a.tick(ctx)
		}
	}
}

func (a *Autoscaler) tick(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("panic in autoscaler tick", "error", r)
		}
	}()

	a.cleanupStaleNodes(ctx)
	a.checkScaleUp(ctx)
	a.checkScaleDown(ctx)
}

func (a *Autoscaler) cleanupStaleNodes(ctx context.Context) {
	nodes, err := a.k8s.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: a.cfg.NodeLabel,
	})
	if err != nil {
		return
	}
	for _, node := range nodes.Items {
		ready := false
		for _, c := range node.Status.Conditions {
			if c.Type == corev1.NodeReady && c.Status == corev1.ConditionTrue {
				ready = true
			}
		}
		if !ready && time.Since(node.CreationTimestamp.Time) > 5*time.Minute {
			slog.Info("cleaning up stale NotReady node", "name", node.Name)
			_ = a.k8s.CoreV1().Nodes().Delete(ctx, node.Name, metav1.DeleteOptions{})
			// Also try to delete the VM
			vms, _ := a.pve.ListVMs()
			for _, vm := range vms {
				if vm.Name == node.Name {
					_ = a.pve.DeleteVM(vm.Node, vm.VMID)
					break
				}
			}
		}
	}
}

func (a *Autoscaler) managedNodeCount(ctx context.Context) int {
	nodes, err := a.k8s.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: a.cfg.NodeLabel,
	})
	if err != nil {
		return 0
	}
	return len(nodes.Items)
}

func (a *Autoscaler) checkScaleUp(ctx context.Context) {
	if a.scalingUp {
		return
	}

	pods, err := a.k8s.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: "status.phase=Pending",
	})
	if err != nil {
		slog.Error("list pending pods", "error", err)
		return
	}

	now := time.Now()
	currentUIDs := make(map[string]bool)

	for _, pod := range pods.Items {
		if !isUnschedulableDueToResources(&pod) {
			continue
		}
		uid := string(pod.UID)
		currentUIDs[uid] = true
		if _, ok := a.pendingSince[uid]; !ok {
			a.pendingSince[uid] = now
		}
	}

	// Clean resolved pods
	for uid := range a.pendingSince {
		if !currentUIDs[uid] {
			delete(a.pendingSince, uid)
		}
	}

	// Check if any pod pending long enough
	for _, since := range a.pendingSince {
		if now.Sub(since) >= time.Duration(a.cfg.ScaleUpPendingSeconds)*time.Second {
			count := len(a.pendingSince)
			slog.Info("pending pods detected, scaling up", "count", count, "pendingFor", now.Sub(since).Round(time.Second))
			go a.scaleUp(ctx)
			// Clear so we don't trigger again immediately
			a.pendingSince = make(map[string]time.Time)
			return
		}
	}
}

func isUnschedulableDueToResources(pod *corev1.Pod) bool {
	if pod.Status.Conditions == nil {
		return false
	}
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodScheduled && c.Reason == "Unschedulable" {
			if strings.Contains(c.Message, "Insufficient") {
				return true
			}
		}
	}
	return false
}

func (a *Autoscaler) scaleUp(ctx context.Context) {
	a.mu.Lock()
	a.scalingUp = true
	a.mu.Unlock()
	defer func() {
		a.mu.Lock()
		a.scalingUp = false
		a.mu.Unlock()
	}()

	if a.managedNodeCount(ctx) >= a.cfg.MaxWorkers {
		slog.Info("at max workers, not scaling up", "max", a.cfg.MaxWorkers)
		return
	}

	if time.Since(a.lastScaleUp) < time.Duration(a.cfg.PollInterval)*time.Second {
		slog.Info("scale-up cooldown active")
		return
	}

	vmid := a.nextVMID()
	ip := a.nextIP(ctx)
	pveNode := a.pickProxmoxNode()
	workerNum := vmid - a.cfg.VMIDStart + 1
	name := fmt.Sprintf("k8s-autoscale-%02d", workerNum)

	slog.Info("SCALE UP", "name", name, "vmid", vmid, "node", pveNode, "ip", ip)

	opts := proxmox.CreateVMOpts{
		VMID:      vmid,
		Name:      name,
		Node:      pveNode,
		Cores:     a.cfg.WorkerCores,
		MemoryMB:  a.cfg.WorkerMemoryMB,
		DiskGB:    a.cfg.WorkerDiskGB,
		Storage:   a.cfg.VMStorage,
		TalosISO:  a.cfg.TalosISO,
		Bridge:    a.cfg.VMBridge,
		VLAN:      a.cfg.VMVLAN,
		Tags:      a.cfg.VMTags,
	}
	if err := a.pve.CreateVM(opts); err != nil {
		slog.Error("create VM failed", "error", err)
		return
	}

	// Wait for VM creation task to finish
	time.Sleep(10 * time.Second)

	if err := a.pve.StartVM(pveNode, vmid); err != nil {
		slog.Error("start VM failed", "error", err)
		return
	}

	// Wait for Talos to boot from ISO and get a DHCP IP
	slog.Info("waiting for VM to boot from Talos ISO", "vmid", vmid)
	var dhcpIP string
	for i := 0; i < 36; i++ { // 3 minutes
		time.Sleep(5 * time.Second)
		if gotIP, err := a.pve.GetVMIP(pveNode, vmid); err == nil {
			dhcpIP = gotIP
			break
		}
	}
	if dhcpIP == "" {
		slog.Error("VM did not get IP, cleaning up", "vmid", vmid)
		_ = a.pve.DeleteVM(pveNode, vmid)
		return
	}

	slog.Info("VM booted", "vmid", vmid, "dhcpIP", dhcpIP, "staticIP", ip)

	configYAML, err := a.talos.GenerateWorkerConfig(name, ip)
	if err != nil {
		slog.Error("generate config failed", "error", err)
		_ = a.pve.DeleteVM(pveNode, vmid)
		return
	}

	if err := a.talos.ApplyConfig(dhcpIP, configYAML); err != nil {
		slog.Error("apply config failed", "error", err)
		_ = a.pve.DeleteVM(pveNode, vmid)
		return
	}

	// Wait for node to join
	slog.Info("waiting for node to join cluster", "name", name)
	for i := 0; i < 36; i++ {
		time.Sleep(5 * time.Second)
		_, err := a.k8s.CoreV1().Nodes().Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			// Label the node
			patch := fmt.Sprintf(`{"metadata":{"labels":{"%s":"true"}}}`, a.cfg.NodeLabel)
			_, err = a.k8s.CoreV1().Nodes().Patch(ctx, name, "application/strategic-merge-patch+json", []byte(patch), metav1.PatchOptions{})
			if err != nil {
				slog.Error("label node failed", "error", err)
			}
			slog.Info("node joined and labeled", "name", name)
			a.lastScaleUp = time.Now()
			return
		}
	}
	slog.Warn("node did not join within timeout", "name", name, "vmid", vmid)
	a.lastScaleUp = time.Now()
}

func (a *Autoscaler) checkScaleDown(ctx context.Context) {
	nodes, err := a.k8s.CoreV1().Nodes().List(ctx, metav1.ListOptions{
		LabelSelector: a.cfg.NodeLabel,
	})
	if err != nil {
		return
	}

	now := time.Now()
	for _, node := range nodes.Items {
		name := node.Name
		cpuPct, memPct, err := a.getNodeUtilization(ctx, name)
		if err != nil {
			delete(a.idleSince, name)
			continue
		}

		isIdle := cpuPct < a.cfg.ScaleDownUtilizationPct && memPct < a.cfg.ScaleDownUtilizationPct

		if isIdle {
			if _, ok := a.idleSince[name]; !ok {
				a.idleSince[name] = now
				slog.Debug("node idle", "name", name, "cpu", cpuPct, "mem", memPct)
			} else if now.Sub(a.idleSince[name]) >= time.Duration(a.cfg.ScaleDownIdleSeconds)*time.Second {
				slog.Info("SCALE DOWN", "name", name, "cpu", cpuPct, "mem", memPct, "idleFor", now.Sub(a.idleSince[name]).Round(time.Second))
				a.scaleDown(ctx, name)
				return
			}
		} else {
			delete(a.idleSince, name)
		}
	}
}

func (a *Autoscaler) scaleDown(ctx context.Context, nodeName string) {
	if a.managedNodeCount(ctx) <= a.cfg.MinWorkers {
		slog.Info("at min workers, not scaling down", "min", a.cfg.MinWorkers)
		return
	}

	if time.Since(a.lastScaleDn) < time.Duration(a.cfg.PollInterval)*time.Second {
		slog.Info("scale-down cooldown active")
		return
	}

	// Cordon
	patch := `{"spec":{"unschedulable":true}}`
	_, err := a.k8s.CoreV1().Nodes().Patch(ctx, nodeName, "application/strategic-merge-patch+json", []byte(patch), metav1.PatchOptions{})
	if err != nil {
		slog.Error("cordon failed", "node", nodeName, "error", err)
		return
	}
	slog.Info("cordoned node", "name", nodeName)

	// Drain
	pods, _ := a.k8s.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: fmt.Sprintf("spec.nodeName=%s", nodeName),
	})
	for _, pod := range pods.Items {
		if isDaemonSetPod(&pod) {
			continue
		}
		eviction := &policyv1.Eviction{
			ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: pod.Namespace},
			DeleteOptions: &metav1.DeleteOptions{
				GracePeriodSeconds: int64Ptr(30),
			},
		}
		_ = a.k8s.PolicyV1().Evictions(pod.Namespace).Evict(ctx, eviction)
	}
	slog.Info("drained node", "name", nodeName)
	time.Sleep(30 * time.Second)

	// Get node IP before deleting
	node, _ := a.k8s.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	_ = a.k8s.CoreV1().Nodes().Delete(ctx, nodeName, metav1.DeleteOptions{})
	slog.Info("deleted k8s node", "name", nodeName)

	// Find and delete Proxmox VM
	vms, _ := a.pve.ListVMs()
	for _, vm := range vms {
		if vm.Name == nodeName {
			_ = a.pve.DeleteVM(vm.Node, vm.VMID)
			break
		}
	}

	_ = node // suppress unused
	delete(a.idleSince, nodeName)
	a.lastScaleDn = time.Now()
}

func (a *Autoscaler) getNodeUtilization(ctx context.Context, nodeName string) (cpuPct, memPct float64, err error) {
	nodeMetrics, err := a.metrics.MetricsV1beta1().NodeMetricses().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return 0, 0, err
	}

	node, err := a.k8s.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
	if err != nil {
		return 0, 0, err
	}

	cpuCapacity := node.Status.Capacity.Cpu().MilliValue()
	memCapacity := node.Status.Capacity.Memory().Value()

	cpuUsage := nodeMetrics.Usage.Cpu().MilliValue()
	memUsage := nodeMetrics.Usage.Memory().Value()

	if cpuCapacity > 0 {
		cpuPct = float64(cpuUsage) / float64(cpuCapacity) * 100
	}
	if memCapacity > 0 {
		memPct = float64(memUsage) / float64(memCapacity) * 100
	}
	return cpuPct, memPct, nil
}

func (a *Autoscaler) nextVMID() int {
	vms, _ := a.pve.ListVMs()
	used := make(map[int]bool)
	for _, vm := range vms {
		used[vm.VMID] = true
	}
	vmid := a.cfg.VMIDStart
	for used[vmid] {
		vmid++
	}
	return vmid
}

func (a *Autoscaler) nextIP(ctx context.Context) string {
	nodes, _ := a.k8s.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	usedIPs := make(map[string]bool)
	for _, n := range nodes.Items {
		for _, addr := range n.Status.Addresses {
			if addr.Type == corev1.NodeInternalIP {
				usedIPs[addr.Address] = true
			}
		}
	}
	for offset := 0; offset < 100; offset++ {
		ip := fmt.Sprintf("%s.%d", a.cfg.IPBase, a.cfg.IPStart+offset)
		if !usedIPs[ip] {
			return ip
		}
	}
	return fmt.Sprintf("%s.%d", a.cfg.IPBase, a.cfg.IPStart)
}

func (a *Autoscaler) pickProxmoxNode() string {
	vms, _ := a.pve.ListVMs()
	counts := make(map[string]int)
	for _, n := range a.cfg.ProxmoxNodes {
		counts[n] = 0
	}
	for _, vm := range vms {
		if strings.Contains(vm.Tags, "autoscaled") {
			counts[vm.Node]++
		}
	}
	best := a.cfg.ProxmoxNodes[0]
	for _, n := range a.cfg.ProxmoxNodes {
		if counts[n] < counts[best] {
			best = n
		}
	}
	return best
}

func isDaemonSetPod(pod *corev1.Pod) bool {
	for _, ref := range pod.OwnerReferences {
		if ref.Kind == "DaemonSet" {
			return true
		}
	}
	return false
}

func int64Ptr(i int64) *int64 { return &i }
