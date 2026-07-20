package scaler

import (
	"context"
	"testing"

	fake "k8s.io/client-go/kubernetes/fake"
)

// nextIP must assign a unique, deterministic IP per vmid even when NO nodes have
// joined the cluster yet — the exact condition under which the old code handed
// every VM the same IP (IPBase.IPStart) and caused the orphan-VM churn loop.
func newTestScaler() *Autoscaler {
	return &Autoscaler{
		cfg: Config{IPBase: "10.43.80", IPStart: 50, VMIDStart: 2001},
		k8s: fake.NewSimpleClientset(), // zero joined nodes
	}
}

func TestNextIP_DeterministicPerVMID(t *testing.T) {
	a := newTestScaler()
	ctx := context.Background()
	cases := map[int]string{
		2001: "10.43.80.50",
		2002: "10.43.80.51",
		2003: "10.43.80.52",
		2010: "10.43.80.59",
	}
	for vmid, want := range cases {
		if got := a.nextIP(ctx, vmid); got != want {
			t.Errorf("nextIP(vmid=%d) = %q, want %q", vmid, got, want)
		}
	}
}

func TestNextIP_NoCollisionAcrossSequentialScaleUps(t *testing.T) {
	a := newTestScaler()
	ctx := context.Background()
	seen := map[string]int{}
	// Simulate a bootstrap window: several VMs created before ANY joins.
	for vmid := 2001; vmid <= 2009; vmid++ {
		ip := a.nextIP(ctx, vmid)
		if prev, dup := seen[ip]; dup {
			t.Fatalf("duplicate IP %s for vmid %d and %d (the original bug)", ip, prev, vmid)
		}
		seen[ip] = vmid
	}
	if len(seen) != 9 {
		t.Fatalf("expected 9 distinct IPs, got %d", len(seen))
	}
}
