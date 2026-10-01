// Package server builds the MCP server and serves it over stdio or HTTP.
package server

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/rakunlabs/ada"
	mcors "github.com/rakunlabs/ada/middleware/cors"
	mlog "github.com/rakunlabs/ada/middleware/log"
	mrecover "github.com/rakunlabs/ada/middleware/recover"
	mrequestid "github.com/rakunlabs/ada/middleware/requestid"
	mserver "github.com/rakunlabs/ada/middleware/server"

	"github.com/rakunlabs/local-mcp/internal/config"
	"github.com/rakunlabs/local-mcp/internal/output"
	"github.com/rakunlabs/local-mcp/internal/search"
	"github.com/rakunlabs/local-mcp/internal/tools"
	"github.com/rakunlabs/local-mcp/internal/workspace"
)

type Server struct {
	cfg     *config.Config
	version string
	mcp     *mcp.Server
	jobs    *tools.Jobs
}

func New(ctx context.Context, cfg *config.Config, version string) (*Server, error) {
	files, err := workspace.New(cfg.Root, cfg.AllowedDirs...)
	if err != nil {
		return nil, fmt.Errorf("init workspace; %w", err)
	}

	store, err := output.New(cfg.Output.Dir, cfg.Output.MaxLines, cfg.Output.MaxBytes)
	if err != nil {
		return nil, fmt.Errorf("init output store; %w", err)
	}

	readable, err := workspace.New(files.Root(), append(files.Allowed(), store.Dir())...)
	if err != nil {
		return nil, fmt.Errorf("init readable workspace; %w", err)
	}

	engine, err := search.New(cfg.Search.Engine, cfg.Search.Ripgrep)
	if err != nil {
		return nil, fmt.Errorf("init search; %w", err)
	}

	jobs := tools.NewJobs(ctx, store)

	srv := mcp.NewServer(&mcp.Implementation{Name: config.ServiceName, Version: version}, &mcp.ServerOptions{
		Instructions: fmt.Sprintf("Local workspace tools. Workspace root: %s. Relative paths resolve from it.", files.Root()),
	})

	enabled, err := tools.Register(srv, &tools.Deps{
		Files:    files,
		Readable: readable,
		Output:   store,
		Search:   engine,
		Jobs:     jobs,
		Config:   cfg,
	})
	if err != nil {
		return nil, err
	}

	slog.Info("tools ready",
		"root", files.Root(),
		"allowed_dirs", files.Allowed(),
		"search", engine.Name(),
		"tools", enabled,
	)

	return &Server{cfg: cfg, version: version, mcp: srv, jobs: jobs}, nil
}

// Close stops background jobs.
func (s *Server) Close() {
	s.jobs.Close()
}

func (s *Server) RunStdio(ctx context.Context) error {
	err := s.mcp.Run(ctx, &mcp.StdioTransport{})
	if err == nil || errors.Is(err, io.EOF) || err.Error() == "server is closing: EOF" {
		return nil
	}

	if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
		return nil
	}

	return fmt.Errorf("serve stdio; %w", err)
}

func (s *Server) RunHTTP(ctx context.Context) error {
	handler := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return s.mcp }, nil)

	server := ada.New()
	server.Use(
		mrecover.Middleware(),
		mserver.Middleware(config.ServiceName+"/"+s.version),
		mcors.Middleware(mcors.WithConfig(s.cfg.HTTP.CORS)),
		mrequestid.Middleware(),
		mlog.Middleware(),
	)

	server.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	})

	server.Handle(s.cfg.HTTP.Path, s.auth(handler))

	if s.cfg.HTTP.Token == "" {
		slog.Warn("http token is not set; anyone who can reach the address can run commands", "address", s.cfg.HTTP.Address)

		if slices.Contains(s.cfg.HTTP.CORS.AllowOrigins, "*") || len(s.cfg.HTTP.CORS.AllowOrigins) == 0 {
			slog.Warn("cors allows every origin and no token is set; any web page opened in a browser on this machine can call the tools, including shell")
		}
	}

	slog.Info("serving MCP over HTTP", "address", s.cfg.HTTP.Address, "path", s.cfg.HTTP.Path)

	return server.StartWithContext(ctx, s.cfg.HTTP.Address)
}

func (s *Server) auth(next http.Handler) http.Handler {
	if s.cfg.HTTP.Token == "" {
		return next
	}

	want := []byte("Bearer " + s.cfg.HTTP.Token)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			http.Error(w, "unauthorized", http.StatusUnauthorized)

			return
		}

		next.ServeHTTP(w, r)
	})
}
