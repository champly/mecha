# mecha

多 agent 编排系统。（[English](README.md)）Coordinator 拆解需求后通过 `mecha ask` 分派给 specialist，任务状态由 Hook 事件驱动。

## 运行原理

```
用户
 │
 ▼
Coordinator（agentd，当前终端）
 │  mecha ask --addr <ADDR> <role> "<task>"
 ▼
mecha Core（gRPC server，127.0.0.1）
 │  Spawn pane + TaskChannel（gRPC 双向流）
 ├──► agentd（architect pane）── PTY ── agent CLI
 ├──► agentd（coder pane）    ── PTY ── agent CLI
 └──► agentd（tester pane）   ── PTY ── agent CLI

Hook 事件：agent ──► mecha webhook ──► agentd 本地 HTTP ──► Core 状态机
```

- **Coordinator**：接收需求、拆解任务、通过 `mecha ask` 派发，不亲自执行
- **agentd**：每个 role 一个，通过 PTY 托管常驻 agent 进程，经 gRPC 与 Core 通信；coordinator 和 specialist 是同一个 agentd 二进制
- **Specialist**：各自运行在独立终端 pane（architect / coder / tester / reviewer），任务执行过程可直接围观
- **Hook 事件**：`SessionStart`（启动完成）、`Stop`（任务成功）、`StopFailure`（任务失败）经 agentd 本地 HTTP 回传，驱动 Core 的状态机

## 快速开始

### 终端配置

**iTerm2** 需要开启 Python API（WebSocket）：

1. 打开 iTerm2 → **Preferences**（`⌘,`）
2. 进入 **General** → **Magic**
3. 勾选 **✓ Enable Python API**

**tmux** 和 **Ghostty** 无需额外配置。

```bash
# 安装
go install github.com/champly/mecha@latest

# 查看版本
mecha version

# 初始化配置
mecha init

# 启动 mecha
mecha
```

配置：`~/.mecha/config.yaml`

详细设计：[docs/DESIGN.md](docs/DESIGN.md)
