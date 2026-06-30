package controller

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/ankycooper/netbird-aviary/internal/config"
	"github.com/ankycooper/netbird-aviary/internal/docker"
	"github.com/ankycooper/netbird-aviary/internal/labels"
	"github.com/ankycooper/netbird-aviary/internal/netbird"
)

// Reconciler keeps NetBird Services in sync with desired state derived
// from Docker container labels. It only touches services whose names match
// a managed prefix or were created by this controller — never anything else.
type Reconciler struct {
	cfg      *config.Config
	docker   *docker.Watcher
	nb       *netbird.Client
	resolver *netbird.Resolver
	log      *slog.Logger

	// net is set once at startup before any reconcile runs, so we don't lock
	// reads from inside the reconcile loop (which already holds mu).
	netMu sync.RWMutex
	net   *NetworkProvision

	mu sync.Mutex // serializes Reconcile()
}

func New(cfg *config.Config, dw *docker.Watcher, nb *netbird.Client, log *slog.Logger) *Reconciler {
	return &Reconciler{
		cfg:      cfg,
		docker:   dw,
		nb:       nb,
		resolver: netbird.NewResolver(nb),
		log:      log,
	}
}

// SetNetworkProvision tells the reconciler about the auto-provisioned NetBird
// Network/Resource so that services running in target_mode=network get the
// right target_id/target_type by default.
func (r *Reconciler) SetNetworkProvision(np *NetworkProvision) {
	r.netMu.Lock()
	defer r.netMu.Unlock()
	r.net = np
}

func (r *Reconciler) provision() *NetworkProvision {
	r.netMu.RLock()
	defer r.netMu.RUnlock()
	return r.net
}

// Reconcile rebuilds desired state from all labeled containers and applies diffs.
// Containers not currently running cause their services to be disabled, not deleted.
func (r *Reconciler) Reconcile(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	containers, err := r.docker.List(ctx)
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}

	desired := map[string]*desiredService{} // name -> spec
	for _, c := range containers {
		specs, err := labels.Parse(r.cfg.LabelPrefix, c.Labels)
		if err != nil {
			r.log.Warn("skipping container with bad labels", "container", c.Name, "err", err)
			continue
		}
		for _, sp := range specs {
			ds, err := r.buildDesired(ctx, c, sp)
			if err != nil {
				r.log.Warn("skipping service", "container", c.Name, "key", sp.Key, "err", err)
				continue
			}
			if existing, dup := desired[ds.service.Name]; dup {
				r.log.Warn("duplicate service name; keeping first",
					"name", ds.service.Name, "first", existing.containerName, "second", c.Name)
				continue
			}
			desired[ds.service.Name] = ds
		}
	}

	actual, err := r.nb.ListServices(ctx)
	if err != nil {
		return fmt.Errorf("list netbird services: %w", err)
	}
	byName := map[string]netbird.Service{}
	for _, s := range actual {
		byName[s.Name] = s
	}

	names := make([]string, 0, len(desired))
	for n := range desired {
		names = append(names, n)
	}
	sort.Strings(names)

	for _, name := range names {
		ds := desired[name]
		cur, exists := byName[name]
		if !exists {
			r.log.Info("creating service", "name", name, "domain", ds.service.Domain)
			if err := r.apply(ctx, nil, &ds.service); err != nil {
				r.log.Error("create failed", "name", name, "err", err)
			}
			continue
		}
		// Container is stopped AND user opted in to delete-on-stop → DELETE.
		if !ds.service.Enabled && ds.ephemeral {
			r.log.Info("deleting ephemeral service (container stopped)",
				"name", name, "id", cur.ID)
			if r.cfg.DryRun {
				r.log.Info("DRY_RUN", "action", "delete", "name", name, "id", cur.ID)
				continue
			}
			if err := r.nb.DeleteService(ctx, cur.ID); err != nil {
				r.log.Error("delete failed", "name", name, "err", err)
			}
			continue
		}
		// If the container is not running, just flip enabled=false on the
		// existing service. Don't try to re-derive port/host/etc — during a
		// `die` event, Docker may briefly report the container without its
		// port bindings, which would cause the labeled port to be used raw
		// instead of the previously-resolved host port (= churn + broken target).
		var merged netbird.Service
		if !ds.service.Enabled {
			merged = disableInPlace(cur)
		} else {
			// If the port wasn't resolved from a live binding, the container is
			// likely transitioning — keep the port that's already on the service
			// to avoid churn between raw label port and the resolved host port.
			if !ds.portFromBinding && len(cur.Targets) > 0 && len(ds.service.Targets) > 0 {
				if cur.Targets[0].Port != 0 && cur.Targets[0].Port != ds.service.Targets[0].Port {
					r.log.Debug("inheriting port from existing service (no live binding)",
						"name", name, "labeled", ds.labeledContainerPort, "kept", cur.Targets[0].Port)
					ds.service.Targets[0].Port = cur.Targets[0].Port
				}
			}
			merged = merge(cur, ds.service)
		}
		if servicesEqual(cur, merged) {
			r.log.Debug("service up-to-date", "name", name)
			continue
		}
		if r.log.Enabled(ctx, slog.LevelDebug) {
			curJSON, _ := json.Marshal(cur)
			mJSON, _ := json.Marshal(merged)
			r.log.Debug("service diff", "name", name, "cur", string(curJSON), "merged", string(mJSON))
		}
		r.log.Info("updating service", "name", name, "enabled", merged.Enabled)
		if err := r.apply(ctx, &cur, &merged); err != nil {
			r.log.Error("update failed", "name", name, "err", err)
		}
	}
	return nil
}

