// Package docker is a minimal Engine-API client (no Docker SDK dependency).
// We only need three things: list containers, inspect one, stream events.
package docker

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const dockerAPIVersion = "v1.43" // stable across Docker 20.10+

// Container is the minimal snapshot the reconciler needs.
type Container struct {
	ID      string
	Name    string
	Labels  map[string]string
	Running bool
	// PortBindings maps "containerPort/proto" => host port. Empty for host-network containers.
	PortBindings map[string]int
	// Networks maps docker-network NAME => container's IP on that network.
	Networks map[string]string
}

// NetworkInfo describes a docker network.
type NetworkInfo struct {
	ID     string
	Name   string
	Subnet string // CIDR, e.g. "172.20.0.0/16"
}

type Watcher struct {
	http        *http.Client
	host        string // "http://docker" for unix; "http://host:port" for tcp
	labelPrefix string
	log         *slog.Logger
}

func New(labelPrefix string, log *slog.Logger) (*Watcher, error) {
	sock := os.Getenv("DOCKER_HOST")
	if sock == "" {
		sock = "unix:///var/run/docker.sock"
	}
	transport, host, err := buildTransport(sock)
	if err != nil {
		return nil, err
	}
	return &Watcher{
		http:        &http.Client{Transport: transport},
		host:        host,
		labelPrefix: labelPrefix,
		log:         log,
	}, nil
}

func buildTransport(dockerHost string) (http.RoundTripper, string, error) {
	switch {
	case strings.HasPrefix(dockerHost, "unix://"):
		sock := strings.TrimPrefix(dockerHost, "unix://")
		tr := &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", sock)
			},
		}
		return tr, "http://docker", nil
	case strings.HasPrefix(dockerHost, "tcp://"):
		return http.DefaultTransport, "http://" + strings.TrimPrefix(dockerHost, "tcp://"), nil
	case strings.HasPrefix(dockerHost, "http://"), strings.HasPrefix(dockerHost, "https://"):
		return http.DefaultTransport, dockerHost, nil
	default:
		return nil, "", fmt.Errorf("unsupported DOCKER_HOST scheme: %s", dockerHost)
	}
}

func (w *Watcher) Close() error { return nil }

// --- Engine API DTOs (subset) ---

type apiContainerSummary struct {
	ID              string                       `json:"Id"`
	Names           []string                     `json:"Names"`
	State           string                       `json:"State"`
	Labels          map[string]string            `json:"Labels"`
	Ports           []apiPort                    `json:"Ports"`
	NetworkSettings *apiContainerSummaryNetworks `json:"NetworkSettings"`
}

type apiContainerSummaryNetworks struct {
	Networks map[string]apiNetworkRef `json:"Networks"`
}

type apiPort struct {
	PrivatePort int    `json:"PrivatePort"`
	PublicPort  int    `json:"PublicPort"`
	Type        string `json:"Type"`
}

type apiInspect struct {
	ID              string         `json:"Id"`
	Name            string         `json:"Name"`
	Config          apiConfig      `json:"Config"`
	State           apiState       `json:"State"`
	NetworkSettings apiNetSettings `json:"NetworkSettings"`
	HostConfig      apiHostConfig  `json:"HostConfig"`
}

type apiConfig struct {
	Hostname string            `json:"Hostname"`
	Labels   map[string]string `json:"Labels"`
}

type apiState struct {
	Running bool `json:"Running"`
}

type apiHostConfig struct {
	NetworkMode string `json:"NetworkMode"`
}

type apiNetSettings struct {
	Ports    map[string][]apiPortBinding `json:"Ports"`
	Networks map[string]apiNetworkRef    `json:"Networks"`
}

type apiNetworkRef struct {
	NetworkID string `json:"NetworkID"`
	IPAddress string `json:"IPAddress"`
}

type apiNetwork struct {
	ID     string  `json:"Id"`
	Name   string  `json:"Name"`
	Driver string  `json:"Driver"`
	IPAM   apiIPAM `json:"IPAM"`
}

type apiIPAM struct {
	Config []apiIPAMConfig `json:"Config"`
}

type apiIPAMConfig struct {
	Subnet  string `json:"Subnet"`
	Gateway string `json:"Gateway"`
}

type apiPortBinding struct {
	HostIP   string `json:"HostIp"`
	HostPort string `json:"HostPort"`
}

type apiEvent struct {
	Type   string `json:"Type"`
	Action string `json:"Action"`
	Actor  struct {
		ID string `json:"ID"`
	} `json:"Actor"`
}

// --- Public API ---

func (w *Watcher) List(ctx context.Context) ([]Container, error) {
	v := url.Values{}
	v.Set("all", "true")
	filt := map[string][]string{"label": {w.labelPrefix + ".enable=true"}}
	fb, _ := json.Marshal(filt)
	v.Set("filters", string(fb))

	var raw []apiContainerSummary
	if err := w.get(ctx, "/containers/json?"+v.Encode(), &raw); err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}
	out := make([]Container, 0, len(raw))
	for _, s := range raw {
		out = append(out, fromSummary(s))
	}
	return out, nil
}

