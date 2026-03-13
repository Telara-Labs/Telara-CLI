<img alt="Telara" src="https://raw.githubusercontent.com/Telera-Labs/Telara-CLI/main/docs/logo-light.png" height="52">

# @telara-cli/cli

The official CLI for [Telara](https://telara.dev) — connect your AI coding tools to your organization's MCP configurations. Manage access, generate keys, and configure Claude Code, Cursor, Windsurf, and VS Code from your terminal.

[![npm](https://img.shields.io/npm/v/@telara-cli/cli?color=7c3aed)](https://www.npmjs.com/package/@telara-cli/cli)
[![License: MIT](https://img.shields.io/badge/License-MIT-7c3aed.svg)](https://github.com/Telara-Labs/Telara-CLI/blob/main/LICENSE)

---

## Installation

```bash
npm install -g @telara-cli/cli
```

**Other methods:**

```bash
# Homebrew
brew install Telara-Labs/tap/telara

# macOS / Linux
curl -fsSL https://get.telara.dev/install.sh | sh

# Windows (PowerShell)
irm https://get.telara.dev/windows | iex
```

---

## Quick start

```bash
# Log in (browser-based device flow)
telara login

# Set up your agent tools
telara setup

# Verify everything is working
telara doctor
```

---

## Commands

### Authentication

| Command | Description |
|---------|-------------|
| `telara login` | Authenticate via browser (device flow) or `--token <tlrc_...>` |
| `telara logout` | Revoke token and remove local credentials |
| `telara whoami` | Show current user, org, and active context |

### MCP configuration management

| Command | Description |
|---------|-------------|
| `telara config list` | List MCP configurations accessible to you |
| `telara config show <name>` | Show data sources, policies, key count, and MCP URL |
| `telara config keys <name>` | List active API keys with scope and expiry |
| `telara config generate-key <name>` | Generate a new API key (`--expires 30d\|90d\|1yr\|never`) |
| `telara config revoke-key <key-id> --config <id>` | Revoke an API key immediately |
| `telara config rotate-key <context-name>` | Generate a replacement key and auto-revoke the old one |

### Agent tool setup

| Command | Description |
|---------|-------------|
| `telara setup` | Interactive setup — auto-detects all installed tools |
| `telara setup claude-code` | Configure Claude Code (`--managed` for enterprise MDM) |
| `telara setup cursor` | Configure Cursor |
| `telara setup windsurf` | Configure Windsurf |
| `telara setup vscode` | Configure VS Code |
| `telara setup all` | Configure all detected tools at once |
| `telara init` | Write a project-scoped MCP config for the current directory |

### Contexts

| Command | Description |
|---------|-------------|
| `telara context list` | List saved contexts |
| `telara context create <name>` | Create a context with a generated scoped API key |
| `telara context use <name>` | Switch the active context |
| `telara context current` | Show active context details and MCP URL |
| `telara context delete <name>` | Remove a saved context |

### Provisioning

| Command | Description |
|---------|-------------|
| `telara provision claude-web` | Key for Claude.ai (Anthropic Organization Connector) |
| `telara provision ci` | Key for CI/CD environments |
| `telara provision managed` | Config for enterprise MDM / GPO deployment |

### Diagnostics

| Command | Description |
|---------|-------------|
| `telara doctor` | Check connectivity, auth, tool configs, and key health |
| `telara version` | Print version, commit, and build date |
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

- **Claude Code** — global and managed (enterprise MDM) scope
- **Cursor** — global scope
- **Windsurf** — global scope
- **VS Code** — project scope

---

## Requirements

- Node.js >= 14
- macOS, Linux, or Windows (x86_64 or ARM64)

---

## Documentation

- [Telara docs](https://telara.dev/docs)
- [CLI guide](https://docs.telara.dev/mcp-clients/cli)
- [GitHub](https://github.com/Telara-Labs/Telara-CLI)
