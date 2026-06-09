package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ankycooper/netbird-aviary/internal/agent"
	"github.com/ankycooper/netbird-aviary/internal/config"
	"github.com/ankycooper/netbird-aviary/internal/controller"
	"github.com/ankycooper/netbird-aviary/internal/docker"
	"github.com/ankycooper/netbird-aviary/internal/netbird"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(2)
	}
	log := newLogger(cfg.LogLevel)

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	dw, err := docker.New(cfg.LabelPrefix, log)
	if err != nil {
		log.Error("docker", "err", err)
		os.Exit(1)
	}
	defer dw.Close()

	nb := netbird.NewClient(cfg.APIURL, cfg.APIToken, cfg.HTTPTimeout)

	r := controller.New(cfg, dw, nb, log)

	// Network-mode bootstrap: trigger when EITHER the global is "network" OR a
	// setup key is provided (which means the user wants network mode available,
	// possibly opt-in per service via netbird.target_mode=network labels).
	if cfg.TargetMode == "network" || cfg.SetupKey != "" {
		if err := bootstrapNetworkMode(ctx, cfg, dw, nb, r, log); err != nil {
			log.Error("network-mode bootstrap failed", "err", err)
			os.Exit(1)
		}
	}

	log.Info("starting reconciler",
		"api_url", cfg.APIURL,
		"label_prefix", cfg.LabelPrefix,
		"reconcile_interval", cfg.ReconcileInterval,
		"target_mode", cfg.TargetMode,
		"dry_run", cfg.DryRun)

	if err := r.Reconcile(ctx); err != nil {
		log.Error("initial reconcile failed", "err", err)
	}

	go func() {
		t := time.NewTicker(cfg.ReconcileInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				if err := r.Reconcile(ctx); err != nil {
					log.Error("periodic reconcile failed", "err", err)
				}
			}
		}
	}()

	dw.Events(ctx, func(id string) {
		r.ReconcileContainer(ctx, id)
	})

	log.Info("shutting down")
}

func bootstrapNetworkMode(
	ctx context.Context,
	cfg *config.Config,
	dw *docker.Watcher,
	nb *netbird.Client,
	r *controller.Reconciler,
	log *slog.Logger,
) error {
	// Decide what hostname the agent will register with.
	peerName, err := deriveHostname(ctx, cfg, dw)
	if err != nil {
		return fmt.Errorf("derive peer hostname: %w", err)
	}
	log.Info("network mode bootstrap", "peer_hostname", peerName)

	// Spawn the embedded netbird agent unless explicitly disabled.
	if !cfg.DisableAgent {
		if cfg.SetupKey == "" {
			return fmt.Errorf("NETBIRD_SETUP_KEY is required when NETBIRD_TARGET_MODE=network (or set NETBIRD_DISABLE_AGENT=true to bring your own agent)")
		}
		mgmtURL := cfg.AgentManagementURL
		if mgmtURL == "" {
			mgmtURL = cfg.APIURL
		}
		ag := agent.New(agent.Config{
			SetupKey:      cfg.SetupKey,
			ManagementURL: mgmtURL,
			Hostname:      peerName,
			LogLevel:      cfg.LogLevel,
		}, log)
		if err := ag.Start(ctx); err != nil {
			return fmt.Errorf("start netbird agent: %w", err)
		}
	} else {
		log.Info("NETBIRD_DISABLE_AGENT=true — expecting an external netbird peer to register with hostname", "hostname", peerName)
	}

	// Now provision the NetBird Network/Resource/Router.
	prov := controller.NewProvisioner(nb, dw, log)
	np, err := prov.Provision(ctx, cfg.DockerNetwork, cfg.NetworkName, peerName)
	if err != nil {
		return err
	}
	log.Info("netbird provision complete",
		"network_id", np.NetworkID,
		"resource_id", np.ResourceID,
		"docker_subnet", np.DockerSubnet,
		"router_peer", np.SelfPeerID)
	r.SetNetworkProvision(np)
	return nil
}

func deriveHostname(ctx context.Context, cfg *config.Config, dw *docker.Watcher) (string, error) {
	if cfg.PeerHostname != "" {
		return cfg.PeerHostname, nil
	}
	// Default scheme: "aviary-<docker-network>"
	self, err := dw.InspectSelf(ctx)
	if err != nil || self == nil {
		// Fall back to container hostname
		h, _ := os.Hostname()
		if h == "" {
			h = "aviary"
		}
		return h, err
	}
	netName := cfg.DockerNetwork
	if netName == "" {
		for n := range self.Networks {
			if n == "bridge" || n == "host" || n == "none" {
				continue
			}
			netName = n
			break
		}
	}
	if netName == "" {
		return "aviary", nil
	}
	return "aviary-" + netName, nil
}

func newLogger(level string) *slog.Logger {
	var lvl slog.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		lvl = slog.LevelInfo
	}
	h := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: lvl})
	return slog.New(h)
}
