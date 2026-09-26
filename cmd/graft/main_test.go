package main

import (
	"testing"

	"github.com/skssmd/graft/internal/config"
)

func cfgWith(defaultName string, registries ...string) *config.GlobalConfig {
	servers := map[string]config.ServerConfig{}
	for _, r := range registries {
		servers[r] = config.ServerConfig{RegistryName: r, Host: r + ".example", Port: 22, User: "root"}
	}
	return &config.GlobalConfig{Default: defaultName, Servers: servers}
}

func TestResolveDefaultRegistry(t *testing.T) {
	tests := []struct {
		name         string
		cfg          *config.GlobalConfig
		command      string
		projectFound bool
		want         string
	}{
		{
			name:         "no project and a valid default falls back",
			cfg:          cfgWith("contabo", "contabo", "azure"),
			command:      "ps",
			projectFound: false,
			want:         "contabo",
		},
		{
			name:         "a project in the current directory wins over the default",
			cfg:          cfgWith("contabo", "contabo"),
			command:      "ps",
			projectFound: true,
			want:         "",
		},
		{
			name:         "no default configured means no fallback",
			cfg:          cfgWith("", "contabo"),
			command:      "ps",
			projectFound: false,
			want:         "",
		},
		{
			name:         "a default naming a deleted registry does not fall back",
			cfg:          cfgWith("gone", "contabo"),
			command:      "ps",
			projectFound: false,
			want:         "",
		},
		{
			name:         "nil config does not panic or fall back",
			cfg:          nil,
			command:      "ps",
			projectFound: false,
			want:         "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := resolveDefaultRegistry(tt.cfg, tt.command, tt.projectFound)
			if got != tt.want {
				t.Fatalf("resolveDefaultRegistry(%q, projectFound=%v) = %q, want %q",
					tt.command, tt.projectFound, got, tt.want)
			}
		})
	}
}

// Commands that already work without a project must not be redirected to the
// default registry - doing so would turn "graft init" into "sudo docker init".
func TestResolveDefaultRegistrySkipsProjectlessCommands(t *testing.T) {
	cfg := cfgWith("contabo", "contabo")

	for _, command := range []string{"init", "registry", "projects", "pub"} {
		t.Run(command, func(t *testing.T) {
			if got := resolveDefaultRegistry(cfg, command, false); got != "" {
				t.Fatalf("command %q was redirected to registry %q; it works without a project", command, got)
			}
		})
	}
}

// Commands that need a server should fall back, including the ones the -r path
// handles specially rather than passing through to docker.
func TestResolveDefaultRegistryCoversRegistryCapableCommands(t *testing.T) {
	cfg := cfgWith("contabo", "contabo")

	for _, command := range []string{"ps", "up", "down", "-sh", "--sh", "psql", "db", "redis", "tunnel", "host", "logs", "sync"} {
		t.Run(command, func(t *testing.T) {
			if got := resolveDefaultRegistry(cfg, command, false); got != "contabo" {
				t.Fatalf("command %q did not fall back to the default registry (got %q)", command, got)
			}
		})
	}
}

func TestParseTunnelPortFlag(t *testing.T) {
	tests := []struct {
		name       string
		args       []string
		wantRemote int
		wantLocal  int
	}{
		{"no flag auto-detects", []string{"mycontainer"}, 0, 0},
		{"-port remote:local", []string{"-port", "5000:8080"}, 5000, 8080},
		{"--port remote:local", []string{"--port", "5000:8080"}, 5000, 8080},
		{"-port single applies to both", []string{"-port", "3000"}, 3000, 3000},
		{"-p is the project flag and is ignored", []string{"-p", "5000:8080"}, 0, 0},
		{"flag after other args", []string{"c", "-port", "80:8080"}, 80, 8080},
		{"missing value is ignored", []string{"-port"}, 0, 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remote, local := parseTunnelPortFlag(tt.args)
			if remote != tt.wantRemote || local != tt.wantLocal {
				t.Fatalf("parseTunnelPortFlag(%v) = (%d, %d), want (%d, %d)",
					tt.args, remote, local, tt.wantRemote, tt.wantLocal)
			}
		})
	}
}
