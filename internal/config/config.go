package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	APIURL   string
	APIToken string

	LabelPrefix string

	DefaultTargetType string
	DefaultTargetID   string // explicit ID (takes precedence)
	DefaultTargetName string // friendly name, resolved via NetBird API
	DefaultNetworkID  string // for resource-type targets
	DefaultNetworkName string
	DefaultHost       string
	DefaultProtocol   string
	DefaultMode       string
	DefaultDomain      string

	// Routing mode + embedded netbird agent settings.
	TargetMode      string // "host" (default) or "network"
	DockerNetwork   string // explicit override; empty => auto-detect
	NetworkName     string // name of the NetBird Network to provision (default: docker network name)
	SetupKey        string // NETBIRD_SETUP_KEY — required if TargetMode=network and embedded agent is used
	PeerHostname    string // override peer hostname; empty => auto-derive
	AgentManagementURL string // override management url for the embedded agent (defaults to APIURL)
	DisableAgent    bool   // if true, don't try to spawn the embedded netbird agent (assume an external one)

	ReconcileInterval time.Duration
	HTTPTimeout       time.Duration
	DryRun            bool
	LogLevel          string
}

func Load() (*Config, error) {
	c := &Config{
		APIURL:            strings.TrimRight(os.Getenv("NETBIRD_API_URL"), "/"),
		APIToken:          os.Getenv("NETBIRD_API_TOKEN"),
		LabelPrefix:       envDefault("LABEL_PREFIX", "netbird"),
		DefaultTargetType:  envDefault("NETBIRD_DEFAULT_TARGET_TYPE", "subnet"),
		DefaultTargetID:    os.Getenv("NETBIRD_DEFAULT_TARGET_ID"),
		DefaultTargetName:  os.Getenv("NETBIRD_DEFAULT_TARGET_NAME"),
		DefaultNetworkID:   os.Getenv("NETBIRD_DEFAULT_NETWORK_ID"),
		DefaultNetworkName: os.Getenv("NETBIRD_DEFAULT_NETWORK_NAME"),
		DefaultHost:       os.Getenv("NETBIRD_DEFAULT_HOST"),
		DefaultProtocol:   envDefault("NETBIRD_DEFAULT_PROTOCOL", "http"),
		DefaultMode:       envDefault("NETBIRD_DEFAULT_MODE", "http"), // L7 reverse proxy — valid: http, tcp, udp, tls
		DefaultDomain:     os.Getenv("NETBIRD_DEFAULT_DOMAIN"),
		TargetMode:        strings.ToLower(envDefault("NETBIRD_TARGET_MODE", "host")),
		DockerNetwork:     os.Getenv("NETBIRD_DOCKER_NETWORK"),
		NetworkName:       os.Getenv("NETBIRD_NETWORK_NAME"),
		SetupKey:          os.Getenv("NETBIRD_SETUP_KEY"),
		PeerHostname:      os.Getenv("NETBIRD_PEER_HOSTNAME"),
		AgentManagementURL: os.Getenv("NETBIRD_AGENT_MANAGEMENT_URL"),
		LogLevel:          envDefault("LOG_LEVEL", "info"),
	}
	if v := os.Getenv("NETBIRD_DISABLE_AGENT"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("NETBIRD_DISABLE_AGENT: %w", err)
		}
		c.DisableAgent = b
	}
	if c.TargetMode != "host" && c.TargetMode != "network" {
		return nil, fmt.Errorf("NETBIRD_TARGET_MODE must be 'host' or 'network', got %q", c.TargetMode)
	}

	interval, err := time.ParseDuration(envDefault("RECONCILE_INTERVAL", "5m"))
	if err != nil {
		return nil, fmt.Errorf("RECONCILE_INTERVAL: %w", err)
	}
	c.ReconcileInterval = interval

	httpTimeout, err := time.ParseDuration(envDefault("HTTP_TIMEOUT", "30s"))
	if err != nil {
		return nil, fmt.Errorf("HTTP_TIMEOUT: %w", err)
	}
	c.HTTPTimeout = httpTimeout

	if v := os.Getenv("DRY_RUN"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("DRY_RUN: %w", err)
		}
		c.DryRun = b
	}

	if c.APIURL == "" {
		return nil, fmt.Errorf("NETBIRD_API_URL is required")
	}
	if c.APIToken == "" {
		return nil, fmt.Errorf("NETBIRD_API_TOKEN is required")
	}
	return c, nil
}

func envDefault(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