// resolveTarget maps a human-friendly target name to a NetBird ID using
// the appropriate API depending on target_type. For "subnet"/"resource"
// targets it also figures out which Network to search.
func (r *Reconciler) resolveTarget(ctx context.Context, targetType, networkID, networkName, targetName string) (string, error) {
	switch strings.ToLower(targetType) {
	case "peer":
		return r.resolver.PeerID(ctx, targetName)
	case "cluster":
		return r.resolver.ClusterID(ctx, targetName)
	case "subnet", "resource", "":
		// Pick the network to search inside.
		networkRef := networkID
		if networkRef == "" {
			networkRef = networkName
		}
		if networkRef == "" {
			if r.cfg.DefaultNetworkID != "" {
				networkRef = r.cfg.DefaultNetworkID
			} else {
				networkRef = r.cfg.DefaultNetworkName
			}
		}
		return r.resolver.ResourceID(ctx, networkRef, targetName)
	default:
		return "", fmt.Errorf("unknown target_type %q (expected peer, subnet, resource, or cluster)", targetType)
	}
}

// disableInPlace returns cur with enabled=false on the service and every target.
func disableInPlace(cur netbird.Service) netbird.Service {
	out := cur
	out.Enabled = false
	out.Targets = append([]netbird.Target(nil), cur.Targets...)
	for i := range out.Targets {
		out.Targets[i].Enabled = false
	}
	return out
}

// ReconcileContainer handles a single container event. We always do a full
// pass — it's the simplest correct approach and the API is cheap.
func (r *Reconciler) ReconcileContainer(ctx context.Context, id string) {
	if err := r.Reconcile(ctx); err != nil {
		r.log.Error("reconcile failed", "trigger", id, "err", err)
	}
}

type desiredService struct {
	service              netbird.Service
	containerName        string
	portFromBinding      bool // true if target.port was resolved from a Docker port binding
	labeledContainerPort int  // the raw labeled port (before resolution), 0 if not labeled
	ephemeral            bool // if container stops, DELETE the NetBird service instead of disabling
}

