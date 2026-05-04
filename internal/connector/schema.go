// Package connector implements the HTTP adapter connector system,
// allowing external HTTP services to act as Discovery/Notify sources.
package connector

import (
	"errors"
	"fmt"
	"os"

	"github.com/pelletier/go-toml/v2"
)

// ErrInvalidConfig is returned by LoadConfig / LoadConfigFromBytes for
// any validation failure.
var ErrInvalidConfig = errors.New("connector: invalid config")

// ConnectorConfig is the parsed view of a connector.toml file.
type ConnectorConfig struct {
	Connector ConnectorSection
	Endpoint  EndpointSection
	Auth      AuthSection
}

// ConnectorSection holds the connector identity fields.
type ConnectorSection struct {
	Name           string
	Type           string // "discover" | "notify" | "trigger" | "store"
	AdapterVersion string
	Description    string
}

// EndpointSection holds the HTTP endpoint URLs for each adapter type.
type EndpointSection struct {
	Discover string
	Notify   string
	Trigger  string
	Store    string
}

// AuthSection holds the authentication configuration.
type AuthSection struct {
	Type  string // "bearer" | "basic" | "none"
	Token string
}

var validConnectorTypes = map[string]struct{}{
	"discover": {},
	"notify":   {},
	"trigger":  {},
	"store":    {},
}

var validAuthTypes = map[string]struct{}{
	"bearer": {},
	"basic":  {},
	"none":   {},
}

type rawConfig struct {
	Connector struct {
		Name           string `toml:"name"`
		Type           string `toml:"type"`
		AdapterVersion string `toml:"adapter_version"`
		Description    string `toml:"description"`
	} `toml:"connector"`
	Endpoint struct {
		Discover string `toml:"discover"`
		Notify   string `toml:"notify"`
		Trigger  string `toml:"trigger"`
		Store    string `toml:"store"`
	} `toml:"endpoint"`
	Auth struct {
		Type  string `toml:"type"`
		Token string `toml:"token"`
	} `toml:"auth"`
}

// LoadConfig reads path and parses the connector.toml.
func LoadConfig(path string) (*ConnectorConfig, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read connector config %s: %w", path, err)
	}
	return LoadConfigFromBytes(body)
}

// LoadConfigFromBytes parses and validates connector.toml bytes.
func LoadConfigFromBytes(body []byte) (*ConnectorConfig, error) {
	var raw rawConfig
	if err := toml.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("%w: parse: %v", ErrInvalidConfig, err)
	}

	if raw.Connector.Name == "" {
		return nil, fmt.Errorf("%w: missing required field connector.name", ErrInvalidConfig)
	}
	if _, ok := validConnectorTypes[raw.Connector.Type]; !ok {
		return nil, fmt.Errorf("%w: invalid connector.type %q (must be discover|notify|trigger|store)", ErrInvalidConfig, raw.Connector.Type)
	}
	authType := raw.Auth.Type
	if authType == "" {
		authType = "none"
	}
	if _, ok := validAuthTypes[authType]; !ok {
		return nil, fmt.Errorf("%w: invalid auth.type %q (must be bearer|basic|none)", ErrInvalidConfig, raw.Auth.Type)
	}

	return &ConnectorConfig{
		Connector: ConnectorSection{
			Name:           raw.Connector.Name,
			Type:           raw.Connector.Type,
			AdapterVersion: raw.Connector.AdapterVersion,
			Description:    raw.Connector.Description,
		},
		Endpoint: EndpointSection{
			Discover: raw.Endpoint.Discover,
			Notify:   raw.Endpoint.Notify,
			Trigger:  raw.Endpoint.Trigger,
			Store:    raw.Endpoint.Store,
		},
		Auth: AuthSection{
			Type:  authType,
			Token: os.ExpandEnv(raw.Auth.Token),
		},
	}, nil
}
