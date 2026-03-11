# @telara-cli/cli

The official CLI for [Telara](https://telara.dev) — manage MCP configurations, API keys, and agent tool integrations from your terminal.

## Installation

```bash
npm install -g @telara-cli/cli
```

### Alternative methods

```bash
# macOS / Linux
curl -fsSL https://get.telara.dev/install.sh | sh

# Windows (PowerShell)
irm https://get.telara.dev/windows | iex

# Homebrew
brew install Telara-Labs/tap/telara
```

## Quick start

```bash
# 1. Authenticate
telara login

# 2. Set up your agent tools (auto-detects installed tools)
telara setup

# 3. Verify everything is working
telara doctor
```

## Commands

### Authentication

| Command | Description |
|---------|-------------|
| `telara login` | Authenticate via browser (device flow) or `--token` |
| `telara logout` | Revoke token and remove local credentials |
| `telara whoami` | Show current user, org, and active context |

### MCP configuration management

| Command | Description |
|---------|-------------|
| `telara config list` | List accessible MCP configurations |
| `telara config show <name>` | Show config details, data sources, and MCP URL |
| `telara config keys <name>` | List API keys for a configuration |
| `telara config generate-key <name>` | Generate a new API key (`--expires 30d\|90d\|1yr\|never`) |
| `telara config revoke-key <id>` | Revoke an API key |
| `telara config rotate-key <ctx>` | Rotate the key for a saved context |

### Agent tool setup

| Command | Description |
|---------|-------------|
| `telara setup` | Interactive setup — auto-detects installed tools |
| `telara setup claude-code` | Configure Claude Code (`--managed` for enterprise) |
| `telara setup cursor` | Configure Cursor |
| `telara setup windsurf` | Configure Windsurf |
| `telara setup vscode` | Configure VS Code (project scope) |
| `telara setup all` | Configure all detected tools |
| `telara init` | Write project-scoped MCP config for detected tools |

### Contexts

Contexts are named (config + API key) pairs for easy switching between environments.

| Command | Description |
|---------|-------------|
| `telara context list` | List saved contexts |
| `telara context create <name>` | Create a new context with a generated API key |
| `telara context use <name>` | Switch active context |
| `telara context current` | Show active context details and MCP URL |
| `telara context delete <name>` | Delete a saved context |

### Provisioning

Generate MCP access keys for specific deployment scenarios.

| Command | Description |
|---------|-------------|
| `telara provision claude-web` | Generate key for Claude.ai (Anthropic Organization Connector) |
| `telara provision ci` | Generate key for CI/CD environments (GitHub Actions, GitLab CI) |
| `telara provision managed` | Generate managed config for enterprise MDM/GPO deployment |

### Diagnostics

| Command | Description |
|---------|-------------|
| `telara doctor` | Check auth, connectivity, tool configs, contexts, and security |
| `telara version` | Print version, commit, and build date |
| `telara update` | Update CLI to the latest version |

## Global flags

| Flag | Description |
|------|-------------|
| `--api-url` | Override the Telara API base URL |
| `--context` | Use a specific context for this command |
| `-v, --verbose` | Print full HTTP responses on errors |

## Supported agent tools

- **Claude Code** (global or managed scope)
- **Cursor** (global scope)
- **Windsurf** (global scope)
- **VS Code** (project scope)

## Requirements

- Node.js >= 14 (for npm installation)
- macOS, Linux, or Windows
- x86_64 or ARM64 architecture

## Documentation

- [Telara docs](https://telara.dev/docs)
- [CLI installation guide](https://telara.dev/docs/cli/install)

## License

MIT