func (r *Reconciler) buildDesired(ctx context.Context, c docker.Container, sp labels.Spec) (*desiredService, error) {
	svc := sp.Service

	// Effective target mode (label > env default)
	mode := sp.TargetMode
	if mode == "" {
		mode = r.cfg.TargetMode
	}

	// Name
	if svc.Name == "" {
		if sp.Key != "" {
			svc.Name = c.Name + "-" + sp.Key
		} else {
			svc.Name = c.Name
		}
	}

	// Domain — required
	if svc.Domain == "" {
		if r.cfg.DefaultDomain != "" {
			svc.Domain = sanitizeSubdomain(svc.Name) + "." + r.cfg.DefaultDomain
		} else {
			return nil, fmt.Errorf("domain not set and no NETBIRD_DEFAULT_DOMAIN")
		}
	} else if !strings.Contains(svc.Domain, ".") && r.cfg.DefaultDomain != "" {
		svc.Domain = svc.Domain + "." + r.cfg.DefaultDomain
	}

	if svc.Mode == "" {
		svc.Mode = r.cfg.DefaultMode
	}

	if len(svc.Targets) == 0 {
		return nil, fmt.Errorf("no target")
	}
	t := svc.Targets[0]

	// Target type/id resolution
	if mode == "network" {
		// In network mode, the auto-provisioned resource is the canonical target.
		// Unless the user explicitly overrode it with labels.
		if t.TargetType == "" {
			t.TargetType = "subnet"
		}
		if t.TargetID == "" {
			np := r.provision()
			if sp.TargetName != "" {
				id, err := r.resolveTarget(ctx, t.TargetType, sp.NetworkID, sp.NetworkName, sp.TargetName)
				if err != nil {
					return nil, err
				}
				t.TargetID = id
			} else if np != nil && np.ResourceID != "" {
				t.TargetID = np.ResourceID
			} else {
				return nil, fmt.Errorf("target_mode=network but provisioner has not run yet (no resource id available)")
			}
		}
	} else {
		// host mode (existing behaviour)
		if t.TargetType == "" {
			t.TargetType = r.cfg.DefaultTargetType
		}
		if t.TargetID == "" {
			name := sp.TargetName
			if name == "" {
				if r.cfg.DefaultTargetID != "" {
					t.TargetID = r.cfg.DefaultTargetID
				} else {
					name = r.cfg.DefaultTargetName
				}
			}
			if t.TargetID == "" {
				if name == "" {
					return nil, fmt.Errorf("no target: set netbird.target.id/name on the container, or NETBIRD_DEFAULT_TARGET_ID/NAME")
				}
				id, err := r.resolveTarget(ctx, t.TargetType, sp.NetworkID, sp.NetworkName, name)
				if err != nil {
					return nil, err
				}
				t.TargetID = id
			}
		}
	}

	if t.Protocol == "" {
		t.Protocol = r.cfg.DefaultProtocol
	}

	// Host + port resolution — differs by mode
	labeledPort := t.Port
	portFromBinding := false

	if mode == "network" {
		// Use the container's IP on the shared docker network, and the labeled
		// port AS-IS (no host-binding resolution — we route directly into the
		// docker subnet, no NAT through host ports).
		np := r.provision()
		if t.Host == "" {
			if np == nil {
				return nil, fmt.Errorf("target_mode=network but provisioner has not run")
			}
			ip := c.Networks[np.DockerNetwork]
			if ip == "" {
				return nil, fmt.Errorf("container %s is not attached to docker network %q (attached: %v) — add it under `networks:` in compose",
					c.Name, np.DockerNetwork, networkNames(c.Networks))
			}
			t.Host = ip
		}
		if t.Port == 0 {
			return nil, fmt.Errorf("target.port not set")
		}
		// Port stays as labeled — it's the container's internal port.
		portFromBinding = true // a no-op in network mode but keeps the transition logic quiet
	} else {
		if t.Host == "" {
			if r.cfg.DefaultHost == "" {
				return nil, fmt.Errorf("target.host not set and no NETBIRD_DEFAULT_HOST")
			}
			t.Host = r.cfg.DefaultHost
		}
		if t.Port == 0 {
			return nil, fmt.Errorf("target.port not set")
		}
		// If user gave the container's internal port, resolve to host-published port.
		if mapped, ok := c.PortBindings[fmt.Sprintf("%d/tcp", t.Port)]; ok && mapped != 0 {
			if mapped != t.Port {
				r.log.Debug("resolved container port via Docker port binding",
					"container", c.Name, "container_port", t.Port, "host_port", mapped)
			}
			t.Port = mapped
			portFromBinding = true
		} else {
			for _, host := range c.PortBindings {
				if host == labeledPort {
					portFromBinding = true
					break
				}
			}
		}
	}

	// Target enabled = container running. Service-level enabled tracks the same,
	// unless user explicitly forced it off.
	t.Enabled = c.Running
	svc.Targets[0] = t

	svc.Enabled = c.Running
	if sp.EnabledExplicit != nil && !*sp.EnabledExplicit {
		svc.Enabled = false
		svc.Targets[0].Enabled = false
	}

	// Resolve group names → IDs. NetBird's Service API rejects bare names;
	// `admins` must be `<group-id>`. Aviary lets users write the friendly form,
	// so we do the lookup here. Unknown group names are warned about and
	// dropped — one typo shouldn't 403-lock an entire service.
	if len(svc.AccessGroups) > 0 {
		ids, missing, err := r.resolver.GroupIDs(ctx, svc.AccessGroups)
		if err != nil {
			return nil, fmt.Errorf("access_groups: %w", err)
		}
		if len(missing) > 0 {
			r.log.Warn("unknown groups in netbird.access_groups — dropping; create them in the NetBird UI",
				"service", svc.Name, "missing", missing, "resolved", ids)
		}
		svc.AccessGroups = ids
	}
	if svc.Auth != nil && svc.Auth.BearerAuth != nil && len(svc.Auth.BearerAuth.DistributionGroups) > 0 {
		ids, missing, err := r.resolver.GroupIDs(ctx, svc.Auth.BearerAuth.DistributionGroups)
		if err != nil {
			return nil, fmt.Errorf("auth.sso_groups: %w", err)
		}
		if len(missing) > 0 {
			r.log.Warn("unknown groups in netbird.auth.sso_groups — dropping",
				"service", svc.Name, "missing", missing, "resolved", ids)
		}
		svc.Auth.BearerAuth.DistributionGroups = ids
	}

	return &desiredService{
		service:              svc,
		containerName:        c.Name,
		portFromBinding:      portFromBinding,
		labeledContainerPort: labeledPort,
		ephemeral:            sp.Ephemeral,
	}, nil
}

