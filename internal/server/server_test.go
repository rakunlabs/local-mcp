package server

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/rakunlabs/local-mcp/internal/config"
)

func startHTTP(t *testing.T, mutate func(*config.Config)) string {
	t.Helper()

	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}

	addr := l.Addr().String()
	_ = l.Close()

	cfg := &config.Config{
		Root:   t.TempDir(),
		Search: config.Search{Engine: "go"},
		Shell:  config.Shell{DefaultTimeoutMS: 10_000},
		Output: config.Output{Dir: t.TempDir(), MaxLines: 2000, MaxBytes: 51200},
		HTTP:   config.HTTP{Address: addr, Path: "/mcp", CORS: config.DefaultCORS()},
	}
	if mutate != nil {
		mutate(cfg)
	}

	ctx, cancel := context.WithCancel(context.Background())

	srv, err := New(ctx, cfg, "test")
	if err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})

	go func() {
		defer close(done)

		_ = srv.RunHTTP(ctx)
	}()

	t.Cleanup(func() {
		cancel()
		<-done
		srv.Close()
	})

	base := "http://" + addr
	for range 50 {
		if resp, err := http.Get(base + "/healthz"); err == nil {
			_ = resp.Body.Close()

			return base
		}

		time.Sleep(20 * time.Millisecond)
	}

	t.Fatal("server did not start")

	return ""
}

const initialize = `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`

func post(t *testing.T, url, origin, token string) *http.Response {
	t.Helper()

	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(initialize))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")

	if origin != "" {
		req.Header.Set("Origin", origin)
	}

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() { _ = resp.Body.Close() })

	return resp
}

func TestCORSPreflightBeforeAuth(t *testing.T) {
	base := startHTTP(t, func(c *config.Config) { c.HTTP.Token = "secret" })

	req, _ := http.NewRequest(http.MethodOptions, base+"/mcp", nil)
	req.Header.Set("Origin", "https://app.example.com")
	req.Header.Set("Access-Control-Request-Method", "POST")
	req.Header.Set("Access-Control-Request-Headers", "content-type,authorization,mcp-session-id")
	req.Header.Set("Access-Control-Request-Private-Network", "true")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status %d", resp.StatusCode)
	}

	for header, want := range map[string]string{
		"Access-Control-Allow-Origin":          "*",
		"Access-Control-Allow-Private-Network": "true",
		"Access-Control-Max-Age":               "600",
	} {
		if got := resp.Header.Get(header); got != want {
			t.Errorf("%s = %q, want %q", header, got, want)
		}
	}

	if got := resp.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(got, "mcp-session-id") {
		t.Errorf("allow headers %q", got)
	}

	if resp := post(t, base+"/mcp", "https://app.example.com", ""); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no token: status %d", resp.StatusCode)
	}

	resp = post(t, base+"/mcp", "https://app.example.com", "secret")
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}

	if got := resp.Header.Get("Access-Control-Expose-Headers"); got != "Mcp-Session-Id" {
		t.Errorf("expose headers %q", got)
	}

	if resp.Header.Get("Mcp-Session-Id") == "" {
		t.Error("missing session id")
	}
}

func TestCORSRestrictedOrigin(t *testing.T) {
	base := startHTTP(t, func(c *config.Config) {
		c.HTTP.CORS.AllowOrigins = []string{"https://*.example.com"}
	})

	if got := post(t, base+"/mcp", "https://app.example.com", "").Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("allowed origin got %q", got)
	}

	if got := post(t, base+"/mcp", "https://evil.test", "").Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("denied origin got %q", got)
	}
}
