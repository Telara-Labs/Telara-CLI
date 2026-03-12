# Telara CLI

The official command-line interface for [Telara](https://telara.dev). Manage MCP configurations, generate API keys, and set up agent tools like Claude Code, Cursor, Windsurf, and VS Code — all from your terminal.

## Installation

### npm (recommended)

```bash
npm install -g @telara-cli/cli
```

### Shell script

```bash
# macOS / Linux
curl -fsSL https://get.telara.dev/install.sh | sh

# Windows (PowerShell)
irm https://get.telara.dev/windows | iex
```

### Homebrew

```bash
brew install Telara-Labs/tap/telara
```

### GitHub Releases

Download pre-built binaries for your platform from the [releases page](https://github.com/Telara-Labs/Telara-CLI/releases).

| Platform | Architectures |
|----------|--------------|
| macOS | x86_64, ARM64 (Apple Silicon) |
| Linux | x86_64, ARM64 |
| Windows | x86_64, ARM64 |

## Quick start

```bash
# 1. Generate a token at https://app.telara.dev/settings?tab=developer

# 2. Authenticate
telara login --token <your-token>
#   or use browser-based device flow:
telara login

# 3. Set up your agent tools (auto-detects installed tools)
telara setup

# 4. Verify everything is working
telara doctor
```

## What it does

Telara CLI connects your agentic coding tools to your organization's MCP (Model Context Protocol) configurations. It handles:

- **Authentication** — device flow (browser) or token-based login
- **MCP configuration management** — list, inspect, and manage API keys for your MCP configs
- **Agent tool setup** — auto-detect and configure Claude Code, Cursor, Windsurf, and VS Code
- **Context management** — switch between environments with named (config + key) pairs
- **Provisioning** — generate keys for Claude.ai web, CI/CD pipelines, and enterprise MDM deployment
- **Diagnostics** — `telara doctor` checks auth, connectivity, tool configs, and security

## Commands

```
telara login                       Authenticate with Telara
telara logout                      Revoke token and remove credentials
telara whoami                      Show current user and org

telara config list                 List MCP configurations
telara config show <name>          Show config details and MCP URL
telara config keys <name>          List API keys
telara config generate-key <name>  Generate a new API key
telara config revoke-key <id>      Revoke an API key
telara config rotate-key <ctx>     Rotate key for a saved context

telara setup                       Interactive agent tool setup
telara setup claude-code           Configure Claude Code
telara setup cursor                Configure Cursor
telara setup windsurf              Configure Windsurf
telara setup vscode                Configure VS Code
telara setup all                   Configure all detected tools
telara init                        Write project-scoped MCP config

telara context list                List saved contexts
telara context create <name>       Create context with generated key
telara context use <name>          Switch active context
telara context current             Show active context and MCP URL
telara context delete <name>       Delete a saved context

telara provision claude-web        Key for Claude.ai Organization Connector
telara provision ci                Key for CI/CD environments
telara provision managed           Config for enterprise MDM/GPO deployment

telara doctor                      Check environment and configuration
telara version                     Print version info
telara update                      Update to latest version
```

## Global flags

```
--api-url    Override the Telara API base URL
--context    Use a specific context for this command
-v           Print full HTTP responses on errors
```

## Supported tools

| Tool | Global setup | Project setup |
|------|-------------|---------------|
| Claude Code | `telara setup claude-code` | `telara init --tool claude-code` |
| Cursor | `telara setup cursor` | `telara init --tool cursor` |
| Windsurf | `telara setup windsurf` | `telara init --tool windsurf` |
| VS Code | project only | `telara setup vscode` / `telara init --tool vscode` |

## System requirements

- macOS, Linux, or Windows
- x86_64 or ARM64 architecture
- Node.js >= 14 (for npm installation method only)

## Development

```bash
cd services/cli
go mod tidy
go build ./cmd/server
./server version
```

### Running tests

```bash
cd services/cli
go test ./... -v -timeout 60s
```

## Documentation

- [Telara docs](https://telara.dev/docs)
- [CLI installation guide](https://docs.telara.dev/mcp-clients/cli)

## License

MIT
