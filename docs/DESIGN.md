# Mecha 方案设计

> mecha 是一个多 agent 编排系统。用户直接与 coordinator 交互，coordinator 通过 `mecha ask` 同步派发任务给 specialist agent。每个 specialist 运行在独立终端 pane 中，任务状态由 Hook 事件驱动。

---

## 1. 概览

### 1.1 解决的痛点

| 痛点 | 解法 |
|---|---|
| 单个 agent 上下文堆叠 | 每个 role agent 独立会话，职责边界清晰 |
| 角色专业度被稀释 | 每个 role 有独立 prompt、独立目录、独立 session |
| 长任务执行不可见 | mecha 在终端 pane 里启动 role agent，用户可直接围观 |
| 黑盒不可追溯 | 关键流程通过 Hook 结构化事件 + 日志落盘 |

### 1.2 术语

| 名词 | 含义 |
|---|---|
| mecha | 唯一的编排进程，负责识别 workspace、启动/回收 role agent |
| coordinator | 用户默认交互角色，承担入口与派活职责 |
| specialist | 被 coordinator 调度的角色，处理具体任务 |
| role agent | 跑在 pane 里的 agent CLI 实例 |
| role | agent 的职责定义，由 profile 中的角色配置描述 |
| profile | 一组角色集合，启动时选择 |
| agent_id | mecha 拉起 role agent 时分配的 UUID，生命周期内不变 |
| session_id | agent CLI 自身的会话 ID，由 SessionStart 事件获取 |
| pane | 终端面板（tmux / iTerm2 / Ghostty），承载一个 role agent |

---

## 2. 架构

### 2.1 进程模型

```
mecha run
  ├── HTTP Server (127.0.0.1:随机端口)
  │   ├── POST /webhook/:agentID    ← mecha webhook 转发
  │   └── POST /ask                 ← mecha ask (阻塞等结果)
  │
  ├── Coordinator (agent 子进程, 接管当前终端)
  │   ├── hooks → mecha webhook --id <uuid> --port <P>
  │   └── 任务分派 → mecha ask --port <P> <role> "<task>"
  │
  └── Specialists (Terminal Backend: tmux / iTerm2 / Ghostty)
      └── 每个 role 一个 pane，任务串行
```

- Coordinator 是 `cmd.Start()` 的子进程，stdin/stdout/stderr 直通终端
- Specialist 通过 `PaneBackend.Spawn()` 在独立 pane 中启动
- 所有 agent 的 webhook 回调到同一个 HTTP Server，按 URL 中 `<agentID>` 分发
- role agent 之间不直接通信，产物落在 workspace 文件系统中

### 2.2 生命周期

| 触发 | 动作 |
|---|---|
| mecha 启动 | 选择 profile，启动 HTTP Server，拉起 coordinator |
| 首次 `ask <role>` | 创建 agent，Spawn 到新 pane，等待 SessionStart 后注入任务 |
| 再次 `ask <role>` | 复用已有 agent，注入新任务 |
| 任务完成 | agent 回到 running 状态，等待下一个任务 |
| coordinator 退出 | 级联 Kill 所有 specialist pane，取消等待的 Ask，关闭 HTTP Server |

### 2.3 约束

- 1 role = 1 个活跃实例，同一 role 不并发
- role 目录在 `Prepare()` 时生成

---

## 3. 配置

文件: `~/.mecha/config.yaml`

```yaml
agent: deepseek-v4-flash          # 默认 agent

agents:
  - name: deepseek-v4-flash
    type: claude
    model: deepseek-v4-flash[1m]
    envs:
      CLAUDE_CODE_MAX_OUTPUT_TOKENS: "8192"

  - name: deepseek-v4-pro
    type: claude
    model: deepseek-v4-pro[1m]
    envs:
      CLAUDE_CODE_MAX_OUTPUT_TOKENS: "16384"

profile: softwarecompany

profiles:
  softwarecompany:
    roles:
      - name: coordinator
        is_coordinator: true
        prompt: |
          你是项目协调者，负责接收需求、拆解任务和汇总结果。
        agent:
          name: deepseek-v4-flash

      - name: architect
        prompt: |
          你是系统架构师，负责技术方案设计。
        agent:
          name: deepseek-v4-pro
```

- `agent` 指向默认 agent，role 可通过 `agent.name` 覆盖
- 每个 profile 必须且只能有一个 `is_coordinator: true` 的 role
- Agent 配置解析: `complete()` 合并 base agent 和 role 层覆盖 (Type, Model, Params, Envs)

---

## 4. 启动与退出

### 4.1 启动

