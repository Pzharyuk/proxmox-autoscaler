# Proxmox Node Autoscaler

A Kubernetes controller that automatically scales Talos Linux worker nodes on Proxmox VE based on cluster demand.

## How It Works

```
Pending Pods (Insufficient Resources)
        │
        ▼
┌─────────────────────┐
│  Autoscaler watches  │ ◄── Poll every 30s
│  for pending pods    │
└─────────┬───────────┘
          │ Pending > 30s?
          ▼
┌─────────────────────┐     ┌──────────────────┐
│ Create VM on Proxmox │────▶│ Apply Talos config │
│ (least-loaded node)  │     │ (worker joins k8s) │
└─────────────────────┘     └──────────────────┘

Idle Nodes (Low Utilization)
        │
        ▼
┌─────────────────────┐
│ Monitor CPU & Memory │ ◄── Metrics API
│ utilization per node │
└─────────┬───────────┘
          │ < 30% for 5 min?
          ▼
┌─────────────────────┐     ┌──────────────────┐
│  Cordon & drain node │────▶│ Delete VM from PVE │
└─────────────────────┘     └──────────────────┘
```

### Scale Up
1. Detects pods stuck in `Pending` state with `Insufficient cpu/memory` for 30+ seconds
2. Picks the Proxmox node with the fewest autoscaled VMs
3. Creates a new VM from the Talos cloud image
4. Waits for DHCP IP, applies Talos machine config with a static IP
5. Waits for the node to join the Kubernetes cluster
6. Labels the node with `autoscaler.proxmox/managed=true`

### Scale Down
1. Monitors CPU and memory utilization on managed nodes via the Metrics API
2. When both are below 30% for 5+ minutes (configurable), triggers scale-down
3. Cordons the node, evicts all pods (except DaemonSets)
4. Deletes the Kubernetes node object
5. Stops and destroys the Proxmox VM

## Configuration

All configuration is via environment variables:

### Proxmox Connection
| Variable | Description | Default |
|----------|-------------|---------|
| `PROXMOX_HOST` | Proxmox API URL (e.g., `https://10.43.100.200:8006`) | required |
| `PROXMOX_TOKEN_ID` | API token ID (e.g., `root@pam!claude`) | required |
| `PROXMOX_TOKEN_SECRET` | API token secret | required |
| `PROXMOX_NODES` | Comma-separated Proxmox node names | required |
| `PROXMOX_VERIFY_SSL` | Verify SSL certificates | `false` |

### Scaling Behavior
| Variable | Description | Default |
|----------|-------------|---------|
| `MIN_WORKERS` | Minimum autoscaled worker count | `0` |
| `MAX_WORKERS` | Maximum autoscaled worker count | `9` |
| `SCALE_UP_PENDING_SECONDS` | Seconds a pod must be pending before scaling up | `30` |
| `SCALE_DOWN_IDLE_SECONDS` | Seconds a node must be idle before scaling down | `300` |
| `SCALE_DOWN_UTILIZATION_PCT` | CPU/memory threshold below which a node is "idle" | `30` |
| `POLL_INTERVAL` | Seconds between autoscaler checks | `30` |

### VM Specs
| Variable | Description | Default |
|----------|-------------|---------|
| `WORKER_CORES` | vCPUs per worker | `4` |
| `WORKER_MEMORY_MB` | Memory per worker (MB) | `8192` |
| `WORKER_DISK_GB` | Disk per worker (GB) | `100` |
| `VM_STORAGE` | Proxmox storage for VM disks | `main` |
| `ISO_STORAGE` | Proxmox storage with Talos image | `ISO` |
| `TALOS_ISO` | Talos ISO volume ID (for boot fallback) | `` |
| `VM_BRIDGE` | Network bridge | `vmbr1` |
| `VM_VLAN` | VLAN tag | `88` |
| `VM_TAGS` | Comma-separated VM tags | `k8s,autoscaled` |

### Networking
| Variable | Description | Default |
|----------|-------------|---------|
| `IP_BASE` | First 3 octets of IP range | `10.43.80` |
| `IP_START` | Starting last octet for autoscaled nodes | `50` |
| `IP_MASK` | Subnet mask bits | `20` |
| `IP_GATEWAY` | Default gateway | `10.43.80.1` |
| `IP_NAMESERVER` | DNS server | `10.43.80.1` |

### Talos
| Variable | Description | Default |
|----------|-------------|---------|
| `TALOS_WORKER_CONFIG_PATH` | Path to base Talos worker config | `/config/worker.yaml` |
| `CLUSTER_NAME` | Talos cluster name | `hgwa-k8s` |
| `VMID_START` | Starting VMID for autoscaled VMs | `2001` |
| `NODE_LABEL` | Kubernetes label for managed nodes | `autoscaler.proxmox/managed` |

## Deployment

Deployed via ArgoCD as part of the [hgwa-k8s-gitops](https://github.com/Pzharyuk/hgwa-k8s-gitops) app-of-apps.

### Prerequisites
- Proxmox API token with VM create/delete permissions
- Talos worker config (base template) mounted as a ConfigMap/Secret
- Ubuntu cloud image available on all Proxmox nodes as `ISO:import/ubuntu-24.04-cloud-amd64.qcow2`
- `talosctl` bundled in the container image

### RBAC
The controller needs a ServiceAccount with permissions to:
- List/watch pods (all namespaces)
- List/get/patch/delete nodes
- Create pod evictions
- Read node metrics

## Architecture

```
┌─────────────────────────────────────────────────┐
│                 Kubernetes Cluster                │
│                                                   │
│  ┌──────────────────┐   ┌──────────────────────┐ │
│  │  Autoscaler Pod   │   │  Metrics Server       │ │
│  │  (this project)   │──▶│  (node utilization)   │ │
│  └────────┬─────────┘   └──────────────────────┘ │
│           │                                       │
│           │ Watch pending pods                    │
│           │ Monitor node utilization              │
│           │ Cordon/drain/delete nodes             │
└───────────┼───────────────────────────────────────┘
            │
            │ Proxmox API
            ▼
┌───────────────────────────────────────────────────┐
│               Proxmox VE Cluster                   │
│  ┌─────────────┐ ┌─────────────┐ ┌─────────────┐ │
│  │ proxmox-01   │ │ proxmox-02   │ │ proxmox-03   │ │
│  │ Create/delete│ │ Create/delete│ │ Create/delete│ │
│  │ Talos VMs    │ │ Talos VMs    │ │ Talos VMs    │ │
│  └─────────────┘ └─────────────┘ └─────────────┘ │
└───────────────────────────────────────────────────┘
```

## License

MIT
