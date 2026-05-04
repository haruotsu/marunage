package connector_test

import (
	"os"
	"testing"

	"github.com/haruotsu/marunage/internal/connector"
)

const validTOML = `
[connector]
name = "my-webhook"
type = "discover"
adapter_version = "v1"
description = "My custom webhook source"

[endpoint]
discover = "http://localhost:8080/discover"
notify = "http://localhost:8080/notify"

[auth]
type = "bearer"
token = "secret"
`

func TestLoadConfig_Valid(t *testing.T) {
	cfg, err := connector.LoadConfigFromBytes([]byte(validTOML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Connector.Name != "my-webhook" {
		t.Errorf("name: got %q, want %q", cfg.Connector.Name, "my-webhook")
	}
	if cfg.Connector.Type != "discover" {
		t.Errorf("type: got %q, want %q", cfg.Connector.Type, "discover")
	}
	if cfg.Connector.AdapterVersion != "v1" {
		t.Errorf("adapter_version: got %q, want %q", cfg.Connector.AdapterVersion, "v1")
	}
	if cfg.Endpoint.Discover != "http://localhost:8080/discover" {
		t.Errorf("endpoint.discover: got %q", cfg.Endpoint.Discover)
	}
	if cfg.Auth.Type != "bearer" {
		t.Errorf("auth.type: got %q, want %q", cfg.Auth.Type, "bearer")
	}
	if cfg.Auth.Token != "secret" {
		t.Errorf("auth.token: got %q, want %q", cfg.Auth.Token, "secret")
	}
}

func TestLoadConfig_EnvExpansion(t *testing.T) {
	t.Setenv("MY_TOKEN", "expanded-token")
	const toml = `
[connector]
name = "env-test"
type = "discover"
adapter_version = "v1"

[endpoint]
discover = "http://localhost:8080/discover"

[auth]
type = "bearer"
token = "${MY_TOKEN}"
`
	cfg, err := connector.LoadConfigFromBytes([]byte(toml))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Auth.Token != "expanded-token" {
		t.Errorf("token: got %q, want %q", cfg.Auth.Token, "expanded-token")
	}
}

func TestLoadConfig_MissingName(t *testing.T) {
	const toml = `
[connector]
type = "discover"
adapter_version = "v1"

[endpoint]
discover = "http://localhost:8080/discover"

[auth]
type = "none"
`
	_, err := connector.LoadConfigFromBytes([]byte(toml))
	if err == nil {
		t.Fatal("expected error for missing name")
	}
}

func TestLoadConfig_InvalidType(t *testing.T) {
	const toml = `
[connector]
name = "bad"
type = "unknown"
adapter_version = "v1"

[endpoint]
discover = "http://localhost:8080/discover"

[auth]
type = "none"
`
	_, err := connector.LoadConfigFromBytes([]byte(toml))
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
}

func TestLoadConfig_InvalidAuthType(t *testing.T) {
	const toml = `
[connector]
name = "bad-auth"
type = "discover"
adapter_version = "v1"

[endpoint]
discover = "http://localhost:8080/discover"

[auth]
type = "api-key"
`
	_, err := connector.LoadConfigFromBytes([]byte(toml))
	if err == nil {
		t.Fatal("expected error for invalid auth type")
	}
}

func TestLoadConfigFile(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "connector*.toml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(validTOML); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	cfg, err := connector.LoadConfig(f.Name())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Connector.Name != "my-webhook" {
		t.Errorf("name: got %q", cfg.Connector.Name)
	}
}
