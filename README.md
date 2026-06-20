# mecha

Multi-agent orchestration system. ([中文](README.zh-CN.md)) The Coordinator breaks down requirements and dispatches tasks to specialists via `mecha ask`, with task state driven by Hook events.

## How It Works

```
User
 │
 ▼
┌─────────────────────────────────────────────────────────┐
│  Coordinator (current terminal)                       │
│                                                         │
│  Receive → Decompose → mecha ask <role> "<task>"        │
│                                                         │
│  ┌─────────────────────────────────────────────────┐   │
│  │          mecha (HTTP Server)                     │   │
│  │                                                 │   │
│  │   POST /ask  ◄────────  POST /webhook/:id       │   │
│  │   (blocking)             (event callback)        │   │
│  └──────┬──────────────────────▲───────────────────┘   │
│         │ Spawn                │ Hook                   │
│         ▼                      │                       │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐             │
│  │architect │  │  coder   │  │ tester   │  ...        │
│  │ (pane)   │  │ (pane)   │  │ (pane)   │             │
│  └──────────┘  └──────────┘  └──────────┘             │
│                                                         │
│  Task done → Coordinator aggregates → returns to User  │
└─────────────────────────────────────────────────────────┘
```

- **Coordinator**: Receives requirements, decomposes tasks, dispatches — never executes directly.
- **Specialists**: Execute tasks in independent panes (architect / coder / tester / reviewer).
- **Hook Events**: `SessionStart` (boot complete), `Stop` (task success), `StopFailure` (task failure) drive state transitions.

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
