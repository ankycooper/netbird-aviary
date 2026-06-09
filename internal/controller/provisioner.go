package controller

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/ankycooper/netbird-aviary/internal/docker"
	"github.com/ankycooper/netbird-aviary/internal/netbird"
)

// NetworkProvision is the result of ensuring the NetBird Network/Resource/Router
// scaffolding is in place for target_mode=network.
type NetworkProvision struct {
	NetworkID       string // NetBird Network id (e.g. "Home" id)
	ResourceID      string // Resource id for the docker subnet — set as target_id on services
	DockerSubnet    string // CIDR of the docker network we're routing
	DockerNetwork   string // docker network name (e.g. "myproject_default")
	SelfPeerID      string // our own NetBird peer id (router for the resource)
	SelfPeerName    string // peer hostname we registered as
}

// Provisioner sets up the NetBird-side artifacts that make target_mode=network
// actually route packets: the Network, a Resource for the docker subnet, and a
// Router pointing at our own peer. All operations are idempotent.
type Provisioner struct {
	nb     *netbird.Client
	docker *docker.Watcher
	log    *slog.Logger
}

func NewProvisioner(nb *netbird.Client, dw *docker.Watcher, log *slog.Logger) *Provisioner {
	return &Provisioner{nb: nb, docker: dw, log: log}
}

// Provision ensures the NetBird scaffolding exists. It returns a snapshot of
// the IDs the reconciler should use as target defaults for network-mode services.
//
// Inputs:
//   - dockerNetworkOverride: if non-empty, force this docker-network name; otherwise auto-detect
//   - netbirdNetworkName: name to use for the NetBird Network (default: docker-network name)
//   - peerName: hostname our embedded agent registered with (used to find our own peer id)
func (p *Provisioner) Provision(ctx context.Context, dockerNetworkOverride, netbirdNetworkName, peerName string) (*NetworkProvision, error) {
	// 1. Pick the docker network we operate on
	dn, err := p.pickDockerNetwork(ctx, dockerNetworkOverride)
	if err != nil {
		return nil, err
	}
	if dn.Subnet == "" {
		return nil, fmt.Errorf("docker network %q has no IPAM subnet", dn.Name)
	}
	p.log.Info("docker network", "name", dn.Name, "subnet", dn.Subnet, "id", dn.ID)

	// 2. Find self peer (the embedded agent must already have connected)
	selfID, err := p.findSelfPeer(ctx, peerName)
	if err != nil {
		return nil, fmt.Errorf("find self peer (hostname=%q): %w", peerName, err)
	}
	p.log.Info("self peer", "id", selfID, "hostname", peerName)

	// 3. Ensure NetBird Network
	if netbirdNetworkName == "" {
		netbirdNetworkName = dn.Name
	}
	netID, err := p.ensureNetwork(ctx, netbirdNetworkName)
	if err != nil {
		return nil, fmt.Errorf("ensure network: %w", err)
	}

	// 4. Ensure Resource for the docker subnet
	resID, err := p.ensureResource(ctx, netID, dn.Subnet)
	if err != nil {
		return nil, fmt.Errorf("ensure resource: %w", err)
	}

	// 5. Ensure Router (our peer is the router)
	if err := p.ensureRouter(ctx, netID, selfID); err != nil {
		return nil, fmt.Errorf("ensure router: %w", err)
	}

	return &NetworkProvision{
		NetworkID:     netID,
		ResourceID:    resID,
		DockerSubnet:  dn.Subnet,
		DockerNetwork: dn.Name,
		SelfPeerID:    selfID,
		SelfPeerName:  peerName,
	}, nil
}

