# mecha

**Turn your terminal into a multi-agent dev team.** ([中文](README.zh-CN.md))

You talk to a **Coordinator**; it decomposes your request and dispatches tasks to **Specialist** agents, each spawned on demand in its own tmux / iTerm2 / Ghostty pane, so you can watch every task execute live. Task state is driven by agent hook events (no polling), and the agents themselves are pluggable: Claude Code, Codex, Gemini, CodeBuddy, or Pi, mixed per role.

## Features

- **Coordinator + Specialists**: only the Coordinator's prompt carries the dispatch instructions (`mecha ask` + the role list), and Core refuses to dispatch tasks *to* it; Specialists receive tasks but get no dispatch capability.
- **Roles you define**: teams are declared as profiles in `~/.mecha/config.yaml` — role name, prompt, and agent per role. The default `softwarecompany` profile (lead + architect / coder / tester / reviewer) is just one example team.
- **Live panes**: each Specialist runs in its own tmux / iTerm2 / Ghostty pane.
- **Pluggable agents**: Claude Code, Codex, Gemini, CodeBuddy, Pi — mix different agents per role.
- **Hook-driven task state**: provider lifecycle hooks report task completion, while Core also detects process exits and timeouts, with no polling.
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

`mecha serve` does not need any of these: it uses private PTYs (headless mode) and never reads your terminal.

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

## Background / Headless Mode (`serve`)

`mecha serve` runs Core without the interactive Coordinator. It needs no TTY or terminal multiplexer: specialist agents are launched on private PTYs and driven with `mecha ask`.

```bash
# Build the binary
go build -o mecha .

# Start in the background. Logs go to stdout, so the supervisor captures them.
nohup ./mecha serve --workspace /path/to/project --addr 127.0.0.1:50837 </dev/null >/tmp/mecha.log 2>&1 &

# Dispatch a task to a specialist role (the Coordinator is not started)
mecha ask --addr 127.0.0.1:50837 <specialist-role> "task"

# Graceful shutdown
kill -TERM <pid>
```

Notes:

- `serve` does **not** start a Coordinator; only specialist roles are available. There is no `chat` command yet.
- `serve` logs are structured `slog` text on **stdout**, and it creates no log files. Only `serve` writes process logs to stdout; plain `mecha` / `mecha run` still write to `~/.mecha/logs/<workspace>/<YYYY-MM-DD>.log`. Redirect serve's stdout/stderr with nohup, systemd, or launchd. agentd logs, startup failures, and panics from specialist processes are forwarded to the same stdout/stderr, so they remain visible.
- `--addr` defaults to `127.0.0.1:0` and accepts **loopback only**: `127.0.0.0/8`, `::1`, or `localhost`; the port must be `0`-`65535`. IPv6 must use the bracketed form, e.g. `[::1]:0` (bare `::1:0` is rejected). Empty hosts/ports, `0.0.0.0`, `::`, and non-loopback IPs are rejected. This is intentional: the unauthenticated gRPC API can drive agents.
- When the port is dynamic, an unconditional `mecha serve listening on <addr>` line is printed to stdout even at `--log-level error`. Use a fixed `--addr` if you need a stable endpoint.
- Other flags: `--log-level debug|info|warn|error` (default `info`) and `--shutdown-timeout` (default `10s`, minimum `5s`). The minimum leaves room for agentd's 4s Close budget (2s SIGTERM grace + 1s post-SIGKILL wait + 1s webhook shutdown) plus a 1s reserve. The global `--config <path>` selects the config file (default `~/.mecha/config.yaml`).
- There is no pid file, `status`, `stop`, or single-instance lock yet. Starting twice is allowed; each instance must use a different `--addr` (otherwise the second one fails to bind).
- SIGINT/SIGTERM shuts down gracefully and exits 0. Agent process groups get SIGTERM, a grace period, then SIGKILL. When a compatible local `ps` is available, agentd also snapshots the agent's descendants (`ps -axo pid=,ppid=`, falling back to `ps -A -o pid=,ppid=`) and signals those PIDs individually (best-effort), so commands that escape the group with `setsid(2)` are usually cleaned up. If `ps` is unavailable or unsupported, only process-group cleanup is guaranteed. A descendant already reparented to PID 1 before the first scan, or a new child spawned between scans whose parent exits first, cannot be traced and may survive. Pane cleanup is bounded by `--shutdown-timeout`; the final backend close escalation is separately bounded by the backend implementation.

## Configuration

Config lives at `~/.mecha/config.yaml`:

```yaml
agent: claude-sonnet-4-6        # default agent for roles that omit agent
agents:
  - name: codex
    type: codex                 # uses Codex's configured default model
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
          name: codex
      - name: tester
        prompt: |
          You are a test engineer (tester)...
        # Omitting agent inherits the default Claude agent.
```

- A role inherits `agent` by default; set `agent.name` to override it.
- Every profile must have exactly one role with `is_coordinator: true`.
- Logs: `mecha` / `mecha run` write structured text to `~/.mecha/logs/<workspace>/<YYYY-MM-DD>.log`. `mecha serve` writes process logs to stdout and creates no log file; redirect with nohup/systemd/launchd.

## Commands

| Command | Purpose |
|---|---|
| `mecha` / `mecha run` | Start Core + Coordinator (`--config <path>` to override the config file) |
| `mecha init [-f]` | Write default config to `~/.mecha/config.yaml` (existing file backed up to `.bak`; `-f` overwrites without backup) |
| `mecha serve --workspace <dir> --addr <ADDR>` | Headless Core server without a Coordinator; see [Background / Headless Mode](#background--headless-mode-serve) |
| `mecha ask --addr <ADDR> <role> "<task>"` | Dispatch a task (used by the Coordinator, blocking) |
| `mecha version` | Print version, build date, Go runtime |

`mecha agentd` and `mecha webhook` are internal — launched by Core and agent hooks respectively.

## Security

All listeners (Core gRPC and the per-agentd webhook HTTP) bind to loopback only (`127.0.0.1` or `::1`; webhooks use `127.0.0.1`), but they carry **no authentication**: any process running as your user on the machine can call `Ask` and drive the agents (which run with `--dangerously-skip-permissions`-style flags). Treat mecha as a single-user local tool — do not run it on shared machines, and never expose the ports through a proxy or tunnel.

## Documentation

Full design details: [docs/DESIGN.md](docs/DESIGN.md)
