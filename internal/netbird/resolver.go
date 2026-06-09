package netbird

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Resolver turns human-friendly names ("Main-net", "Home", "docker-host")
// into NetBird IDs. Results are cached for the lifetime of the resolver
// so the controller doesn't hammer the API.
type Resolver struct {
	c *Client

	mu        sync.Mutex
	peers     map[string]string             // peer name|hostname -> peer id
	networks  map[string]string             // network name -> network id
	resources map[string]map[string]string  // network id -> (resource name -> resource id)
	clusters  map[string]string             // cluster id or address -> id
	groups    map[string]string             // group name -> group id
	loaded    struct {
		peers    bool
		networks bool
		clusters bool
		groups   bool
	}
}

func NewResolver(c *Client) *Resolver {
	return &Resolver{
		c:         c,
		peers:     map[string]string{},
		networks:  map[string]string{},
		resources: map[string]map[string]string{},
		clusters:  map[string]string{},
		groups:    map[string]string{},
	}
}

// LooksLikeID is a best-effort heuristic: NetBird IDs are short, lowercase,
// alphanumeric, with no spaces or dashes (e.g. "d8i9gt4co72s73e2dc40").
// Names typically contain capitals, dashes, or spaces.
func LooksLikeID(s string) bool {
	if len(s) < 16 || len(s) > 40 {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z':
		default:
			return false
		}
	}
	return true
}

func (r *Resolver) PeerID(ctx context.Context, nameOrID string) (string, error) {
	if nameOrID == "" {
		return "", fmt.Errorf("peer name/id is empty")
	}
	if LooksLikeID(nameOrID) {
		return nameOrID, nil
	}
	r.mu.Lock()
	if !r.loaded.peers {
		r.mu.Unlock()
		if err := r.loadPeers(ctx); err != nil {
			return "", err
		}
		r.mu.Lock()
	}
	defer r.mu.Unlock()
	if id, ok := r.peers[strings.ToLower(nameOrID)]; ok {
		return id, nil
	}
	return "", fmt.Errorf("no NetBird peer named %q", nameOrID)
}

func (r *Resolver) NetworkID(ctx context.Context, nameOrID string) (string, error) {
	if nameOrID == "" {
		return "", fmt.Errorf("network name/id is empty")
	}
	if LooksLikeID(nameOrID) {
		return nameOrID, nil
	}
	r.mu.Lock()
	if !r.loaded.networks {
		r.mu.Unlock()
		if err := r.loadNetworks(ctx); err != nil {
			return "", err
		}
		r.mu.Lock()
	}
	defer r.mu.Unlock()
	if id, ok := r.networks[strings.ToLower(nameOrID)]; ok {
		return id, nil
	}
	return "", fmt.Errorf("no NetBird network named %q", nameOrID)
}

// ResourceID resolves a Network Resource. If networkNameOrID is empty, every
// network is searched and a match is returned only if exactly one resource
// has that name across all networks (ambiguity is an error).
func (r *Resolver) ResourceID(ctx context.Context, networkNameOrID, nameOrID string) (string, error) {
	if nameOrID == "" {
		return "", fmt.Errorf("resource name/id is empty")
	}
	if LooksLikeID(nameOrID) {
		return nameOrID, nil
	}

	// Single-network search path
	if networkNameOrID != "" {
		netID, err := r.NetworkID(ctx, networkNameOrID)
		if err != nil {
			return "", err
		}
		if err := r.loadResources(ctx, netID); err != nil {
			return "", err
		}
		r.mu.Lock()
		defer r.mu.Unlock()
		if id, ok := r.resources[netID][strings.ToLower(nameOrID)]; ok {
			return id, nil
		}
		return "", fmt.Errorf("no resource named %q in network %s", nameOrID, networkNameOrID)
	}

	// Cross-network search
	if err := r.ensureNetworksLoaded(ctx); err != nil {
		return "", err
	}
	r.mu.Lock()
	nets := make([]string, 0, len(r.networks))
	for _, id := range r.networks {
		nets = append(nets, id)
	}
	r.mu.Unlock()

	var matches []string
	want := strings.ToLower(nameOrID)
	for _, netID := range nets {
		if err := r.loadResources(ctx, netID); err != nil {
			return "", err
		}
		r.mu.Lock()
		if id, ok := r.resources[netID][want]; ok {
			matches = append(matches, id)
		}
		r.mu.Unlock()
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no resource named %q in any network (set NETBIRD_DEFAULT_NETWORK_NAME to scope the search)", nameOrID)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("resource name %q is ambiguous across networks; set NETBIRD_DEFAULT_NETWORK_NAME or netbird.network", nameOrID)
	}
}

