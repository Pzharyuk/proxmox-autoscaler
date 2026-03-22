package talos

import (
	"fmt"
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
	cfg        Config
	baseConfig map[string]any
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
	return yaml.Unmarshal(data, &m.baseConfig)
}

func (m *Manager) GenerateWorkerConfig(hostname, ip string) ([]byte, error) {
	// Deep copy base config
	data, _ := yaml.Marshal(m.baseConfig)
	var config map[string]any
	yaml.Unmarshal(data, &config)

	machine, _ := config["machine"].(map[string]any)
	if machine == nil {
		machine = make(map[string]any)
		config["machine"] = machine
	}

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

	return yaml.Marshal(config)
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
