# mecha

**把你的终端变成一支多 agent 开发团队。**（[English](README.md)）

你只需和 **Coordinator** 对话：它拆解需求、把任务派发给 **Specialist**——每个 Specialist 按需拉起，跑在独立的 tmux / iTerm2 / Ghostty pane 里，执行过程全程围观。任务状态由 agent 的 hook 事件驱动（无轮询），agent 本身可插拔：Claude Code、Codex、Gemini、CodeBuddy、Pi，可按 role 混用。

## 特性

- **Coordinator + Specialist 分工**：只有 Coordinator 的 prompt 会注入派发指令（`mecha ask` + 可用 role 列表），且 Core 拒绝向它派发任务；Specialist 只接任务、无派发能力。
- **角色由你定义**：团队以 profile 形式声明在 `~/.mecha/config.yaml` 中——每个 role 的名字、prompt、使用的 agent 都可配置。默认的 `softwarecompany` profile（lead + architect / coder / tester / reviewer）只是一个示例团队。
- **执行过程可见**：每个 Specialist 运行在独立的 tmux / iTerm2 / Ghostty pane 中。
- **可插拔 agent**：支持 Claude Code、Codex、Gemini、CodeBuddy、Pi，不同 role 可混用不同 agent。
- **Hook 事件驱动**：`SessionStart` / `Stop` / `StopFailure` 事件回传 Core 驱动状态机，无需轮询。
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

## 配置说明

配置文件：`~/.mecha/config.yaml`

```yaml
agent: claude-sonnet-4-6        # 默认 agent
agents:
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
          name: claude-sonnet-4-6
```

- 每个 role 可通过 `agent.name` 覆盖默认 agent。
- 每个 profile 必须且只能有一个 `is_coordinator: true` 的 role。
- 日志：`~/.mecha/logs/<workspace>/<YYYY-MM-DD>.log`

## 命令一览

| 命令 | 用途 |
|---|---|
| `mecha` / `mecha run` | 启动 Core + Coordinator（`--config <path>` 指定配置文件） |
| `mecha init [-f]` | 写出默认配置到 `~/.mecha/config.yaml`（已有文件备份为 `.bak`；`-f` 覆盖不备份） |
| `mecha ask --addr <ADDR> <role> "<task>"` | 派发任务（由 Coordinator 调用，阻塞等结果） |
| `mecha version` | 输出版本、构建日期和 Go 运行时信息 |

`mecha agentd` 与 `mecha webhook` 为内部命令，分别由 Core 和 agent hook 拉起，用户不直接调用。

## 安全说明

所有监听端口（Core gRPC 和各 agentd 的 webhook HTTP）都只绑定 `127.0.0.1`，但**没有任何鉴权**：本机上与你的用户同权的任意进程都可以调用 `Ask` 驱动 agent（而 agent 以 `--dangerously-skip-permissions` 之类的放行模式运行）。请把 mecha 当作单机单用户的本地工具：不要在共享机器上运行，也不要通过代理或隧道把端口暴露出去。

## 文档

完整设计细节：[docs/DESIGN.md](docs/DESIGN.md)
