package proxmox

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	baseURL    string
	authHeader string
	httpClient *http.Client
}

type VM struct {
	VMID   int    `json:"vmid"`
	Name   string `json:"name"`
	Node   string `json:"node"`
	Status string `json:"status"`
	Tags   string `json:"tags"`
}

func NewClient(host, tokenID, tokenSecret string, verifySSL bool) *Client {
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{InsecureSkipVerify: !verifySSL},
	}
	return &Client{
		baseURL:    fmt.Sprintf("%s/api2/json", strings.TrimRight(host, "/")),
		authHeader: fmt.Sprintf("PVEAPIToken=%s=%s", tokenID, tokenSecret),
		httpClient: &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}
}

func (c *Client) request(method, path string, data url.Values) (json.RawMessage, error) {
	reqURL := c.baseURL + path
	var body io.Reader
	if data != nil {
		body = strings.NewReader(data.Encode())
	}
	req, err := http.NewRequest(method, reqURL, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", c.authHeader)
	if data != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("API error %d: %s", resp.StatusCode, string(respBody))
	}

	var result struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, err
	}
	return result.Data, nil
}

func (c *Client) ListVMs() ([]VM, error) {
	data, err := c.request("GET", "/cluster/resources?type=vm", nil)
	if err != nil {
		return nil, err
	}
	var vms []VM
	if err := json.Unmarshal(data, &vms); err != nil {
		return nil, err
	}
	return vms, nil
}

type CreateVMOpts struct {
	VMID      int
	Name      string
	Node      string
	Cores     int
	MemoryMB  int
	DiskGB    int
	Storage   string
	ISOImport string
	TalosISO  string
	Bridge    string
	VLAN      int
	Tags      string
}

func (c *Client) CreateVM(opts CreateVMOpts) error {
	data := url.Values{
		"vmid":    {fmt.Sprintf("%d", opts.VMID)},
		"name":    {opts.Name},
		"tags":    {opts.Tags},
		"memory":  {fmt.Sprintf("%d", opts.MemoryMB)},
		"cores":   {fmt.Sprintf("%d", opts.Cores)},
		"sockets": {"1"},
		"cpu":     {"host"},
		"ostype":  {"l26"},
		"scsihw":  {"virtio-scsi-single"},
		"scsi0":   {fmt.Sprintf("%s:0,import-from=%s,discard=on,ssd=1,iothread=1", opts.Storage, opts.ISOImport)},
		"net0":    {fmt.Sprintf("virtio,bridge=%s,tag=%d", opts.Bridge, opts.VLAN)},
		"boot":    {"order=scsi0"},
		"agent":   {"1"},
		"bios":    {"seabios"},
		"machine": {"q35"},
	}
	if opts.TalosISO != "" {
		data.Set("ide2", fmt.Sprintf("%s,media=cdrom", opts.TalosISO))
		data.Set("boot", "order=scsi0;ide2")
	}

	path := fmt.Sprintf("/nodes/%s/qemu", opts.Node)
	_, err := c.request("POST", path, data)
	if err != nil {
		return fmt.Errorf("create VM %d on %s: %w", opts.VMID, opts.Node, err)
	}
	slog.Info("created VM", "vmid", opts.VMID, "name", opts.Name, "node", opts.Node)
	return nil
}

func (c *Client) ResizeDisk(node string, vmid, sizeGB int) error {
	data := url.Values{
		"disk": {"scsi0"},
		"size": {fmt.Sprintf("%dG", sizeGB)},
	}
	path := fmt.Sprintf("/nodes/%s/qemu/%d/resize", node, vmid)
	_, err := c.request("PUT", path, data)
	return err
}

func (c *Client) StartVM(node string, vmid int) error {
	path := fmt.Sprintf("/nodes/%s/qemu/%d/status/start", node, vmid)
	_, err := c.request("POST", path, nil)
	return err
}

func (c *Client) StopVM(node string, vmid int) error {
	path := fmt.Sprintf("/nodes/%s/qemu/%d/status/stop", node, vmid)
	_, err := c.request("POST", path, nil)
	return err
}

func (c *Client) DeleteVM(node string, vmid int) error {
	_ = c.StopVM(node, vmid)
	time.Sleep(5 * time.Second)
	path := fmt.Sprintf("/nodes/%s/qemu/%d?purge=1&destroy-unreferenced-disks=1", node, vmid)
	_, err := c.request("DELETE", path, nil)
	if err != nil {
		return err
	}
	slog.Info("deleted VM", "vmid", vmid, "node", node)
	return nil
}

func (c *Client) GetVMIP(node string, vmid int) (string, error) {
	path := fmt.Sprintf("/nodes/%s/qemu/%d/agent/network-get-interfaces", node, vmid)
	data, err := c.request("GET", path, nil)
	if err != nil {
		return "", err
	}

	var result struct {
		Result []struct {
			Name        string `json:"name"`
			IPAddresses []struct {
				Type    string `json:"ip-address-type"`
				Address string `json:"ip-address"`
			} `json:"ip-addresses"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", err
	}

	for _, iface := range result.Result {
		for _, addr := range iface.IPAddresses {
			if addr.Type == "ipv4" && !strings.HasPrefix(addr.Address, "127.") {
				return addr.Address, nil
			}
		}
	}
	return "", fmt.Errorf("no IPv4 address found for VM %d", vmid)
}
