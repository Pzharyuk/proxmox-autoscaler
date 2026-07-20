package talos

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	WorkerConfigPath string
	IPMask           int
	Gateway          string
	Nameserver       string
}

type Manager struct {
	cfg Config
	// baseDocs holds EVERY YAML document in the worker config, in order. Talos
	// machine configs are multi-document (a v1alpha1 machine config plus separate
	// documents like HostnameConfig, KubeletConfig, ...). The previous code used
	// yaml.Unmarshal into a single map, which decodes ONLY the first document and
	// silently dropped the rest — so applied nodes got an incomplete config.
	baseDocs []map[string]any
}

func NewManager(cfg Config) (*Manager, error) {
	m := &Manager{cfg: cfg}
	if err := m.loadBaseConfig(); err != nil {
		return nil, fmt.Errorf("load base config: %w", err)
	}
	return m, nil
}

func (m *Manager) loadBaseConfig() error {
	data, err := os.ReadFile(m.cfg.WorkerConfigPath)
	if err != nil {
		return err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	for {
		var doc map[string]any
		err := dec.Decode(&doc)
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("decode worker config document: %w", err)
		}
		if doc != nil {
			m.baseDocs = append(m.baseDocs, doc)
		}
	}
	if len(m.baseDocs) == 0 {
		return fmt.Errorf("worker config %s contained no documents", m.cfg.WorkerConfigPath)
	}
	return nil
}

// GenerateWorkerConfig returns the full multi-document worker config with the
// machine document's network patched for this node (explicit hostname + static
// IP). Every other document is preserved verbatim, EXCEPT any HostnameConfig
// document, which is dropped because it would conflict with the explicit
// machine.network.hostname we set per node.
func (m *Manager) GenerateWorkerConfig(hostname, ip string) ([]byte, error) {
	// Deep-copy all docs via a marshal/unmarshal round-trip.
	raw, err := yaml.Marshal(m.baseDocs)
	if err != nil {
		return nil, err
	}
	var docs []map[string]any
	if err := yaml.Unmarshal(raw, &docs); err != nil {
		return nil, err
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	patched := false
	for _, doc := range docs {
		// Drop HostnameConfig docs — superseded by the explicit hostname below.
		if kind, _ := doc["kind"].(string); kind == "HostnameConfig" {
			continue
		}
		// Patch the v1alpha1 machine config document's network.
		if machine, ok := doc["machine"].(map[string]any); ok {
			machine["network"] = map[string]any{
				"hostname": hostname,
				"interfaces": []any{
					map[string]any{
						"deviceSelector": map[string]any{"busPath": "0*"},
						"addresses":      []string{fmt.Sprintf("%s/%d", ip, m.cfg.IPMask)},
						"routes": []any{
							map[string]any{"network": "0.0.0.0/0", "gateway": m.cfg.Gateway},
						},
					},
				},
				"nameservers": []string{m.cfg.Nameserver},
			}
			patched = true
		}
		if err := enc.Encode(doc); err != nil {
			return nil, err
		}
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	if !patched {
		return nil, fmt.Errorf("no machine config document found to patch")
	}
	return buf.Bytes(), nil
}

func (m *Manager) ApplyConfig(targetIP string, configYAML []byte) error {
	tmpFile := fmt.Sprintf("/tmp/talos-config-%s-%d.yaml", targetIP, time.Now().UnixNano())
	if err := os.WriteFile(tmpFile, configYAML, 0600); err != nil {
		return err
	}
	defer os.Remove(tmpFile)

	cmd := exec.Command("talosctl", "apply-config", "--insecure", "--nodes", targetIP, "--file", tmpFile)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("talosctl apply-config: %s: %w", string(output), err)
	}
	slog.Info("applied Talos config", "target", targetIP)
	return nil
}