func (r *Reconciler) apply(ctx context.Context, cur, desired *netbird.Service) error {
	if r.cfg.DryRun {
		r.log.Info("DRY_RUN", "action", actionLabel(cur), "name", desired.Name, "service", desired)
		return nil
	}
	if cur == nil {
		_, err := r.nb.CreateService(ctx, desired)
		return err
	}
	_, err := r.nb.UpdateService(ctx, cur.ID, desired)
	return err
}

func actionLabel(cur *netbird.Service) string {
	if cur == nil {
		return "create"
	}
	return "update"
}

// merge takes the existing service (cur) and overlays the desired fields,
// preserving server-assigned fields (ID, meta, port_auto_assigned) and any
// fields the user may have configured manually that we don't manage.
func merge(cur, d netbird.Service) netbird.Service {
	out := d
	out.ID = cur.ID
	out.Meta = cur.Meta
	out.PortAutoAssigned = cur.PortAutoAssigned
	if out.ProxyCluster == "" {
		out.ProxyCluster = cur.ProxyCluster
	}
	if out.ListenPort == 0 {
		out.ListenPort = cur.ListenPort
	}
	return out
}

// servicesEqual compares fields we manage via JSON canonicalization, ignoring
// fields the server normalizes on its own:
//   - id / meta / port_auto_assigned / terminated → server-assigned
//   - target.options == {}                        → server returns empty object even when we omit it
//   - secret values (password/pin/header value)   → server redacts these in responses
//     (we still SEND them on create/update; we just can't diff them)
func servicesEqual(a, b netbird.Service) bool {
	normalize := func(s netbird.Service) []byte {
		s.ID = ""
		s.Meta = nil
		s.PortAutoAssigned = false
		s.Terminated = false
		for i := range s.Targets {
			if s.Targets[i].Options != nil && isEmptyOptions(s.Targets[i].Options) {
				s.Targets[i].Options = nil
			}
		}
		if s.Auth != nil {
			if s.Auth.PasswordAuth != nil {
				s.Auth.PasswordAuth.Password = ""
			}
			if s.Auth.PINAuth != nil {
				s.Auth.PINAuth.PIN = ""
			}
			for i := range s.Auth.HeaderAuths {
				s.Auth.HeaderAuths[i].Value = ""
			}
			if isEmptyAuth(s.Auth) {
				s.Auth = nil
			}
		}
		if s.AccessRestrictions != nil && isEmptyAccessRestrictions(s.AccessRestrictions) {
			s.AccessRestrictions = nil
		}
		out, _ := json.Marshal(s)
		return out
	}
	return string(normalize(a)) == string(normalize(b))
}

func isEmptyOptions(o *netbird.TargetOptions) bool {
	if o == nil {
		return true
	}
	return !o.SkipTLSVerify && o.RequestTimeout == "" && o.PathRewrite == "" &&
		len(o.CustomHeaders) == 0 && !o.ProxyProtocol &&
		o.SessionIdleTimeout == "" && !o.DirectUpstream
}

func isEmptyAuth(a *netbird.Auth) bool {
	if a == nil {
		return true
	}
	hasPwd := a.PasswordAuth != nil && a.PasswordAuth.Enabled
	hasPIN := a.PINAuth != nil && a.PINAuth.Enabled
	hasBearer := a.BearerAuth != nil && a.BearerAuth.Enabled
	hasLink := a.LinkAuth != nil && a.LinkAuth.Enabled
	hasHeader := false
	for _, h := range a.HeaderAuths {
		if h.Enabled {
			hasHeader = true
			break
		}
	}
	return !(hasPwd || hasPIN || hasBearer || hasLink || hasHeader)
}

func isEmptyAccessRestrictions(a *netbird.AccessRestrictions) bool {
	if a == nil {
		return true
	}
	return len(a.AllowedCIDRs) == 0 && len(a.BlockedCIDRs) == 0 &&
		len(a.AllowedCountries) == 0 && len(a.BlockedCountries) == 0 &&
		(a.CrowdsecMode == "" || a.CrowdsecMode == "off")
}

func sanitizeSubdomain(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '_' || r == ' ':
			b.WriteByte('-')
		case r == '-' || r == '.':
			b.WriteRune(r)
		}
	}
	return strings.Trim(b.String(), "-.")
}