func (w *Watcher) Inspect(ctx context.Context, id string) (*Container, error) {
	var info apiInspect
	err := w.get(ctx, "/containers/"+id+"/json", &info)
	if err != nil {
		var nf *notFoundError
		if errors.As(err, &nf) {
			return nil, nil
		}
		return nil, fmt.Errorf("inspect %s: %w", id, err)
	}
	c := Container{
		ID:           info.ID,
		Name:         strings.TrimPrefix(info.Name, "/"),
		Labels:       info.Config.Labels,
		Running:      info.State.Running,
		PortBindings: map[string]int{},
		Networks:     map[string]string{},
	}
	for cp, bindings := range info.NetworkSettings.Ports {
		for _, b := range bindings {
			if b.HostPort == "" {
				continue
			}
			n, err := strconv.Atoi(b.HostPort)
			if err != nil {
				continue
			}
			c.PortBindings[cp] = n
			break
		}
	}
	for name, ref := range info.NetworkSettings.Networks {
		if ref.IPAddress != "" {
			c.Networks[name] = ref.IPAddress
		}
	}
	return &c, nil
}

// SelfContainerID returns the controller's own container ID. It tries, in order:
//   1. /proc/self/mountinfo (reliable in Docker Desktop and most modern setups)
//   2. /proc/self/cgroup (cgroups v1/v2 native)
//   3. os.Hostname() (Docker sets this to the short id by default; works unless
//      the user passes --hostname)
func (w *Watcher) SelfContainerID() string {
	if id := scanMountinfo(); id != "" {
		return id
	}
	if id := scanCgroup(); id != "" {
		return id
	}
	if h, err := os.Hostname(); err == nil && len(h) >= 12 && isHex(h) {
		return h
	}
	return ""
}

func scanMountinfo() string {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return ""
	}
	// Lines like:
	//   <id> <pid> <maj:min> /docker/containers/<container-id>/<file> /etc/<file> ...
	for _, line := range strings.Split(string(data), "\n") {
		i := strings.Index(line, "/docker/containers/")
		if i < 0 {
			continue
		}
		seg := line[i+len("/docker/containers/"):]
		j := strings.IndexByte(seg, '/')
		if j < 0 {
			continue
		}
		if id := seg[:j]; len(id) == 64 && isHex(id) {
			return id
		}
	}
	return ""
}

func scanCgroup() string {
	data, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		idx := strings.LastIndex(line, "/")
		if idx < 0 {
			continue
		}
		seg := line[idx+1:]
		seg = strings.TrimPrefix(seg, "docker-")
		seg = strings.TrimSuffix(seg, ".scope")
		if len(seg) == 64 && isHex(seg) {
			return seg
		}
	}
	return ""
}

// InspectSelf returns the controller container's own info.
func (w *Watcher) InspectSelf(ctx context.Context) (*Container, error) {
	id := w.SelfContainerID()
	if id == "" {
		return nil, fmt.Errorf("could not determine own container id from /proc/self/cgroup")
	}
	return w.Inspect(ctx, id)
}

// GetNetwork fetches details about a docker network by name or id.
func (w *Watcher) GetNetwork(ctx context.Context, nameOrID string) (*NetworkInfo, error) {
	var n apiNetwork
	if err := w.get(ctx, "/networks/"+nameOrID, &n); err != nil {
		return nil, err
	}
	info := &NetworkInfo{ID: n.ID, Name: n.Name}
	if len(n.IPAM.Config) > 0 {
		info.Subnet = n.IPAM.Config[0].Subnet
	}
	return info, nil
}

func isHex(s string) bool {
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// Events streams container lifecycle events to handler until ctx is done.
// It reconnects with backoff on stream failure.
func (w *Watcher) Events(ctx context.Context, handler func(id string)) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		err := w.streamEvents(ctx, handler)
		if ctx.Err() != nil {
			return
		}
		w.log.Warn("docker event stream closed, reconnecting", "err", err, "backoff", backoff)
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

func (w *Watcher) streamEvents(ctx context.Context, handler func(id string)) error {
	v := url.Values{}
	filt := map[string][]string{
		"type":  {"container"},
		"event": {"start", "die", "stop", "destroy", "update"},
	}
	fb, _ := json.Marshal(filt)
	v.Set("filters", string(fb))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		w.host+"/"+dockerAPIVersion+"/events?"+v.Encode(), nil)
	if err != nil {
		return err
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("events: %s", resp.Status)
	}
	w.log.Info("docker event stream connected")

	dec := json.NewDecoder(bufio.NewReader(resp.Body))
	for {
		var ev apiEvent
		if err := dec.Decode(&ev); err != nil {
			return err
		}
		w.log.Debug("docker event", "type", ev.Type, "action", ev.Action, "id", ev.Actor.ID)
		if ev.Actor.ID != "" {
			handler(ev.Actor.ID)
		}
	}
}

// --- helpers ---

type notFoundError struct{ msg string }

func (e *notFoundError) Error() string { return e.msg }

func (w *Watcher) get(ctx context.Context, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.host+"/"+dockerAPIVersion+path, nil)
	if err != nil {
		return err
	}
	resp, err := w.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return &notFoundError{msg: "not found: " + path}
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("docker api %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func fromSummary(s apiContainerSummary) Container {
	name := ""
	if len(s.Names) > 0 {
		name = strings.TrimPrefix(s.Names[0], "/")
	}
	out := Container{
		ID:           s.ID,
		Name:         name,
		Labels:       s.Labels,
		Running:      s.State == "running",
		PortBindings: map[string]int{},
		Networks:     map[string]string{}, // populated lazily via Inspect when needed
	}
	for _, p := range s.Ports {
		if p.PublicPort == 0 {
			continue
		}
		out.PortBindings[fmt.Sprintf("%d/%s", p.PrivatePort, p.Type)] = p.PublicPort
	}
	if s.NetworkSettings != nil {
		for name, ref := range s.NetworkSettings.Networks {
			if ref.IPAddress != "" {
				out.Networks[name] = ref.IPAddress
			}
		}
	}
	return out
}
