// Package tools implements the MCP tools of the local server, adapted from
// OpenCode's built-in file and command tools.
package tools

import (
	"fmt"
	"slices"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/rakunlabs/local-mcp/internal/config"
	"github.com/rakunlabs/local-mcp/internal/output"
	"github.com/rakunlabs/local-mcp/internal/search"
	"github.com/rakunlabs/local-mcp/internal/workspace"
)

// Deps is everything a tool may use.
type Deps struct {
	// Files bounds every path a tool reads or writes.
	Files *workspace.Workspace
	// Readable is Files plus the output store, so saved outputs can be read
	// and searched but not edited by path.
	Readable *workspace.Workspace
	Output   *output.Store
	Search   search.Engine
	Jobs     *Jobs
	Config   *config.Config
}

type tool struct {
	name     string
	readOnly bool
	register func(*mcp.Server, *Deps)
}

var all = []tool{
	{name: "read", readOnly: true, register: registerRead},
	{name: "glob", readOnly: true, register: registerGlob},
	{name: "grep", readOnly: true, register: registerGrep},
	{name: "edit", register: registerEdit},
	{name: "write", register: registerWrite},
	{name: "patch", register: registerPatch},
	{name: "shell", register: registerShell},
	{name: "shell_job", register: registerShellJob},
}

// Names lists every tool the server has.
func Names() []string {
	names := make([]string, 0, len(all))
	for _, t := range all {
		names = append(names, t.name)
	}

	return names
}

// Register adds the tools allowed by the configuration and returns their names.
func Register(server *mcp.Server, deps *Deps) ([]string, error) {
	known := Names()
	for _, name := range deps.Config.DisabledTools {
		if !slices.Contains(known, name) {
			return nil, fmt.Errorf("disabled_tools: unknown tool %q; known tools: %v", name, known)
		}
	}

	var enabled []string

	for _, t := range all {
		if deps.Config.ReadOnly && !t.readOnly {
			continue
		}

		if slices.Contains(deps.Config.DisabledTools, t.name) {
			continue
		}

		// A background job manager is useless without the tool that starts jobs.
		if t.name == "shell_job" && !slices.Contains(enabled, "shell") {
			continue
		}

		t.register(server, deps)
		enabled = append(enabled, t.name)
	}

	sort.Strings(enabled)

	return enabled, nil
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func ptr[T any](v T) *T { return &v }

var (
	readOnlyAnnotations = &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: ptr(false)}
	editAnnotations     = &mcp.ToolAnnotations{DestructiveHint: ptr(true), OpenWorldHint: ptr(false)}
)
