package scaler

import (
	"os"
	"strconv"
	"strings"
)

type Config struct {
	// Proxmox
	ProxmoxHost        string
	ProxmoxTokenID     string
	ProxmoxTokenSecret string
	ProxmoxNodes       []string
	ProxmoxVerifySSL   bool

	// Scaling
	MinWorkers             int
	MaxWorkers             int
	ScaleUpPendingSeconds  int
	ScaleDownIdleSeconds   int
	ScaleDownUtilizationPct float64
	PollInterval           int

	// VM Specs
	WorkerCores   int
	WorkerMemoryMB int
	WorkerDiskGB  int
	VMStorage     string
	ISOStorage    string
	TalosISO      string
	TalosISO      string
	VMBridge      string
	VMVLAN        int
	VMTags        string

	// Networking
	IPBase      string
	IPStart     int
	IPMask      int
	IPGateway   string
	IPNameserver string

	// Talos
	TalosWorkerConfigPath string
	ClusterName           string
	VMIDStart             int
	NodeLabel             string
}

func LoadConfig() Config {
	return Config{
		ProxmoxHost:        envStr("PROXMOX_HOST", ""),
		ProxmoxTokenID:     envStr("PROXMOX_TOKEN_ID", ""),
		ProxmoxTokenSecret: envStr("PROXMOX_TOKEN_SECRET", ""),
		ProxmoxNodes:       strings.Split(envStr("PROXMOX_NODES", ""), ","),
		ProxmoxVerifySSL:   envStr("PROXMOX_VERIFY_SSL", "false") == "true",

		MinWorkers:              envInt("MIN_WORKERS", 0),
		MaxWorkers:              envInt("MAX_WORKERS", 9),
		ScaleUpPendingSeconds:   envInt("SCALE_UP_PENDING_SECONDS", 30),
		ScaleDownIdleSeconds:    envInt("SCALE_DOWN_IDLE_SECONDS", 300),
		ScaleDownUtilizationPct: envFloat("SCALE_DOWN_UTILIZATION_PCT", 30.0),
		PollInterval:            envInt("POLL_INTERVAL", 30),

		WorkerCores:    envInt("WORKER_CORES", 4),
		WorkerMemoryMB: envInt("WORKER_MEMORY_MB", 8192),
		WorkerDiskGB:   envInt("WORKER_DISK_GB", 100),
		VMStorage:      envStr("VM_STORAGE", "main"),
		ISOStorage:     envStr("ISO_STORAGE", "ISO"),
		TalosISO:       envStr("TALOS_ISO", "ISO:iso/talos-v1.12.6-metal-amd64.iso"),
		TalosISO:       envStr("TALOS_ISO", ""),
		VMBridge:       envStr("VM_BRIDGE", "vmbr1"),
		VMVLAN:         envInt("VM_VLAN", 88),
		VMTags:         envStr("VM_TAGS", "k8s;autoscaled"),

		IPBase:       envStr("IP_BASE", "10.43.80"),
		IPStart:      envInt("IP_START", 50),
		IPMask:       envInt("IP_MASK", 20),
		IPGateway:    envStr("IP_GATEWAY", "10.43.80.1"),
		IPNameserver: envStr("IP_NAMESERVER", "10.43.80.1"),

		TalosWorkerConfigPath: envStr("TALOS_WORKER_CONFIG_PATH", "/config/worker.yaml"),
		ClusterName:           envStr("CLUSTER_NAME", "hgwa-k8s"),
		VMIDStart:             envInt("VMID_START", 2001),
		NodeLabel:             envStr("NODE_LABEL", "autoscaler.proxmox/managed"),
	}
}

func envStr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func envFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}
