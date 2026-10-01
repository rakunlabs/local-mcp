package config

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

func TestLoadCORSMergesOverDefaults(t *testing.T) {
	file := filepath.Join(t.TempDir(), "local.yaml")
	if err := os.WriteFile(file, []byte("http:\n  cors:\n    allow_origins:\n      - https://app.example.com\n    allow_private_network: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CONFIG_FILE", file)

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	cors := cfg.HTTP.CORS
	if !slices.Equal(cors.AllowOrigins, []string{"https://app.example.com"}) {
		t.Errorf("allow_origins = %v", cors.AllowOrigins)
	}

	if cors.AllowPrivateNetwork {
		t.Error("allow_private_network should be overridden to false")
	}

	defaults := DefaultCORS()
	if !slices.Equal(cors.AllowHeaders, defaults.AllowHeaders) || cors.MaxAge != 600 {
		t.Errorf("unset keys must keep defaults: %+v", cors)
	}
}

func TestLoadCORSDefault(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Chdir(t.TempDir())

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(cfg.HTTP.CORS.AllowOrigins, []string{"*"}) || !cfg.HTTP.CORS.AllowPrivateNetwork {
		t.Errorf("default cors = %+v", cfg.HTTP.CORS)
	}
}

func writeConfig(t *testing.T, dir, logLevel string) {
	t.Helper()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "local.yaml"), []byte("log_level: "+logLevel+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestLoadSearchOrder(t *testing.T) {
	home := t.TempDir()
	work := t.TempDir()

	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Chdir(work)

	writeConfig(t, filepath.Join(home, ".config", "local-mcp"), "warn")

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if cfg.LogLevel != "warn" {
		t.Errorf("~/.config/local-mcp not used: log_level = %q", cfg.LogLevel)
	}

	writeConfig(t, work, "error")

	cfg, err = Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if cfg.LogLevel != "error" {
		t.Errorf("working directory must win: log_level = %q", cfg.LogLevel)
	}
}

func TestConfigFolders(t *testing.T) {
	t.Setenv("HOME", "/home/u")
	t.Setenv("XDG_CONFIG_HOME", "/xdg")

	folders := ConfigFolders()

	if folders[0] != filepath.Join("/xdg", "local-mcp") ||
		folders[1] != filepath.Join("/home/u", ".config", "local-mcp") ||
		folders[len(folders)-1] != filepath.Join("/etc", "local-mcp") {
		t.Errorf("folders = %v", folders)
	}
}
