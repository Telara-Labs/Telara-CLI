<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/logo-dark.png">
  <img alt="Telara" src="docs/logo-light.png" height="52">
</picture>

# Telara CLI

The official command-line interface for [Telara](https://telara.dev). Connect your AI coding tools to your organization's MCP configurations — manage access, generate keys, and configure Claude Code, Cursor, Windsurf, and VS Code in seconds.

[![npm](https://img.shields.io/npm/v/@telara-cli/cli?label=npm&color=7c3aed)](https://www.npmjs.com/package/@telara-cli/cli)
[![GitHub release](https://img.shields.io/github/v/release/Telara-Labs/Telara-CLI?label=release&color=7c3aed)](https://github.com/Telara-Labs/Telara-CLI/releases)
[![License: MIT](https://img.shields.io/badge/License-MIT-7c3aed.svg)](LICENSE)

---

## Installation

### npm (recommended)

```bash
npm install -g @telara-cli/cli
```

### Homebrew

```bash
brew install Telara-Labs/tap/telara
```

### Shell script

```bash
# macOS / Linux
curl -fsSL https://get.telara.dev/install.sh | sh

# Windows (PowerShell)
irm https://get.telara.dev/windows | iex
```

### GitHub Releases

Download pre-built binaries from the [releases page](https://github.com/Telara-Labs/Telara-CLI/releases).

| Platform | Architectures |
|----------|--------------|
| macOS | x86_64, ARM64 (Apple Silicon) |
| Linux | x86_64, ARM64 |
| Windows | x86_64, ARM64 |

---

## Quick start

```bash
# 1. Log in (browser-based device flow)
telara login

#    — or use a token directly:
telara login --token <tlrc_...>

# 2. Set up your agent tools (auto-detects Claude Code, Cursor, Windsurf, VS Code)
telara setup

# 3. Verify everything is working
telara doctor
```

---

## Commands

### Authentication

| Command | Description |
|---------|-------------|
| `telara login` | Authenticate via browser (device flow) or `--token <tlrc_...>` |
| `telara logout` | Revoke token, snapshot MCP configs, and remove local credentials |
| `telara whoami` | Show current user, org, token prefix, and active context |

### MCP configuration management

| Command | Description |
|---------|-------------|
| `telara config list` | List MCP configurations accessible to you |
| `telara config show <name>` | Show data sources, policies, key count, and MCP URL |
| `telara config keys <name>` | List active API keys with scope and expiry |
| `telara config rotate-key <context-name>` | Generate a replacement key and auto-revoke the old one |

### Agent tool setup

| Command | Description |
|---------|-------------|
| `telara setup` | Interactive setup — auto-detects and configures all installed tools |
| `telara setup claude-code` | Configure Claude Code (`--managed` for enterprise MDM) |
| `telara setup cursor` | Configure Cursor |
| `telara setup windsurf` | Configure Windsurf |
| `telara setup vscode` | Configure VS Code |
| `telara setup all` | Configure all detected tools at once |
| `telara init` | Write a project-scoped MCP config for the current directory |

### Contexts

A context is a named (config + API key) pair. Switch between them to change which integrations and tools your AI assistant can reach.

| Command | Description |
|---------|-------------|
| `telara context list` | List saved contexts, with active marker |
| `telara context create <name>` | Create a context — prompts for config, generates a scoped key |
| `telara context use <name>` | Switch the active context |
| `telara context current` | Show active context details and MCP URL |
| `telara context delete <name>` | Remove a saved context (warns if the key is still active) |

### Provisioning

Generate MCP access keys for specific deployment scenarios.

| Command | Description |
|---------|-------------|
| `telara provision claude-web` | Key for Claude.ai (Anthropic Organization Connector) |
| `telara provision ci` | Key for CI/CD environments (GitHub Actions, GitLab CI, etc.) |
| `telara provision managed` | Config for enterprise MDM / GPO deployment |

### Diagnostics & utilities

| Command | Description |
|---------|-------------|
| `telara doctor` | Check connectivity, auth, tool configs, contexts, and key health |
| `telara version` | Print version, commit hash, and build date |
| `telara update` | Self-update to the latest release |

---

## Global flags

| Flag | Description |
|------|-------------|
| `--api-url <url>` | Override the Telara API base URL |
| `--context <name>` | Use a specific context for this command |
| `-v, --verbose` | Print full HTTP responses on errors |

---

## Supported agent tools

| Tool | Global setup | Project setup (`init`) |
|------|-------------|----------------------|
| Claude Code | `telara setup claude-code` | `telara init --tool claude-code` |
| Cursor | `telara setup cursor` | `telara init --tool cursor` |
| Windsurf | `telara setup windsurf` | `telara init --tool windsurf` |
| VS Code | `telara setup vscode` | `telara init --tool vscode` |

---

## System requirements

- macOS, Linux, or Windows (x86_64 or ARM64)
- Node.js >= 14 (for npm installation only)

---

## Development

```bash
cd services/cli
go mod tidy
go build ./cmd/server
./server version
```

### Tests

```bash
cd services/cli
go test ./... -v -timeout 60s
```

---

## Documentation

- [Telara docs](https://telara.dev/docs)
- [CLI installation guide](https://docs.telara.dev/mcp-clients/cli)

## License

MIT
