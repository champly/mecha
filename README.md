# mecha

**Turn your terminal into a multi-agent dev team.** ([中文](README.zh-CN.md))

You talk to a **Coordinator**; it decomposes your request and dispatches tasks to **Specialist** agents, each spawned on demand in its own tmux / iTerm2 / Ghostty pane, so you can watch every task execute live. Task state is driven by agent hook events (no polling), and the agents themselves are pluggable: Claude Code, Codex, Gemini, CodeBuddy, or Pi, mixed per role.

## Features

- **Coordinator + Specialists**: only the Coordinator's prompt carries the dispatch instructions (`mecha ask` + the role list), and Core refuses to dispatch tasks *to* it; Specialists receive tasks but get no dispatch capability.
- **Roles you define**: teams are declared as profiles in `~/.mecha/config.yaml` — role name, prompt, and agent per role. The default `softwarecompany` profile (lead + architect / coder / tester / reviewer) is just one example team.
- **Live panes**: each Specialist runs in its own tmux / iTerm2 / Ghostty pane.
- **Pluggable agents**: Claude Code, Codex, Gemini, CodeBuddy, Pi — mix different agents per role.
- **Hook-driven state machine**: `SessionStart` / `Stop` / `StopFailure` events flow back to the Core, no polling.
- **Single binary**: Core, agentd, and CLI are one `mecha` binary; no external services.

## How It Works

```
User
 │  stdin/stdout (current terminal)
 ▼
Coordinator (agentd, foreground child of Core) ── PTY ── agent CLI
 │  mecha ask --addr <ADDR> <role> "<task>"
 ▼
mecha Core (gRPC server, 127.0.0.1:<random port>)
 │  Spawn pane + TaskChannel (gRPC bidi stream)
 ├──► agentd (architect pane) ── PTY ── agent CLI
 ├──► agentd (coder pane)     ── PTY ── agent CLI
 └──► agentd (tester pane)    ── PTY ── agent CLI

Hook events: agent ──► mecha webhook ──► agentd local HTTP
                  ──► gRPC (ReportStatus / TaskResult) ──► Core state machine
```

- **Coordinator**: the role you talk to; its prompt is injected with the dispatch instructions (`mecha ask` + available roles), and Core refuses tasks addressed to it.
- **agentd**: one per role; manages a long-lived agent process over a PTY and talks to Core via gRPC. Coordinator and Specialists run the same `agentd` binary.
- **Specialists**: each runs in its own terminal pane, so you can watch tasks execute live.
- **Hook events**: forwarded to the role's agentd over local HTTP and drive Core's state machine.

## Requirements

- One of the supported agent CLIs installed and authenticated: `claude`, `codex`, `gemini`, `codebuddy`, or `pi`.
- A supported terminal multiplexer/emulator (auto-detected, in priority order):
  - **tmux** — works out of the box.
  - **iTerm2** — requires the Python API: **Preferences** (`⌘,`) → **General** → **Magic** → enable **✓ Enable Python API**.
  - **Ghostty** — panes are driven via AppleScript, so a recent version with AppleScript support is required; on first spawn, macOS will ask for Automation permission for Ghostty — allow it.

## Quick Start

```bash
# Install
go install github.com/champly/mecha@latest

# Check version
mecha version

# Initialize config (existing config.yaml is backed up to config.yaml.bak; -f overwrites without backup)
mecha init

# Start mecha (bare `mecha` == `mecha run`)
mecha
```

Then just tell the Coordinator what you want built — it takes care of decomposition and dispatch.

## Configuration

Config lives at `~/.mecha/config.yaml`:

```yaml
agent: claude-sonnet-4-6        # default agent
agents:
  - name: claude-sonnet-4-6
    type: claude                # claude / codex / gemini / codebuddy / pi
    model: claude-sonnet-4-6

profile: softwarecompany        # active role set
profiles:
  softwarecompany:
    roles:
      - name: lead
        is_coordinator: true    # exactly one per profile
        prompt: |
          You are the Lead (orchestrator) running inside mecha...
        agent:
          name: claude-sonnet-4-6
      - name: coder
        prompt: |
          You are a developer (coder)...
        agent:
          name: claude-sonnet-4-6
```

- Each role can override the default agent via `agent.name`.
- Every profile must have exactly one role with `is_coordinator: true`.
- Logs: `~/.mecha/logs/<workspace>/<YYYY-MM-DD>.log`

## Commands

| Command | Purpose |
|---|---|
| `mecha` / `mecha run` | Start Core + Coordinator (`--config <path>` to override the config file) |
| `mecha init [-f]` | Write default config to `~/.mecha/config.yaml` (existing file backed up to `.bak`; `-f` overwrites without backup) |
| `mecha ask --addr <ADDR> <role> "<task>"` | Dispatch a task (used by the Coordinator, blocking) |
| `mecha version` | Print version, build date, Go runtime |

`mecha agentd` and `mecha webhook` are internal — launched by Core and agent hooks respectively.

## Security

All listeners (Core gRPC and the per-agentd webhook HTTP) bind to `127.0.0.1` only, but they carry **no authentication**: any process running as your user on the machine can call `Ask` and drive the agents (which run with `--dangerously-skip-permissions`-style flags). Treat mecha as a single-user local tool — do not run it on shared machines, and never expose the ports through a proxy or tunnel.

## Documentation

Full design details: [docs/DESIGN.md](docs/DESIGN.md)