func (r *Resolver) ClusterID(ctx context.Context, nameOrID string) (string, error) {
	if nameOrID == "" {
		return "", fmt.Errorf("cluster name/id is empty")
	}
	if LooksLikeID(nameOrID) {
		return nameOrID, nil
	}
	r.mu.Lock()
	if !r.loaded.clusters {
		r.mu.Unlock()
		if err := r.loadClusters(ctx); err != nil {
			return "", err
		}
		r.mu.Lock()
	}
	defer r.mu.Unlock()
	if id, ok := r.clusters[strings.ToLower(nameOrID)]; ok {
		return id, nil
	}
	return "", fmt.Errorf("no proxy cluster named or addressed %q", nameOrID)
}

// GroupIDs resolves a slice of group names/ids to group IDs. Unresolved
// names cause an error listing every one that failed.
func (r *Resolver) GroupIDs(ctx context.Context, namesOrIDs []string) ([]string, error) {
	if len(namesOrIDs) == 0 {
		return nil, nil
	}
	r.mu.Lock()
	if !r.loaded.groups {
		r.mu.Unlock()
		if err := r.loadGroups(ctx); err != nil {
			return nil, err
		}
		r.mu.Lock()
	}
	out := make([]string, 0, len(namesOrIDs))
	var missing []string
	for _, n := range namesOrIDs {
		if LooksLikeID(n) {
			out = append(out, n)
			continue
		}
		if id, ok := r.groups[strings.ToLower(n)]; ok {
			out = append(out, id)
		} else {
			missing = append(missing, n)
		}
	}
	r.mu.Unlock()
	if len(missing) > 0 {
		return nil, fmt.Errorf("no NetBird groups found for: %s", strings.Join(missing, ", "))
	}
	return out, nil
}

// --- loaders ---

func (r *Resolver) loadPeers(ctx context.Context) error {
	ps, err := r.c.ListPeers(ctx)
	if err != nil {
		return fmt.Errorf("list peers: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, p := range ps {
		if p.Name != "" {
			r.peers[strings.ToLower(p.Name)] = p.ID
		}
		if p.Hostname != "" {
			r.peers[strings.ToLower(p.Hostname)] = p.ID
		}
	}
	r.loaded.peers = true
	return nil
}

func (r *Resolver) loadNetworks(ctx context.Context) error {
	ns, err := r.c.ListNetworks(ctx)
	if err != nil {
		return fmt.Errorf("list networks: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, n := range ns {
		if n.Name != "" {
			r.networks[strings.ToLower(n.Name)] = n.ID
		}
	}
	r.loaded.networks = true
	return nil
}

func (r *Resolver) ensureNetworksLoaded(ctx context.Context) error {
	r.mu.Lock()
	loaded := r.loaded.networks
	r.mu.Unlock()
	if loaded {
		return nil
	}
	return r.loadNetworks(ctx)
}

func (r *Resolver) loadResources(ctx context.Context, networkID string) error {
	r.mu.Lock()
	if _, ok := r.resources[networkID]; ok {
		r.mu.Unlock()
		return nil
	}
	r.mu.Unlock()
	rs, err := r.c.ListNetworkResources(ctx, networkID)
	if err != nil {
		return fmt.Errorf("list resources in %s: %w", networkID, err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	m := map[string]string{}
	for _, x := range rs {
		if x.Name != "" {
			m[strings.ToLower(x.Name)] = x.ID
		}
	}
	r.resources[networkID] = m
	return nil
}

func (r *Resolver) loadClusters(ctx context.Context) error {
	cs, err := r.c.ListProxyClusters(ctx)
	if err != nil {
		return fmt.Errorf("list clusters: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range cs {
		if c.Address != "" {
			r.clusters[strings.ToLower(c.Address)] = c.ID
		}
	}
	r.loaded.clusters = true
	return nil
}

func (r *Resolver) loadGroups(ctx context.Context) error {
	gs, err := r.c.ListGroups(ctx)
	if err != nil {
		return fmt.Errorf("list groups: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, g := range gs {
		if g.Name != "" {
			r.groups[strings.ToLower(g.Name)] = g.ID
		}
	}
	r.loaded.groups = true
	return nil
}
