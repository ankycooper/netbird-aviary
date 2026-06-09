package labels

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/ankycooper/netbird-aviary/internal/netbird"
)

// Spec is one desired service derived from a container's labels.
// Defaults still need to be applied by the reconciler.
type Spec struct {
	Key             string // service key from labels: "" for shorthand, else <svc>
	Service         netbird.Service
	HasPort         bool // target.port was explicitly set
	HasHost         bool
	HasTargetID     bool
	HasTargetType   bool
	HasProtocol     bool
	HasPassHost     bool // pass_host_header was set
	HasRewrite      bool
	EnabledExplicit *bool
	Ephemeral       bool // if true, DELETE the NetBird service when the container stops (default: false = disable in place)

	// Friendly-name overrides — resolved to IDs by the reconciler.
	TargetName  string // netbird.target.name
	NetworkID   string // netbird.network.id
	NetworkName string // netbird.network[.name]

	// Routing mode override; empty => use controller default
	TargetMode string // netbird.target_mode: "host" or "network"
}

// Parse extracts every desired Spec from a container's labels.
// Returns nil if netbird.enable != true (container is not managed).
func Parse(prefix string, raw map[string]string) ([]Spec, error) {
	master := raw[prefix+".enable"]
	if !truthy(master) {
		return nil, nil
	}

	// Discover service keys. A label of form `<prefix>.services.<svc>.<rest>` => key="<svc>".
	// Any label of form `<prefix>.<rest>` (and not `<prefix>.services.*`) => key="".
	keys := map[string]bool{}
	hasShort := false
	servicesRoot := prefix + ".services."
	for k := range raw {
		if !strings.HasPrefix(k, prefix+".") {
			continue
		}
		rest := k[len(prefix)+1:]
		if rest == "enable" {
			continue
		}
		if strings.HasPrefix(rest, "services.") {
			svc := rest[len("services."):]
			i := strings.IndexByte(svc, '.')
			if i < 0 {
				continue
			}
			keys[svc[:i]] = true
		} else {
			hasShort = true
		}
	}

	var out []Spec
	if hasShort {
		spec, err := parseOne(prefix+".", "", raw)
		if err != nil {
			return nil, err
		}
		out = append(out, spec)
	}
	// Stable order so behaviour is reproducible.
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)
	for _, key := range ordered {
		spec, err := parseOne(servicesRoot+key+".", key, raw)
		if err != nil {
			return nil, fmt.Errorf("service %q: %w", key, err)
		}
		out = append(out, spec)
	}
	return out, nil
}

