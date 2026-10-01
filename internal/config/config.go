// Package config loads the configuration of the local MCP server.
//
// Values come from chu: defaults, then the first local-mcp.{toml,yaml,yml,json}
// found in the working directory, ~/.config/local-mcp, the OS user config
// directory or /etc/local-mcp (or the file named by CONFIG_FILE), then LOCAL_MCP_*
// environment variables.
package config

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"slices"

	mcors "github.com/rakunlabs/ada/middleware/cors"
	"github.com/rakunlabs/chu"
	"github.com/rakunlabs/chu/loader/loaderenv"
	"github.com/rakunlabs/chu/loader/loaderfile"
	"github.com/rakunlabs/logi"
)

const ServiceName = "local-mcp"

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

	// CORS is ada's CORS configuration. Keys the config omits keep the
	// values from DefaultCORS.
	CORS mcors.Cors `cfg:"cors"`
}

// DefaultCORS lets browser-based MCP clients reach the server. It only
// matters to browsers; command-line clients send no Origin header.
func DefaultCORS() mcors.Cors {
	return mcors.Cors{
		AllowOrigins: []string{"*"},
		// The streamable HTTP transport posts requests, opens the event
		// stream with GET and ends the session with DELETE.
		AllowMethods: []string{
			http.MethodGet,
			http.MethodPost,
			http.MethodDelete,
			http.MethodOptions,
		},
		AllowHeaders: []string{
			"content-type",
			"accept",
			"authorization",
			"cache-control",
			"last-event-id",
			"mcp-session-id",
			"mcp-protocol-version",
		},
		// A page must read the session id from the initialize response to
		// make a second call.
		ExposeHeaders: []string{"Mcp-Session-Id"},
		// Answers Chrome's Private Network Access preflight, which a page on
		// a public address must pass to reach a loopback server.
		AllowPrivateNetwork: true,
		MaxAge:              600,
	}
}

// ConfigFolders are searched in order, after the working directory, for
// local-mcp.{toml,yaml,yml,json}. /etc is also checked after /etc/local-mcp.
func ConfigFolders() []string {
	var folders []string

	add := func(dir string) {
		if dir != "" && !slices.Contains(folders, dir) {
			folders = append(folders, dir)
		}
	}

	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		add(filepath.Join(xdg, "local-mcp"))
	}

	if home, err := os.UserHomeDir(); err == nil {
		add(filepath.Join(home, ".config", "local-mcp"))
	}

	// ~/Library/Application Support on macOS, %AppData% on Windows.
	if dir, err := os.UserConfigDir(); err == nil {
		add(filepath.Join(dir, "local-mcp"))
	}

	add(filepath.Join("/etc", "local-mcp"))
	add("/etc")

	return folders
}

func Load(ctx context.Context) (*Config, error) {
	var cfg Config

	// Seeded before loading: chu merges over the struct, so keys the config
	// omits keep their defaults.
	cfg.HTTP.CORS = DefaultCORS()

	if err := chu.Load(ctx, ServiceName, &cfg,
		chu.WithLoaderOption(loaderfile.New(loaderfile.WithFolders(ConfigFolders()...))),
		chu.WithLoaderOption(loaderenv.New(loaderenv.WithPrefix("LOCAL_MCP_"))),
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