```
mecha run
  │
  ├── New(workspace, cfg)
  │   ├── term.NewAutoProvider()   # 检测终端
  │   └── initLogger()             # ~/.mecha/logs/<workspace-hash>/YYYY-MM-DD.log
  │
  └── Start(ctx)
      ├── 1. 找 coordinator role
      ├── 2. Start HTTP server (127.0.0.1:0)，端口记入 config.WebhookPort
      ├── 3. Create coordinator agent
      │      agent.New() → Prepare(): 写 CLAUDE.md + .claude/settings.json
      ├── 4. launchCoordinator()  # 子进程接管终端, Ctrl-C 透传
      └── 5. Cleanup (coordinator 退出后)
             ├── backend.Kill() 所有 specialist pane
             ├── 取消所有等待的 Ask
             └── srv.Shutdown()
```

### 4.2 生成物

```
<workspace>/.mecha/roles/<role-name>/
├── CLAUDE.md                          # role prompt + workspace (coordinator 额外含 available_roles)
└── .claude/
    └── settings.json                  # hooks (SessionStart, Stop, StopFailure)
```

### 4.3 settings.json

```json
{
  "hooks": {
    "SessionStart": [{
      "hooks": [{
        "type": "command",
        "command": "mecha",
        "args": ["webhook", "--id", "<uuid>", "--port", "<端口>"]
      }]
    }],
    "Stop":          [ ... ],
    "StopFailure":   [ ... ]
  }
}
```

### 4.4 CLAUDE.md

Coordinator 额外包含 `<available_roles>` 块:

```
<available_roles>
  You can delegate tasks by running:
    mecha ask --port <P> <role> "<task>"

  Available roles:
  - architect: 你是系统架构师...
  - coder: 你是开发实现者...
</available_roles>
```

Specialist 不含此块，不能调度子 agent。

---

## 5. 任务分派 (Ask)

### 5.1 流程

```
coordinator 执行: mecha ask --port <P> <role> "<task>"
  │
  └── POST /ask {"role":"<role>", "task":"<task>"}  (阻塞)
       │
       └── Core.Ask(ctx, role, task)
            │
            ├── agent 不存在?
            │   ├── createAgent()      # agent.New + Prepare
            │   ├── backend.Spawn()    # 新 pane, status = starting
            │   └── 等待 SessionStart  # 30s 超时 → 失败
            │
            ├── status = busy
            ├── Send(task)
            ├── 等待 result (Stop → output; StopFailure → error)
            ├── status = running
            └── return result
```

- `mecha ask` 同步阻塞，stdout 直接输出 agent 返回内容，exit 0
- 失败时 stderr 输出错误信息，exit 1

### 5.2 Agent 状态机

```
                              SessionStart
Spawn ──► starting ────────────────────────► running
                 │                              │
                 │ 30s 超时 → 失败               │ Ask 下发任务
                 │                              ▼
                 │                            busy
                 │                              │
                 │                 ┌────────────┤
                 │                 ▼            ▼
                 │           Stop (完成)   StopFailure (失败)
                 │                 │            │
                 │                 └─────┬──────┘
                 │                       ▼
                 │                    running
                 │
                 └──► 失败 (返回错误给 Ask)
```

| 状态 | 含义 |
|---|---|
| starting | agent 已 spawn，等待启动完毕 |
| running | agent 就绪，可接收任务 |
| busy | 正在执行任务，等待 Stop 或 StopFailure |

### 5.3 关键数据结构

```go
type instance struct {
    role   string
    agent  types.Agent
    handle term.PaneHandle
    status string           // starting | running | busy
    result chan taskResult
}

type taskResult struct {
    output string
    err    string
}

type Core struct {
    coordinator       types.Agent
    specialists       map[string]*instance
    agentByID         map[string]types.Agent
    instanceByAgentID map[string]*instance
}
```

### 5.4 Coordinator 与 Specialist

| | Coordinator | Specialist |
|---|---|---|
| 启动方式 | `cmd.Start()` 子进程 | `backend.Spawn()` pane |
| stdin/stdout | 直连终端 | 终端 pane |
| 有 PaneHandle | ❌ | ✅ |
| 可接收任务 | ❌ | ✅ |
| 可调度子 agent | ✅ | ❌ |
| 有 agent 状态机 | ❌ (只观测) | ✅ |

---

## 6. 通信

### 6.1 PaneBackend 接口

| 方法 | 用途 |
|---|---|
| `Spawn(workDir, cmd, env) → handle` | 拉起 pane，跑 role agent CLI |
| `Send(handle, text)` | 注入文本到 pane stdin |
| `Kill(handle)` | 关闭 pane |

**后端**: tmux (macOS/Linux), iTerm2 (macOS), Ghostty (macOS/跨平台)

### 6.2 CLI 命令

