# mecha

**把你的终端变成一支多 agent 开发团队。**（[English](README.md)）

你只需和 **Coordinator** 对话：它拆解需求、把任务派发给 **Specialist**——每个 Specialist 按需拉起，跑在独立的 tmux / iTerm2 / Ghostty pane 里，执行过程全程围观。任务状态由 agent 的 hook 事件驱动（无轮询），agent 本身可插拔：Claude Code、Codex、Gemini、CodeBuddy、Pi，可按 role 混用。

## 特性

- **Coordinator + Specialist 分工**：只有 Coordinator 的 prompt 会注入派发指令（`mecha ask` + 可用 role 列表），且 Core 拒绝向它派发任务；Specialist 只接任务、无派发能力。
- **角色由你定义**：团队以 profile 形式声明在 `~/.mecha/config.yaml` 中——每个 role 的名字、prompt、使用的 agent 都可配置。默认的 `softwarecompany` profile（lead + architect / coder / tester / reviewer）只是一个示例团队。
- **执行过程可见**：每个 Specialist 运行在独立的 tmux / iTerm2 / Ghostty pane 中。
- **可插拔 agent**：支持 Claude Code、Codex、Gemini、CodeBuddy、Pi，不同 role 可混用不同 agent。
- **Hook 驱动任务状态**：各 agent 通过生命周期 hook 回报任务完成，同时 Core 会处理进程退出和超时，无需轮询。
- **单二进制**：Core、agentd、CLI 都在一个 `mecha` 二进制里，无外部服务依赖。

## 运行原理

```
用户
 │  stdin/stdout（当前终端）
 ▼
Coordinator（agentd，Core 的前台子进程）── PTY ── agent CLI
 │  mecha ask --addr <ADDR> <role> "<task>"
 ▼
mecha Core（gRPC server，127.0.0.1:<随机端口>）
 │  Spawn pane + TaskChannel（gRPC 双向流）
 ├──► agentd（architect pane）── PTY ── agent CLI
 ├──► agentd（coder pane）    ── PTY ── agent CLI
 └──► agentd（tester pane）   ── PTY ── agent CLI

Hook 事件：agent ──► mecha webhook ──► agentd 本地 HTTP
                ──► gRPC（ReportStatus / TaskResult）──► Core 状态机
```

- **Coordinator**：用户交互角色；prompt 中注入了派发指令（`mecha ask` + 可用 role 列表），Core 拒绝向它派发任务。
- **agentd**：每个 role 一个，通过 PTY 托管常驻 agent 进程，经 gRPC 与 Core 通信；Coordinator 和 Specialist 是同一个 agentd 二进制。
- **Specialist**：各自运行在独立终端 pane，任务执行过程可直接围观。
- **Hook 事件**：经 agentd 本地 HTTP 回传，驱动 Core 的状态机。

## 环境要求

- 已安装并完成认证的 agent CLI 之一：`claude`、`codex`、`gemini`、`codebuddy` 或 `pi`。
- 受支持的终端（自动检测，按优先级）：
  - **tmux**：无需额外配置。
  - **iTerm2**：需开启 Python API：**Preferences**（`⌘,`）→ **General** → **Magic** → 勾选 **✓ Enable Python API**。
  - **Ghostty**：分屏通过 AppleScript 驱动，需要支持 AppleScript 的较新版本；首次分屏时 macOS 会弹出针对 Ghostty 的"自动化"权限请求，请允许。

`mecha serve` 不需要以上任一终端环境：它使用私有 PTY（headless 模式），也不会读取你的终端。

## 快速开始

```bash
# 安装
go install github.com/champly/mecha@latest

# 查看版本
mecha version

# 初始化配置（已有 config.yaml 备份为 config.yaml.bak；-f 直接覆盖不备份）
mecha init

# 启动 mecha（裸 `mecha` 等同 `mecha run`）
mecha
```

启动后直接告诉 Coordinator 你要做什么，拆解和派活由它完成。

## 后台运行（headless）

`mecha serve` 以 headless 方式运行 Core，不启动交互式 Coordinator，也不需要 TTY 或终端复用器：每个 specialist 都跑在私有 PTY 上，通过 `mecha ask` 驱动。

```bash
# 构建
go build -o mecha .

# 后台启动。日志写 stdout，由 supervisor 捕获
nohup ./mecha serve --workspace /path/to/project --addr 127.0.0.1:50837 </dev/null >/tmp/mecha.log 2>&1 &

# 给 specialist role 派发任务（serve 不启动 Coordinator）
mecha ask --addr 127.0.0.1:50837 <specialist-role> "task"

# 优雅退出
kill -TERM <pid>
```

说明：

