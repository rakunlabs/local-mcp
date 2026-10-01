// Package config loads the configuration of the local MCP server.
//
// Values come from chu: defaults, then local.{toml,yaml,yml,json} (or the file
// named by CONFIG_FILE), then LOCAL_* environment variables.
package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/rakunlabs/chu"
	"github.com/rakunlabs/chu/loader/loaderenv"
	"github.com/rakunlabs/logi"
)

const ServiceName = "local"

type Config struct {
	LogLevel string `cfg:"log_level" default:"info"`

	// Root is the workspace directory. Relative tool paths resolve from it.
	// Defaults to the current working directory.
	Root string `cfg:"root"`

	// AllowedDirs are extra directories, outside Root, that tools may access.
	AllowedDirs []string `cfg:"allowed_dirs"`

	// ReadOnly removes every tool that can change the filesystem or run commands.
	ReadOnly bool `cfg:"read_only"`

	// DisabledTools removes tools by name, e.g. ["shell", "patch"].
	DisabledTools []string `cfg:"disabled_tools"`

	Search Search `cfg:"search"`
	Shell  Shell  `cfg:"shell"`
	Output Output `cfg:"output"`
	HTTP   HTTP   `cfg:"http"`
}

type Search struct {
	// Engine is "auto" (ripgrep when found, else the built-in Go engine),
	// "rg" or "go".
	Engine string `cfg:"engine" default:"auto"`
	// Ripgrep is the rg binary; looked up in PATH when empty.
	Ripgrep string `cfg:"ripgrep"`
}

type Shell struct {
	// Path of the shell; defaults to $SHELL, then /bin/sh (cmd.exe on Windows).
	Path string `cfg:"path"`
	// DefaultTimeoutMS applies to foreground commands without a timeout.
	DefaultTimeoutMS int `cfg:"default_timeout_ms" default:"120000"`
	// MaxTimeoutMS caps requested timeouts; 0 means no cap.
	MaxTimeoutMS int `cfg:"max_timeout_ms" default:"0"`
}

type Output struct {
	// Dir keeps full outputs that were too large to return. It is always
	// readable by read and grep. Defaults to <tmp>/local-mcp/tool-output.
	Dir      string `cfg:"dir"`
	MaxLines int    `cfg:"max_lines" default:"2000"`
	MaxBytes int    `cfg:"max_bytes" default:"51200"`
}

type HTTP struct {
	// Address is only used with --server. Bound to loopback by default
	// because the shell tool runs with the host user's authority.
	Address string `cfg:"address" default:"127.0.0.1:8080"`
	Path    string `cfg:"path" default:"/mcp"`
	// Token, when set, is required as "Authorization: Bearer <token>".
	Token string `cfg:"token" log:"false"`
}

func Load(ctx context.Context) (*Config, error) {
	var cfg Config
	if err := chu.Load(ctx, ServiceName, &cfg,
		chu.WithLoaderOption(loaderenv.New(loaderenv.WithPrefix("LOCAL_"))),
	); err != nil {
		return nil, fmt.Errorf("load config; %w", err)
	}

	if err := logi.SetLogLevel(cfg.LogLevel); err != nil {
		return nil, fmt.Errorf("set log level %s; %w", cfg.LogLevel, err)
	}

	if cfg.Output.Dir == "" {
		cfg.Output.Dir = filepath.Join(os.TempDir(), "local-mcp", "tool-output")
	}

	switch cfg.Search.Engine {
	case "auto", "rg", "go":
	default:
		return nil, fmt.Errorf("search.engine must be auto, rg or go; got %q", cfg.Search.Engine)
	}

	slog.Debug("loaded configuration", "config", chu.MarshalMap(cfg))

	return &cfg, nil
}
