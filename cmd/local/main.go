// Command local is an MCP server that gives an agent OpenCode-style file and
// shell tools over a local workspace. It speaks stdio by default and
// streamable HTTP with --server.
package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/rakunlabs/into"
	"github.com/rakunlabs/logi"

	"github.com/rakunlabs/local-mcp/internal/config"
	"github.com/rakunlabs/local-mcp/internal/server"
)

var (
	version = "v0.0.0"
	commit  = "-"
	date    = "-"
)

type options struct {
	server bool
	root   string
}

func main() {
	var opts options

	flag.BoolVar(&opts.server, "server", false, "serve over streamable HTTP instead of stdio")
	flag.StringVar(&opts.root, "root", "", "workspace root (overrides config; defaults to the current directory)")
	flag.Parse()

	into.Init(func(ctx context.Context) error {
		return run(ctx, opts)
	},
		// logi writes to stderr, which keeps stdout free for the stdio transport.
		into.WithLogger(logi.InitializeLog(logi.WithCaller(false))),
		into.WithMsgf("%s version:[%s] commit:[%s] date:[%s]", config.ServiceName, version, commit, date),
	)
}

func run(ctx context.Context, opts options) error {
	cfg, err := config.Load(ctx)
	if err != nil {
		return err
	}

	if opts.root != "" {
		cfg.Root = opts.root
	}

	srv, err := server.New(ctx, cfg, version)
	if err != nil {
		return fmt.Errorf("init server; %w", err)
	}
	defer srv.Close()

	if opts.server {
		return srv.RunHTTP(ctx)
	}

	return srv.RunStdio(ctx)
}
