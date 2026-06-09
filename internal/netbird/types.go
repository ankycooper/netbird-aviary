package netbird

type Service struct {
	ID                 string              `json:"id,omitempty"`
	Name               string              `json:"name"`
	Domain             string              `json:"domain"`
	Mode               string              `json:"mode,omitempty"`
	ListenPort         int                 `json:"listen_port,omitempty"`
	PortAutoAssigned   bool                `json:"port_auto_assigned,omitempty"`
	ProxyCluster       string              `json:"proxy_cluster,omitempty"`
	Targets            []Target            `json:"targets"`
	Enabled            bool                `json:"enabled"`
	Terminated         bool                `json:"terminated,omitempty"`
	PassHostHeader     bool                `json:"pass_host_header,omitempty"`
	RewriteRedirects   bool                `json:"rewrite_redirects,omitempty"`
	Auth               *Auth               `json:"auth,omitempty"`
	AccessRestrictions *AccessRestrictions `json:"access_restrictions,omitempty"`
	Private            bool                `json:"private,omitempty"`
	AccessGroups       []string            `json:"access_groups,omitempty"`
	Meta               *ServiceMeta        `json:"meta,omitempty"`
}

type ServiceMeta struct {
	CreatedAt           string `json:"created_at,omitempty"`
	CertificateIssuedAt string `json:"certificate_issued_at,omitempty"`
	Status              string `json:"status,omitempty"`
}

type Target struct {
	TargetID   string         `json:"target_id"`
	TargetType string         `json:"target_type"`
	Path       string         `json:"path,omitempty"`
	Protocol   string         `json:"protocol"`
	Host       string         `json:"host,omitempty"`
	Port       int            `json:"port"`
	Enabled    bool           `json:"enabled"`
	Options    *TargetOptions `json:"options,omitempty"`
}

type TargetOptions struct {
	SkipTLSVerify      bool              `json:"skip_tls_verify,omitempty"`
	RequestTimeout     string            `json:"request_timeout,omitempty"`
	PathRewrite        string            `json:"path_rewrite,omitempty"`
	CustomHeaders      map[string]string `json:"custom_headers,omitempty"`
	ProxyProtocol      bool              `json:"proxy_protocol,omitempty"`
	SessionIdleTimeout string            `json:"session_idle_timeout,omitempty"`
	DirectUpstream     bool              `json:"direct_upstream,omitempty"`
}

type Auth struct {
	PasswordAuth *PasswordAuth `json:"password_auth,omitempty"`
	PINAuth      *PINAuth      `json:"pin_auth,omitempty"`
	BearerAuth   *BearerAuth   `json:"bearer_auth,omitempty"`
	LinkAuth     *LinkAuth     `json:"link_auth,omitempty"`
	HeaderAuths  []HeaderAuth  `json:"header_auths,omitempty"`
}

type PasswordAuth struct {
	Enabled  bool   `json:"enabled"`
	Password string `json:"password"`
}

type PINAuth struct {
	Enabled bool   `json:"enabled"`
	PIN     string `json:"pin"`
}

type BearerAuth struct {
	Enabled            bool     `json:"enabled"`
	DistributionGroups []string `json:"distribution_groups,omitempty"`
}

type LinkAuth struct {
	Enabled bool `json:"enabled"`
}

type HeaderAuth struct {
	Enabled bool   `json:"enabled"`
	Header  string `json:"header"`
	Value   string `json:"value"`
}

// --- lookup types ---

type Peer struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Hostname string `json:"hostname"`
	IP       string `json:"ip"`
}

type Network struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Resources []string `json:"resources"`
}

type NetworkResource struct {
	ID          string   `json:"id,omitempty"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Type        string   `json:"type,omitempty"`
	Address     string   `json:"address"`
	Enabled     bool     `json:"enabled"`
	Groups      []string `json:"groups"`
}

type NetworkRouter struct {
	ID         string   `json:"id,omitempty"`
	Peer       string   `json:"peer,omitempty"`
	PeerGroups []string `json:"peer_groups,omitempty"`
	Metric     int      `json:"metric"`
	Masquerade bool     `json:"masquerade"`
	Enabled    bool     `json:"enabled"`
}

type NetworkCreate struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
}

type ProxyCluster struct {
	ID      string `json:"id"`
	Address string `json:"address"`
	Type    string `json:"type"`
	Online  bool   `json:"online"`
}

type Group struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type AccessRestrictions struct {
	AllowedCIDRs     []string `json:"allowed_cidrs,omitempty"`
	BlockedCIDRs     []string `json:"blocked_cidrs,omitempty"`
	AllowedCountries []string `json:"allowed_countries,omitempty"`
	BlockedCountries []string `json:"blocked_countries,omitempty"`
	CrowdsecMode     string   `json:"crowdsec_mode,omitempty"`
}
