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
	t.Chdir(t.TempDir())

	cfg, err := Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(cfg.HTTP.CORS.AllowOrigins, []string{"*"}) || !cfg.HTTP.CORS.AllowPrivateNetwork {
		t.Errorf("default cors = %+v", cfg.HTTP.CORS)
	}
}
