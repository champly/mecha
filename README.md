# mecha

Multi-agent orchestration system. ([中文](README.zh-CN.md)) The Coordinator breaks down requirements and dispatches tasks to specialists via `mecha ask`, with task state driven by Hook events.

## How It Works

```
User
 │
 ▼
Coordinator (agentd, current terminal)
 │  mecha ask --addr <ADDR> <role> "<task>"
 ▼
mecha Core (gRPC server, 127.0.0.1)
 │  Spawn pane + TaskChannel (gRPC bidi stream)
 ├──► agentd (architect pane) ── PTY ── agent CLI
 ├──► agentd (coder pane)     ── PTY ── agent CLI
 └──► agentd (tester pane)    ── PTY ── agent CLI

Hook events: agent ──► mecha webhook ──► agentd local HTTP ──► Core state machine
```

- **Coordinator**: Receives requirements, decomposes tasks, dispatches via `mecha ask` — never executes directly.
- **agentd**: One per role; manages a long-lived agent process over a PTY and talks to Core via gRPC. Coordinator and specialists run the same `agentd` binary.
- **Specialists**: Each runs in its own terminal pane (architect / coder / tester / reviewer), so you can watch tasks execute live.
- **Hook Events**: `SessionStart` (boot complete), `Stop` (task success), `StopFailure` (task failure) are forwarded to the role's agentd over local HTTP and drive Core's state machine.

## Quick Start

### Terminal Setup

**iTerm2** requires the Python API (WebSocket) to be enabled:

1. Open iTerm2 → **Preferences** (`⌘,`)
2. Go to **General** → **Magic**
3. Enable **✓ Enable Python API**

**tmux** and **Ghostty** work out of the box.

```bash
# Install
go install github.com/champly/mecha@latest

# Check version
mecha version

# Initialize config
mecha init

# Start mecha
mecha
```

Config: `~/.mecha/config.yaml`

Full design: [docs/DESIGN.md](docs/DESIGN.md)