func (p *Provisioner) pickDockerNetwork(ctx context.Context, override string) (*docker.NetworkInfo, error) {
	self, err := p.docker.InspectSelf(ctx)
	if err != nil {
		return nil, fmt.Errorf("inspect self: %w", err)
	}
	if self == nil {
		return nil, fmt.Errorf("could not inspect own container")
	}
	if override != "" {
		if _, ok := self.Networks[override]; !ok {
			return nil, fmt.Errorf("NETBIRD_DOCKER_NETWORK=%q but controller is not attached to that network (attached: %v)", override, networkNames(self.Networks))
		}
		return p.docker.GetNetwork(ctx, override)
	}
	// Pick a non-default network. Skip "bridge" / "host" / "none".
	candidates := []string{}
	for name := range self.Networks {
		if name == "bridge" || name == "host" || name == "none" {
			continue
		}
		candidates = append(candidates, name)
	}
	switch len(candidates) {
	case 0:
		return nil, fmt.Errorf("controller is not attached to a non-default docker network — set NETBIRD_DOCKER_NETWORK or attach the controller to a user-defined network")
	case 1:
		return p.docker.GetNetwork(ctx, candidates[0])
	default:
		return nil, fmt.Errorf("controller is attached to multiple docker networks (%v) — set NETBIRD_DOCKER_NETWORK to disambiguate", candidates)
	}
}

// findSelfPeer polls the peer list until we find one whose hostname matches.
// The embedded agent may take a few seconds to register on first run.
func (p *Provisioner) findSelfPeer(ctx context.Context, hostname string) (string, error) {
	hostname = strings.ToLower(hostname)
	deadline := time.Now().Add(60 * time.Second)
	for {
		peers, err := p.nb.ListPeers(ctx)
		if err != nil {
			return "", err
		}
		for _, peer := range peers {
			if strings.EqualFold(peer.Hostname, hostname) || strings.EqualFold(peer.Name, hostname) {
				return peer.ID, nil
			}
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("timed out waiting for own peer to register (looked for hostname/name %q in %d peers)", hostname, len(peers))
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (p *Provisioner) ensureNetwork(ctx context.Context, name string) (string, error) {
	nets, err := p.nb.ListNetworks(ctx)
	if err != nil {
		return "", err
	}
	for _, n := range nets {
		if strings.EqualFold(n.Name, name) {
			p.log.Info("netbird network exists", "id", n.ID, "name", n.Name)
			return n.ID, nil
		}
	}
	out, err := p.nb.CreateNetwork(ctx, &netbird.NetworkCreate{
		Name:        name,
		Description: "Auto-provisioned by netbird-aviary",
	})
	if err != nil {
		return "", err
	}
	p.log.Info("netbird network created", "id", out.ID, "name", name)
	return out.ID, nil
}

func (p *Provisioner) ensureResource(ctx context.Context, netID, subnet string) (string, error) {
	resources, err := p.nb.ListNetworkResources(ctx, netID)
	if err != nil {
		return "", err
	}
	for _, r := range resources {
		if r.Address == subnet {
			p.log.Info("netbird resource exists", "id", r.ID, "address", r.Address)
			return r.ID, nil
		}
	}
	out, err := p.nb.CreateNetworkResource(ctx, netID, &netbird.NetworkResource{
		Name:    "docker-" + sanitizeName(subnet),
		Address: subnet,
		Enabled: true,
		Groups:  []string{}, // user can attach access groups manually in the UI for policy
	})
	if err != nil {
		return "", err
	}
	p.log.Info("netbird resource created", "id", out.ID, "address", subnet)
	return out.ID, nil
}

func (p *Provisioner) ensureRouter(ctx context.Context, netID, peerID string) error {
	routers, err := p.nb.ListNetworkRouters(ctx, netID)
	if err != nil {
		return err
	}
	for _, r := range routers {
		if r.Peer == peerID {
			if !r.Enabled {
				p.log.Info("re-enabling existing netbird router", "id", r.ID, "peer", peerID)
				r.Enabled = true
				_, err := p.nb.UpdateNetworkRouter(ctx, netID, r.ID, &r)
				return err
			}
			p.log.Info("netbird router exists", "id", r.ID, "peer", peerID)
			return nil
		}
	}
	out, err := p.nb.CreateNetworkRouter(ctx, netID, &netbird.NetworkRouter{
		Peer:       peerID,
		Metric:     9999,
		Masquerade: true,
		Enabled:    true,
	})
	if err != nil {
		return err
	}
	p.log.Info("netbird router created", "id", out.ID, "peer", peerID)
	return nil
}

func networkNames(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func sanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.' || r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}