func parseOne(base, key string, raw map[string]string) (Spec, error) {
	s := Spec{Key: key}
	svc := &s.Service

	if v, ok := raw[base+"name"]; ok {
		svc.Name = v
	}
	if v, ok := raw[base+"domain"]; ok {
		svc.Domain = v
	}
	if v, ok := raw[base+"mode"]; ok {
		svc.Mode = v
	}
	if v, ok := raw[base+"listen_port"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return s, fmt.Errorf("listen_port: %w", err)
		}
		svc.ListenPort = n
	}
	if v, ok := raw[base+"proxy_cluster"]; ok {
		svc.ProxyCluster = v
	}
	if v, ok := raw[base+"enable"]; ok {
		b := truthy(v)
		s.EnabledExplicit = &b
	}
	if v, ok := raw[base+"ephemeral"]; ok {
		s.Ephemeral = truthy(v)
	}
	if v, ok := raw[base+"target_mode"]; ok {
		s.TargetMode = strings.ToLower(strings.TrimSpace(v))
	}
	if v, ok := raw[base+"private"]; ok {
		svc.Private = truthy(v)
	}
	if v, ok := raw[base+"access_groups"]; ok {
		svc.AccessGroups = splitCSV(v)
	}
	for _, k := range []string{base + "advanced.pass_host_header", base + "pass_host_header"} {
		if v, ok := raw[k]; ok {
			svc.PassHostHeader = truthy(v)
			s.HasPassHost = true
			break
		}
	}
	for _, k := range []string{base + "advanced.rewrite_redirects", base + "rewrite_redirects"} {
		if v, ok := raw[k]; ok {
			svc.RewriteRedirects = truthy(v)
			s.HasRewrite = true
			break
		}
	}

	// Target (single, with shorthand)
	t := netbird.Target{Enabled: true}
	opts := &netbird.TargetOptions{CustomHeaders: map[string]string{}}

	if v, ok := raw[base+"target.type"]; ok {
		t.TargetType = v
		s.HasTargetType = true
	}
	if v, ok := raw[base+"target.id"]; ok {
		t.TargetID = v
		s.HasTargetID = true
	}
	if v, ok := raw[base+"target.name"]; ok {
		s.TargetName = v
	}
	if v, ok := raw[base+"network.id"]; ok {
		s.NetworkID = v
	}
	for _, k := range []string{base + "network.name", base + "network"} {
		if v, ok := raw[k]; ok {
			s.NetworkName = v
			break
		}
	}
	if v, ok := raw[base+"target.protocol"]; ok {
		t.Protocol = v
		s.HasProtocol = true
	} else if v, ok := raw[base+"protocol"]; ok {
		t.Protocol = v
		s.HasProtocol = true
	}
	if v, ok := raw[base+"target.host"]; ok {
		t.Host = v
		s.HasHost = true
	} else if v, ok := raw[base+"host"]; ok {
		t.Host = v
		s.HasHost = true
	}
	if v, ok := raw[base+"target.port"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return s, fmt.Errorf("target.port: %w", err)
		}
		t.Port = n
		s.HasPort = true
	} else if v, ok := raw[base+"port"]; ok {
		n, err := strconv.Atoi(v)
		if err != nil {
			return s, fmt.Errorf("port: %w", err)
		}
		t.Port = n
		s.HasPort = true
	}
	if v, ok := raw[base+"target.path"]; ok {
		t.Path = v
	}
	if v, ok := raw[base+"target.skip_tls_verify"]; ok {
		opts.SkipTLSVerify = truthy(v)
	}
	if v, ok := raw[base+"target.request_timeout"]; ok {
		opts.RequestTimeout = v
	}
	if v, ok := raw[base+"target.path_rewrite"]; ok {
		opts.PathRewrite = v
	}
	if v, ok := raw[base+"target.proxy_protocol"]; ok {
		opts.ProxyProtocol = truthy(v)
	}
	if v, ok := raw[base+"target.session_idle_timeout"]; ok {
		opts.SessionIdleTimeout = v
	}
	if v, ok := raw[base+"target.direct_upstream"]; ok {
		opts.DirectUpstream = truthy(v)
	}
	// Custom headers
	headerPrefix := base + "target.headers."
	for k, v := range raw {
		if strings.HasPrefix(k, headerPrefix) {
			opts.CustomHeaders[k[len(headerPrefix):]] = v
		}
	}
	if len(opts.CustomHeaders) == 0 {
		opts.CustomHeaders = nil
	}
	if hasAnyOption(opts) {
		t.Options = opts
	}
	svc.Targets = []netbird.Target{t}

	// Auth
	auth := &netbird.Auth{}
	authSet := false
	if v, ok := raw[base+"auth.password"]; ok {
		auth.PasswordAuth = &netbird.PasswordAuth{Enabled: true, Password: v}
		authSet = true
	}
	if v, ok := raw[base+"auth.pin"]; ok {
		auth.PINAuth = &netbird.PINAuth{Enabled: true, PIN: v}
		authSet = true
	}
	if v, ok := raw[base+"auth.sso"]; ok {
		auth.BearerAuth = &netbird.BearerAuth{Enabled: truthy(v)}
		if g, ok := raw[base+"auth.sso_groups"]; ok {
			auth.BearerAuth.DistributionGroups = splitCSV(g)
		}
		authSet = true
	}
	if v, ok := raw[base+"auth.link"]; ok {
		auth.LinkAuth = &netbird.LinkAuth{Enabled: truthy(v)}
		authSet = true
	}
	headerAuthPrefix := base + "auth.header."
	for k, v := range raw {
		if strings.HasPrefix(k, headerAuthPrefix) {
			name := k[len(headerAuthPrefix):]
			auth.HeaderAuths = append(auth.HeaderAuths, netbird.HeaderAuth{
				Enabled: true,
				Header:  name,
				Value:   v,
			})
			authSet = true
		}
	}
	if authSet {
		sort.Slice(auth.HeaderAuths, func(i, j int) bool {
			return auth.HeaderAuths[i].Header < auth.HeaderAuths[j].Header
		})
		svc.Auth = auth
	}

	// Access restrictions
	ar := &netbird.AccessRestrictions{}
	arSet := false
	if v, ok := raw[base+"access.crowdsec"]; ok {
		ar.CrowdsecMode = v
		arSet = true
	}
	if v, ok := raw[base+"access.allow_cidrs"]; ok {
		ar.AllowedCIDRs = splitCSV(v)
		arSet = true
	}
	if v, ok := raw[base+"access.block_cidrs"]; ok {
		ar.BlockedCIDRs = splitCSV(v)
		arSet = true
	}
	if v, ok := raw[base+"access.allow_countries"]; ok {
		ar.AllowedCountries = splitCSV(v)
		arSet = true
	}
	if v, ok := raw[base+"access.block_countries"]; ok {
		ar.BlockedCountries = splitCSV(v)
		arSet = true
	}
	if arSet {
		svc.AccessRestrictions = ar
	}

	return s, nil
}

func hasAnyOption(o *netbird.TargetOptions) bool {
	if o == nil {
		return false
	}
	return o.SkipTLSVerify || o.RequestTimeout != "" || o.PathRewrite != "" ||
		len(o.CustomHeaders) > 0 || o.ProxyProtocol ||
		o.SessionIdleTimeout != "" || o.DirectUpstream
}

func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "t", "true", "yes", "on", "enabled":
		return true
	}
	return false
}

func splitCSV(v string) []string {
	parts := strings.Split(v, ",")
	out := parts[:0]
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
