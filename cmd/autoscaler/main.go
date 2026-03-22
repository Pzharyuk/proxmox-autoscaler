package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/Pzharyuk/proxmox-autoscaler/internal/scaler"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	cfg := scaler.LoadConfig()

	if cfg.ProxmoxHost == "" {
		slog.Error("PROXMOX_HOST is required")
		os.Exit(1)
	}
	if len(cfg.ProxmoxNodes) == 0 || cfg.ProxmoxNodes[0] == "" {
		slog.Error("PROXMOX_NODES is required")
		os.Exit(1)
	}

	autoscaler, err := scaler.New(cfg)
	if err != nil {
		slog.Error("failed to initialize autoscaler", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		slog.Info("received shutdown signal")
		cancel()
	}()

	autoscaler.Run(ctx)
}