| 命令 | 调用方 | 用途 |
|---|---|---|
| `mecha` | 用户 | 等同于 `mecha run` |
| `mecha run` | 用户 | 启动 mecha |
| `mecha init [-f]` | 用户 | 初始化 `~/.mecha/config.yaml` |
| `mecha ask --port <P> <role> "<task>"` | coordinator | 派发任务 (阻塞等结果) |
| `mecha webhook --id <uuid> --port <P>` | agent hook | 从 stdin 读取 Hook JSON 转发给 mecha |

### 6.3 Hook 回调

agent 原生 Hook handler 触发时调用 `mecha webhook`，Hook JSON 通过 stdin 传入，`mecha webhook` POST 到 HTTP Server。

**事件流**:

```
Agent 触发 hook
  └── 执行: mecha webhook --id <uuid> --port <P>
       └── stdin: {"hook_event_name":"Stop", ...}
       └── POST /webhook/<uuid>
            │
            └── Core.handleWebhook()
                 ├── 提取 agentID (URL path)
                 ├── agentByID[agentID].ParseHookEvent(body)
                 └── onEvent(agentID, event)
```

**事件处理**:

| 事件 | 处理 |
|---|---|
| `SessionStart` | 首次: `starting → running`；后续: 状态不变 |
| `Stop` | `busy → running`；result chan ← output |
| `StopFailure` | `busy → running`；result chan ← error |

**统一事件结构**:

```go
type HookEvent struct {
    AgentID      string
    Event        string          // SessionStart | Stop | StopFailure
    SessionID    string
    Output       string          // Stop: last_assistant_message
    OutputSource string
    Error        string          // StopFailure: error_type
    Raw          json.RawMessage
}
```

**Agent 接口**:

```go
type Agent interface {
    Prepare() error
    Cmd() *exec.Cmd
    ParseHookEvent(raw []byte) (HookEvent, error)
    ID() string
}
```

不同 agent 类型各自实现 `ParseHookEvent`，将原生事件转为统一结构。

---

## 7. 代码结构

```
cmd/
  main.go          # 根命令
  run.go           # run 子命令
  init.go          # init 子命令
  ask.go           # ask 子命令
  webhook.go       # webhook 子命令

pkg/
  config/
    config.go      # 配置加载、校验、合并
  agent/
    agent.go       # agent 注册表 + prompt 渲染
    types/
      types.go     # Agent 接口 + HookEvent
    claude/
      claude.go    # Claude agent 实现
      event.go     # Claude Hook JSON 解析
  core/
    app.go         # Core 结构体, New, Start
    coordinator.go # launchCoordinator
    agent.go       # createAgent, Ask, ensureSpecialist
    server.go      # HTTP server + handlers + onEvent
    logger.go      # slog → ~/.mecha/logs/<workspace-hash>/
    util.go        # envSliceToMap
  term/
    provider.go    # PaneBackend 接口 + PaneSpec
    auto.go        # 终端自动检测
    common.go      # 共用逻辑
    tmux.go        # tmux 后端
    iterm2.go      # iTerm2 后端
    ghostty.go     # Ghostty 后端
```

---

## 8. 日志

```
~/.mecha/logs/<workspace-hash>/YYYY-MM-DD.log
```

- `workspace-hash` = workspace 绝对路径的短哈希，多 workspace 自动隔离
- slog TextHandler, 输出 key=value 格式，带文件名和行号
- 按天追加，轮转 / 清理暂不做

---

## 9. 设计决策

| 决策 | 结论 |
|---|---|
| mecha ask | 同步阻塞，stdout 输出结果 / exit 0，stderr 输出错误 / exit 1 |
| ask 输出格式 | 直接输出 agent 返回内容，不包装 |
| 并发 | 不做，同一 role 一次一个任务 |
| SessionStart | 首次: starting→running; 后续: 状态不变 |
| coordinator 退出 | 级联 Kill specialist pane + 取消等待的 Ask |
| Hook 事件 | 只注册 SessionStart / Stop / StopFailure |

---

## 10. 遗留项

| 遗留项 | 说明 |
|---|---|
| 执行超时 | 任务下发后无限等待，超时 Kill 未实现 |
| agent 崩溃 | pane 进程退出无感知，Ask 会永远阻塞 |
| coordinator 特殊化 | 启动方式不同（子进程 vs pane），未统一模型 |
| session_id 持久化 | 仅存内存，重启后丢失，resume 无法恢复历史会话 |
| backend.Send 可靠性 | 文本注入 pane 后无送达确认，pane 死亡时可能静默丢失 |
| resume 支持 | session_id 已获取，agent 会话恢复机制未接入 |
| mecha 进程崩溃 | 被 SIGKILL 后 coordinator 和 specialist pane 残留 |
| 日志轮转 | 按天追加，无上限，长期运行会持续增长 |
| 信任对话框 | 每个新 role 目录首次启动弹出信任确认，可用  Prepare() 中 -p 模式预授权 |