- `serve` **不启动 Coordinator**，只提供 specialist 角色；暂不支持 `chat`。
- `serve` 的日志是 `slog` 结构化文本，写 **stdout**，且不会创建日志文件。只有 `serve` 把进程日志输出到 stdout；裸 `mecha` / `mecha run` 仍写工作区日志文件 `~/.mecha/logs/<workspace>/<YYYY-MM-DD>.log`。由 nohup、systemd 或 launchd 负责重定向 serve 的 stdout/stderr。agentd 日志、specialist 启动失败和 panic 会转发到同一 stdout/stderr，不会被丢弃。
- `--addr` 默认 `127.0.0.1:0`，且**只接受 loopback**：`127.0.0.0/8`、`::1` 或 `localhost`；端口必须是 `0`-`65535`。IPv6 必须使用方括号形式，例如 `[::1]:0`（裸写 `::1:0` 会被拒绝）。空 host/端口、`0.0.0.0`、`::` 和非 loopback IP 一律拒绝。原因：无鉴权的 gRPC API 可以驱动 agent。
- 使用动态端口时，启动后会无条件向 stdout 打印 `mecha serve listening on <addr>`，即使 `--log-level error` 也能看到；需要固定入口时请显式传 `--addr`。
- 其他 flag：`--log-level debug|info|warn|error`（默认 `info`）、`--shutdown-timeout`（默认 `10s`，最小 `5s`）。最小值给 agentd 的 4s Close 预算（2s SIGTERM 宽限 + 1s SIGKILL 后等待 + 1s webhook 关闭）留出 1s 余量；全局 `--config <path>` 指定配置文件（默认 `~/.mecha/config.yaml`）。
- 暂不提供 pid 文件、`status`、`stop` 或单实例锁。允许重复启动，但多个实例必须使用不同 `--addr`，否则第二个实例会因端口占用启动失败。
- SIGINT/SIGTERM 会优雅退出并返回 0：agent 进程组先收到 SIGTERM，宽限期后仍未退出则 SIGKILL。本机有可用且兼容的 `ps` 时，agentd 还会用 `ps -axo pid=,ppid=`（失败时尝试 `ps -A -o pid=,ppid=`）快照 agent 的后代并逐个 PID 发信号（best-effort），因此用 `setsid(2)` 脱离进程组的命令通常也会被清理；若 `ps` 不可用或不兼容，则只保证进程组清理。若后代在第一次扫描前已 reparent 到 PID 1，或两次扫描之间新 spawn 且父进程先退出，则无法追溯，仍可能残留。pane 清理受 `--shutdown-timeout` 约束；最后的 backend close 升级由对应 backend 自身的有界实现兜底。

## 配置说明

配置文件：`~/.mecha/config.yaml`

```yaml
agent: claude-sonnet-4-6        # role 未指定 agent 时使用
agents:
  - name: codex
    type: codex                 # 使用 Codex 自身配置的默认模型
  - name: claude-sonnet-4-6
    type: claude                # claude / codex / gemini / codebuddy / pi
    model: claude-sonnet-4-6

profile: softwarecompany        # 启用的角色集合
profiles:
  softwarecompany:
    roles:
      - name: lead
        is_coordinator: true    # 每个 profile 恰好一个
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
        # 不写 agent，自动继承默认的 Claude agent
```

- role 默认继承 `agent`；可通过 `agent.name` 覆盖。
- 每个 profile 必须且只能有一个 `is_coordinator: true` 的 role。
- 日志：`mecha` / `mecha run` 写 `~/.mecha/logs/<workspace>/<YYYY-MM-DD>.log`；`mecha serve` 写 stdout 且不创建日志文件，由 nohup/systemd/launchd 重定向。

## 命令一览

| 命令 | 用途 |
|---|---|
| `mecha` / `mecha run` | 启动 Core + Coordinator（`--config <path>` 指定配置文件） |
| `mecha init [-f]` | 写出默认配置到 `~/.mecha/config.yaml`（已有文件备份为 `.bak`；`-f` 覆盖不备份） |
| `mecha serve --workspace <dir> --addr <ADDR>` | headless Core 服务，不启动 Coordinator；见[后台运行（headless）](#后台运行headless) |
| `mecha ask --addr <ADDR> <role> "<task>"` | 派发任务（由 Coordinator 调用，阻塞等结果） |
| `mecha version` | 输出版本、构建日期和 Go 运行时信息 |

`mecha agentd` 与 `mecha webhook` 为内部命令，分别由 Core 和 agent hook 拉起，用户不直接调用。

## 安全说明

所有监听端口（Core gRPC 和各 agentd 的 webhook HTTP）都只绑定 loopback（`127.0.0.1` 或 `::1`；webhook 使用 `127.0.0.1`），但**没有任何鉴权**：本机上与你的用户同权的任意进程都可以调用 `Ask` 驱动 agent（而 agent 以 `--dangerously-skip-permissions` 之类的放行模式运行）。请把 mecha 当作单机单用户的本地工具：不要在共享机器上运行，也不要通过代理或隧道把端口暴露出去。

## 文档

- 当前系统实现：[docs/DESIGN.md](docs/DESIGN.md)
- 后续 daemon 与 IM 路线：[docs/ROADMAP.md](docs/ROADMAP.md)
