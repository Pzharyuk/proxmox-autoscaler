package talos

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// A multi-document worker config: machine config + HostnameConfig + a third doc.
// The old single-document yaml.Unmarshal dropped everything after the machine doc.
const multiDocWorker = `version: v1alpha1
machine:
  type: worker
  token: secret-token
  ca:
    crt: base64ca
cluster:
  id: cluster-id
  controlPlane:
    endpoint: https://10.43.80.5:6443
---
apiVersion: v1alpha1
kind: HostnameConfig
auto: stable
---
apiVersion: v1alpha1
kind: KubeletConfig
extraArgs:
  rotate-server-certificates: "true"
`

func writeBase(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "worker.yaml")
	if err := os.WriteFile(p, []byte(multiDocWorker), 0600); err != nil {
		t.Fatal(err)
	}
	m, err := NewManager(Config{WorkerConfigPath: p, IPMask: 20, Gateway: "10.43.80.1", Nameserver: "10.43.80.1"})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	return m
}

func TestLoadBaseConfig_KeepsAllDocuments(t *testing.T) {
	m := writeBase(t)
	if len(m.baseDocs) != 3 {
		t.Fatalf("expected 3 documents loaded, got %d (the single-doc bug drops the rest)", len(m.baseDocs))
	}
}

func TestGenerateWorkerConfig_PreservesDocsAndPatchesNetwork(t *testing.T) {
	m := writeBase(t)
	out, err := m.GenerateWorkerConfig("k8s-autoscale-05", "10.43.80.54")
	if err != nil {
		t.Fatalf("GenerateWorkerConfig: %v", err)
	}

	var docs []map[string]any
	dec := yaml.NewDecoder(strings.NewReader(string(out)))
	for {
		var d map[string]any
		if err := dec.Decode(&d); err != nil {
			break
		}
		if d != nil {
			docs = append(docs, d)
		}
	}

	// HostnameConfig dropped (conflicts with explicit hostname); machine + KubeletConfig kept => 2 docs.
	if len(docs) != 2 {
		t.Fatalf("expected 2 output docs (machine + KubeletConfig, HostnameConfig dropped), got %d", len(docs))
	}

	var haveMachine, haveKubelet, haveHostname bool
	for _, d := range docs {
		if machine, ok := d["machine"].(map[string]any); ok {
			haveMachine = true
			net, _ := machine["network"].(map[string]any)
			if net == nil {
				t.Fatal("machine.network not set")
			}
			if net["hostname"] != "k8s-autoscale-05" {
				t.Errorf("hostname = %v, want k8s-autoscale-05", net["hostname"])
			}
			// essential join fields must survive the round-trip
			if machine["token"] != "secret-token" {
				t.Errorf("machine.token lost: %v", machine["token"])
			}
		}
		if kind, _ := d["kind"].(string); kind == "KubeletConfig" {
			haveKubelet = true
		}
		if kind, _ := d["kind"].(string); kind == "HostnameConfig" {
			haveHostname = true
		}
	}
	if !haveMachine {
		t.Error("machine config document missing from output")
	}
	if !haveKubelet {
		t.Error("KubeletConfig document was dropped — the multi-doc bug is not fixed")
	}
	if haveHostname {
		t.Error("HostnameConfig should have been dropped (conflicts with explicit hostname)")
	}
}
