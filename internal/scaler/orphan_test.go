package scaler

import (
	"sort"
	"testing"

	"github.com/Pzharyuk/proxmox-autoscaler/internal/proxmox"
)

func TestOrphanVMsToReap(t *testing.T) {
	vms := []proxmox.VM{
		{VMID: 2001, Name: "k8s-autoscale-01"}, // orphan: not a node, not provisioning
		{VMID: 2002, Name: "k8s-autoscale-02"}, // joined node -> keep
		{VMID: 2003, Name: "k8s-autoscale-03"}, // mid-join -> keep
		{VMID: 2004, Name: "k8s-autoscale-04"}, // orphan
		{VMID: 100, Name: "k8s-worker-01"},     // static node, wrong prefix -> never touched
		{VMID: 101, Name: "some-other-vm"},     // unrelated -> never touched
	}
	isNode := map[string]bool{"k8s-autoscale-02": true, "k8s-worker-01": true}
	provisioning := map[string]bool{"k8s-autoscale-03": true}

	got := orphanVMsToReap(vms, isNode, provisioning)
	var ids []int
	for _, v := range got {
		ids = append(ids, v.VMID)
	}
	sort.Ints(ids)

	want := []int{2001, 2004}
	if len(ids) != len(want) {
		t.Fatalf("reap set = %v, want %v", ids, want)
	}
	for i := range want {
		if ids[i] != want[i] {
			t.Fatalf("reap set = %v, want %v", ids, want)
		}
	}
}

func TestOrphanVMsToReap_NeverTouchesNonAutoscaleVMs(t *testing.T) {
	vms := []proxmox.VM{
		{VMID: 10, Name: "k8s-cp-01"},
		{VMID: 11, Name: "k8s-worker-07"},
		{VMID: 12, Name: "vault"},
	}
	got := orphanVMsToReap(vms, map[string]bool{}, map[string]bool{})
	if len(got) != 0 {
		t.Fatalf("must never select non-autoscale VMs, got %v", got)
	}
}
