<img alt="Telara" src="https://raw.githubusercontent.com/Telara-Labs/Telara-CLI/main/docs/logo-light.png" height="52">

# @telara-cli/cli

The official CLI for [Telara](https://telara.dev) — connect your AI coding tools to your organization's MCP configurations. Claude Code, Cursor, Windsurf, and VS Code are configured automatically on login.

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
# Log in — auto-connects your installed tools
telara login

# Verify everything is working
telara doctor
```

---

## Three-layer configuration

| Layer | Name | How it's set |
|-------|------|-------------|
| **1** | Managed | Automatic on login |
| **2** | Global | `telara config global <name>` |
| **3** | Project | `telara config project <name>` |

---

## Commands

### Authentication

| Command | Description |
|---------|-------------|
| `telara login` | Sign in via browser or `--token <tlrc_...>` — auto-connects tools |
| `telara logout` | Revoke token and remove local credentials |
| `telara whoami` | Show current user and organization |

### Configuration

| Command | Description |
|---------|-------------|
| `telara config` | Show what's configured at each layer |
| `telara config list` | List available configurations |
| `telara config show <name>` | Inspect a configuration |
| `telara config global <name>` | Set global configuration (Layer 2) |
| `telara config project <name>` | Set project-specific configuration (Layer 3) |
| `telara config keys <name>` | List API keys (read-only) |

### Provisioning (admin)

| Command | Description |
|---------|-------------|
| `telara provision claude-web` | Credentials for Claude.ai Organization Connector |
| `telara provision ci` | Service account key for CI/CD pipelines |
| `telara provision managed` | Config for enterprise MDM / GPO deployment |

### Diagnostics

| Command | Description |
|---------|-------------|
| `telara doctor` | Check connectivity, auth, and tool configuration |
| `telara version` | Print version |
| `telara update` | Self-update to the latest release |

---

## Supported agent tools

- **Claude Code** — global and project scope
- **Cursor** — global and project scope
- **Windsurf** — global and project scope
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
