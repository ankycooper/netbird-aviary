// Package agent supervises the embedded `netbird` binary as a child process.
// Used when NETBIRD_TARGET_MODE=network — we need an actual netbird agent
// inside this container to act as the routing peer for the docker subnet.
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	defaultBinary = "/usr/local/bin/netbird"
)

type Config struct {
	Binary        string // path to netbird binary (default /usr/local/bin/netbird)
	SetupKey      string // required on first run
	ManagementURL string // e.g. https://nb.example.com
	Hostname      string // peer hostname
	ConfigDir     string // where netbird stores state; default /var/lib/netbird (persistent volume holds default.json + WG key)
	LogLevel      string // info|warn|debug
}

type Agent struct {
	cfg Config
	log *slog.Logger

	mu      sync.Mutex
	cmd     *exec.Cmd
	stopped bool
}

func New(cfg Config, log *slog.Logger) *Agent {
	if cfg.Binary == "" {
		cfg.Binary = defaultBinary
	}
	if cfg.ConfigDir == "" {
		cfg.ConfigDir = "/var/lib/netbird"
	}
	if cfg.LogLevel == "" {
		cfg.LogLevel = "info"
	}
	return &Agent{cfg: cfg, log: log}
}

// Start spawns `netbird up --foreground-mode` as a child process. This is
// the all-in-one mode: starts the daemon, registers with the setup key, and
// keeps the WireGuard interface up. Returns once the process is launched —
// caller polls the NetBird API to confirm the peer has actually appeared.
//
// On unexpected exit the supervisor restarts with backoff while ctx is alive.
func (a *Agent) Start(ctx context.Context) error {
	if _, err := os.Stat(a.cfg.Binary); err != nil {
		return fmt.Errorf("netbird binary not found at %s: %w", a.cfg.Binary, err)
	}
	if a.cfg.ManagementURL == "" {
		return errors.New("management URL is required")
	}
	go a.supervise(ctx)
	return nil
}

func (a *Agent) buildArgs() []string {
	// Default config path is /var/lib/netbird/default.json; we use ConfigDir
	// for the WireGuard state directory.
	args := []string{
		"up",
		"--foreground-mode",
		"--management-url", a.cfg.ManagementURL,
		"--log-file", "console",
		"--log-level", a.cfg.LogLevel,
	}
	if a.cfg.Hostname != "" {
		args = append(args, "--hostname", a.cfg.Hostname)
	}
	if a.cfg.SetupKey != "" {
		args = append(args, "--setup-key", a.cfg.SetupKey)
	}
	return args
}

// Stop sends SIGTERM to the service child if running.
func (a *Agent) Stop() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.stopped = true
	if a.cmd != nil && a.cmd.Process != nil {
		_ = a.cmd.Process.Signal(os.Interrupt)
	}
}

func (a *Agent) supervise(ctx context.Context) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		if a.stopped {
			a.mu.Unlock()
			return
		}
		cmd := exec.CommandContext(ctx, a.cfg.Binary, a.buildArgs()...)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		a.cmd = cmd
		a.mu.Unlock()

		a.log.Info("netbird agent starting", "args", strings.Join(redactArgs(a.buildArgs()), " "))
		err := cmd.Run()
		if ctx.Err() != nil {
			return
		}
		a.mu.Lock()
		stopped := a.stopped
		a.mu.Unlock()
		if stopped {
			return
		}
		a.log.Warn("netbird agent exited, restarting", "err", err, "backoff", backoff)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}

// redactArgs masks the setup-key value when logging.
func redactArgs(args []string) []string {
	out := make([]string, len(args))
	copy(out, args)
	for i := 0; i < len(out)-1; i++ {
		if out[i] == "--setup-key" {
			out[i+1] = "<redacted>"
		}
	}
	return out
}
