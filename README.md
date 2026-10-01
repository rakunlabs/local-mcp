# local-mcp

[![License](https://img.shields.io/github/license/rakunlabs/local-mcp?color=blue&style=flat-square)](https://raw.githubusercontent.com/rakunlabs/local-mcp/main/LICENSE)
[![Coverage](https://img.shields.io/sonar/coverage/rakunlabs_local-mcp?logo=sonarcloud&server=https%3A%2F%2Fsonarcloud.io&style=flat-square)](https://sonarcloud.io/summary/overall?id=rakunlabs_local-mcp)

`local-mcp` is an MCP server that gives an agent file and shell tools over a local
workspace. The tools are adapted from [OpenCode's built-in tools](https://opencode.ai/v2/docs/tools/).

| Tool        | What it does                                                                                                                         |
|-------------|--------------------------------------------------------------------------------------------------------------------------------------|
| `read`      | Read a text file with numbered lines (paged by `offset`/`limit`, 2000 lines / 50 KiB per page), an image or PDF, or list a directory |
| `glob`      | Find files by glob pattern, honouring `.gitignore`                                                                                   |
| `grep`      | Search file contents by regex or literal text, with `include`, `caseSensitive` and `limit`                                           |
| `edit`      | Replace exact text; must be unique unless `replaceAll`. Keeps line endings and BOM                                                   |
| `write`     | Create or overwrite a file, creating parent directories                                                                              |
| `patch`     | Apply a `*** Begin Patch` patch that adds, updates, moves and deletes files. Every operation is validated before anything is written |
| `shell`     | Run a command with `workdir` and `timeout`; `background: true` starts a job                                                          |
| `shell_job` | `list`, `status`, `wait` or `kill` background jobs                                                                                   |

`glob` and `grep` use ripgrep when it is installed and a built-in Go engine
otherwise. Large outputs are cut to a preview and the full text is saved to a
file that `read` and `grep` can open.

## Install

Download the binary for your platform from the
[latest release](https://github.com/rakunlabs/local-mcp/releases/latest):

```sh
OS=$(uname -s | tr '[:upper:]' '[:lower:]')
ARCH=$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/')
curl -fsSL "https://github.com/rakunlabs/local-mcp/releases/latest/download/local-mcp_${OS}_${ARCH}.tar.gz" | tar -xz -C ~/bin local-mcp
```

Make sure `~/bin` exists (`mkdir -p ~/bin`) and is on your `PATH`.

Archives are `local-mcp_<os>_<arch>.tar.gz` for linux and darwin and
`local-mcp_windows_<arch>.zip` for windows, on amd64 and arm64. Each release has a
`checksums.txt`.

On macOS, a binary downloaded with a browser may be quarantined; clear it with
`xattr -d com.apple.quarantine local-mcp`.

## Use

stdio (default), e.g. in `opencode.json`:

```json
{
  "mcp": {
    "local-mcp": { "type": "local", "command": ["local-mcp", "--root", "/path/to/project"] }
  }
}
```

Streamable HTTP:

```sh
LOCAL_MCP_HTTP_TOKEN=secret local-mcp --server   # http://127.0.0.1:8080/mcp
```

CORS is on by default so browser-based MCP clients can connect: every origin
is allowed, the MCP headers are accepted, `Mcp-Session-Id` is exposed and
Chrome's Private Network Access preflight is answered. Change it under
`http.cors`; keys you omit keep their defaults.

## Security

Paths are confined to the workspace root and `allowed_dirs`; symlinks that
point outside are rejected. `shell`, however, runs with the full authority of
the host user. Use `read_only: true` or `disabled_tools` to limit what an
endpoint can do, and set `http.token` when serving over HTTP.

With the default `allow_origins: ["*"]` and no token, any web page opened in a
browser on the same machine can call the tools, including `shell`. Set
`http.token`, narrow `http.cors.allow_origins`, or both.

## Configuration

See [`local-mcp.example.yaml`](local-mcp.example.yaml). Config is loaded with
[chu](https://github.com/rakunlabs/chu). The first
`local-mcp.{toml,yaml,yml,json}` found is used, searched in this order:

1. the working directory
2. `$XDG_CONFIG_HOME/local-mcp/`, if set
3. `~/.config/local-mcp/`
4. the OS user config directory (`~/Library/Application Support/local-mcp/`
   on macOS, `%AppData%\local-mcp\` on Windows)
5. `/etc/local-mcp/`
6. `/etc/`

`CONFIG_FILE=/path/to/config.yaml` skips the search. `LOCAL_MCP_*` environment
variables override the file, e.g. `LOCAL_MCP_READ_ONLY=true`.
