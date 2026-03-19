<picture>
  <source media="(prefers-color-scheme: dark)" srcset="docs/logo-dark.png">
  <img alt="Telara" src="docs/logo-light.png" height="52">
</picture>

# Telara CLI

The official command-line interface for [Telara](https://telara.dev). Connect your AI coding tools to your organization's MCP configurations — Claude Code, Cursor, Windsurf, and VS Code are configured automatically on login.

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
# 1. Log in — auto-connects your installed tools (Layer 1)
telara login

# 2. Verify everything is working
telara doctor
```

That's it. On first login, the CLI detects your installed AI tools and connects them to your organization's default MCP configuration.

---

## Three-layer configuration

Telara configs are applied in three layers — each overrides the one below:

| Layer | Name | How it's set | Scope |
|-------|------|-------------|-------|
| **1** | Managed | Automatic on `telara login` | Organization-wide |
| **2** | Global | `telara config global <name>` | All your projects |
| **3** | Project | `telara config project <name>` | Single directory |

Resolution order: **Project > Global > Managed**

---

## Commands

### Authentication

| Command | Description |
|---------|-------------|
| `telara login` | Sign in via browser (device flow) or `--token <tlrc_...>` — auto-connects tools |
| `telara logout` | Revoke token, save MCP configs, and remove local credentials |
| `telara whoami` | Show current user, organization, and token prefix |

### Configuration

| Command | Description |
|---------|-------------|
| `telara config` | Show what's configured at each layer and which tools are connected |
| `telara config list` | List MCP configurations you have access to |
| `telara config show <name>` | Show data sources, deployments, policies, and MCP URL |
| `telara config global <name>` | Set your global configuration (Layer 2) |
| `telara config project <name>` | Set a project-specific configuration for the current directory (Layer 3) |
| `telara config keys <name>` | List active API keys (read-only) |

### Provisioning (admin)

Generate credentials for environments where the CLI isn't available.

| Command | Description |
|---------|-------------|
| `telara provision claude-web` | Credentials for Claude.ai (Anthropic Organization Connector) |
| `telara provision ci` | Service account key for CI/CD pipelines |
| `telara provision managed` | Config for enterprise MDM / GPO fleet deployment |

### Diagnostics

| Command | Description |
|---------|-------------|
| `telara doctor` | Check connectivity, auth, and tool configuration |
| `telara version` | Print version, commit hash, and build date |
| `telara update` | Self-update to the latest release |

---

## Global flags

| Flag | Description |
|------|-------------|
| `-v, --verbose` | Print full HTTP responses on errors |

---

## Supported agent tools

| Tool | Detected automatically | Global config | Project config |
|------|----------------------|---------------|----------------|
| Claude Code | ✓ | ✓ | ✓ |
| Cursor | ✓ | ✓ | ✓ |
| Windsurf | ✓ | ✓ | ✓ |
| VS Code | ✓ | — | ✓ |

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
