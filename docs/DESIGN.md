# Mecha 设计文档

> mecha 是一个多 agent 编排系统。用户直接与 Coordinator 交互，Coordinator 通过 `mecha ask` 同步派发任务给 Specialist agent。每个 Specialist 运行在独立终端 pane 中，任务状态由 Hook 事件驱动。

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
| Coordinator | 用户默认交互角色，承担入口与派活职责 |
| Specialist | 被 Coordinator 调度的角色，处理具体任务 |
| role agent | 跑在 pane 里的 Claude Code 实例 |
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
  ├── Coordinator (agent 子进程, 接管当前终端, cmd.Start)
  │   ├── hooks → mecha webhook --id <uuid> --port <P>
  │   └── 任务分派 → mecha ask --port <P> <role> "<task>"
  │
  └── Specialists (Terminal Backend: tmux / iTerm2 / Ghostty)
      └── 每个 role 一个 pane，任务串行
```

- **Coordinator** 是 `cmd.Start()` 的子进程，stdin/stdout/stderr 直通当前终端
- **Specialist** 通过后端 `Spawn()` 在独立 pane 中启动
- 所有 agent 的 webhook 回调到同一个 HTTP Server，按 URL 中 `<agentID>` 分发
- role agent 之间不直接通信，产物落在 workspace 文件系统中

### 2.2 生命周期

| 触发 | 动作 |
|---|---|
| mecha 启动 | 选择 profile，启动 HTTP Server，拉起 Coordinator |
| 首次 `ask <role>` | 创建 agent，Spawn 到新 pane，等待 SessionStart 后注入任务 |
| 再次 `ask <role>` | 复用已有 agent，注入新任务 |
| 任务完成 | agent 回到 running 状态，等待下一个任务 |
| Coordinator 退出 | 级联 Kill 所有 Specialist pane，取消等待的 Ask，关闭 HTTP Server |

### 2.3 约束

- 1 role = 1 个活跃实例，同一 role 不并发
- role 目录在 `Prepare()` 时生成
- 每个 profile 必须且只能有一个 `is_coordinator: true` 的 role

---

## 3. 配置

文件: `~/.mecha/config.yaml`

```yaml
agent: claude-sonnet-4-6          # 默认 agent

agents:
  - name: claude-sonnet-4-6
    type: claude
    model: claude-sonnet-4-6
    envs:
      CLAUDE_CODE_MAX_OUTPUT_TOKENS: "8192"

  - name: claude-opus-4-8
    type: claude
    model: claude-opus-4-8
    envs:
      CLAUDE_CODE_MAX_OUTPUT_TOKENS: "16384"

profile: softwarecompany

profiles:
  softwarecompany:
    roles:
      - name: lead
        is_coordinator: true
        prompt: |
          你是运行在 mecha 里的 Lead...
        agent:
          name: claude-sonnet-4-6

      - name: architect
        prompt: |
          你是一个系统架构师...
        agent:
          name: claude-opus-4-8

      - name: coder
        prompt: |
          你是一个开发者...
        agent:
          name: claude-sonnet-4-6

      - name: tester
        prompt: |
          你是一个测试工程师...
        agent:
          name: claude-sonnet-4-6

      - name: reviewer
        prompt: |
          你是一个代码审查者...
        agent:
          name: claude-opus-4-8
```

**配置解析规则：**
- `agent` 指向默认 agent，role 可通过 `agent.name` 覆盖
- `config.complete()` 做三件事：trim whitespace、解析 role 级别 agent 覆盖（Type/Model/Params/Envs 合并到 base agent）、写回 map
- `config.validate()` 检查：agent name 非空且唯一、默认 agent 存在、每个 role 的 agent 可解析、每个 profile 恰好一个 Coordinator
- 默认配置通过 `//go:embed config.yaml` 嵌入二进制，`mecha init` 写出到 `~/.mecha/config.yaml`

### 3.1 配置结构体

```go
type Config struct {
    Agent    string                    `yaml:"agent"`
    Agents   []AgentConfig             `yaml:"agents"`
    Profile  string                    `yaml:"profile"`
    Profiles map[string]ProfileConfig  `yaml:"profiles"`
}

type ProfileConfig struct {
    Roles []Role `yaml:"roles"`
}

type Role struct {
    Name          string      `yaml:"name"`
    Prompt        string      `yaml:"prompt"`
    IsCoordinator bool        `yaml:"is_coordinator,omitempty"`
    Agent         AgentConfig `yaml:"agent"`
}

type AgentConfig struct {
    Name   string            `yaml:"name,omitempty"`
    Type   string            `yaml:"type"`
    Binary string            `yaml:"binary,omitempty"`
    Model  string            `yaml:"model"`
    Params map[string]any    `yaml:"params"`
    Envs   map[string]string `yaml:"envs"`
}
```

### 3.2 Runtime

```go
type Runtime struct {
    MechaBinary string  // mecha 二进制路径（默认 "mecha"，可通过 ldflags 覆盖）
    WebhookPort string  // HTTP 服务器端口，bind 后确定
}
```

`Runtime` 在启动时构造，通过 agent 工厂和 CLI 命令显式传递，避免包间隐式耦合。

---

## 4. 启动与退出

### 4.1 启动流程

```
mecha run
  │
  ├── core.New(workspace, cfg)
  │   ├── term.New()                  # 自动检测终端 (tmux > iTerm2 > Ghostty)
  │   └── initLogger()                # ~/.mecha/logs/<路径下划线分隔>/YYYY-MM-DD.log
  │
  └── c.Start(ctx)
      ├── 1. 找 coordinator role
      ├── 2. 创建 coordinator agent, Prepare()
      ├── 3. 绑定 TCP listener (127.0.0.1:0)，端口记入 runtime.WebhookPort
      ├── 4. startHTTPServer()：注册 POST /webhook/ 和 POST /ask
      ├── 5. launchCoordinator()：子进程接管终端，SIGINT 透传
      └── 6. Cleanup (coordinator 退出后)
             ├── srv.Shutdown()
             ├── 级联 Kill 所有 Specialist pane
             └── 取消所有等待的 Ask
```

### 4.2 生成物

```
<workspace>/.mecha/roles/<role-name>/
├── CLAUDE.md                          # role prompt，通过 --append-system-prompt-file 注入
└── settings.json                      # hook 配置，通过 --settings 合并到全局配置
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
    "Stop": [{
      "hooks": [{
        "type": "command",
        "command": "mecha",
        "args": ["webhook", "--id", "<uuid>", "--port", "<端口>"]
      }]
    }],
    "StopFailure": [{
      "hooks": [{
        "type": "command",
        "command": "mecha",
        "args": ["webhook", "--id", "<uuid>", "--port", "<端口>"]
      }]
    }]
  }
}
```

每个 hook 触发时执行 `mecha webhook --id <agentID> --port <port>`，hook JSON 通过 stdin 传入。

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
            │   ├── createAgent()       # agent.New + Prepare
            │   ├── backend.Spawn()     # 新 pane, status = StatusStarting
            │   └── 等待 SessionStart   # 30s 超时 → 失败
            │
            ├── status = StatusBusy
            ├── backend.Send(task + "\n")
            ├── 等待 result (Stop → output; StopFailure → error)
            │   └── 超时 30min → Kill Specialist + 清理
            ├── status = StatusRunning
            └── return result
```

- `mecha ask` 同步阻塞，stdout 直接输出 agent 返回内容，exit 0
- 失败时 stderr 输出错误信息，exit 1

### 5.2 Agent 状态机

```
                              SessionStart
Spawn ──► StatusStarting ────────────────────► StatusRunning
                 │                                  │
                 │ 30s 超时 → Kill + 失败            │ Ask 下发任务
                 │                                  ▼
                 │                             StatusBusy
                 │                                  │
                 │                     ┌────────────┤
                 │                     ▼            ▼
                 │                Stop (完成)   StopFailure (失败)
                 │                     │            │
                 │                     └─────┬──────┘
                 │                          ▼
                 │                     StatusRunning
                 │
                 └──► 失败 (返回 error 给 Ask)
```

| 状态 | 常量 | 含义 |
|---|---|---|
| starting | `StatusStarting` | agent 已 spawn，等待 SessionStart |
| running | `StatusRunning` | agent 就绪，可接收任务 |
| busy | `StatusBusy` | 正在执行任务，等待 Stop 或 StopFailure |

### 5.3 关键数据结构

```go
type Core struct {
    workspace string
    cfg       config.Config
    runtime   config.Runtime
    backend   term.Backend

    coordinator       types.Agent
    specialists       map[string]*instance
    agentByID         map[string]types.Agent
    instanceByAgentID map[string]*instance

    logger  *slog.Logger
    logFile *os.File
}

type instance struct {
    role   string
    agent  types.Agent
    handle term.Handle
    status string           // StatusStarting | StatusRunning | StatusBusy
    ready  chan struct{}    // 当 SessionStart 到达时 close
    result chan taskResult  // 每次任务的完成信号
}

type taskResult struct {
    output string
    err    string
}
```

### 5.4 Coordinator 与 Specialist 对比

| | Coordinator | Specialist |
|---|---|---|
| 启动方式 | `cmd.Start()` 子进程 | `backend.Spawn()` pane |
| stdin/stdout | 直连终端 | 终端 pane |
| 有 Handle | ❌ | ✅ |
| 可接收任务 | ❌ | ✅ |
| 可调度子 agent | ✅ (prompt 会输出 available_roles) | ❌ |
| 有 agent 状态机 | ❌ (只观测进程退出) | ✅ |

---

## 6. 通信

### 6.1 Terminal Backend 接口

```go
type Backend interface {
    Spawn(ctx context.Context, spec Spec) (Handle, error)
    Send(ctx context.Context, handle Handle, text string) error
    Capture(ctx context.Context, handle Handle) (string, error)
    CaptureAll(ctx context.Context, handle Handle) (string, error)
    Kill(ctx context.Context, handle Handle) error
}

type Spec struct {
    WorkDir string
    Command []string
    Env     map[string]string
}

type Handle interface {
    ID() string
    PaneID() string
}
```

**后端实现：**

| 后端 | 平台 | 通信方式 | 备注 |
|---|---|---|---|
| tmux | macOS/Linux | `exec.Command` 调用 tmux CLI | 无需额外配置 |
| iTerm2 | macOS | WebSocket (protobuf) | 需开启 Preferences → General → Magic → Enable Python API |
| Ghostty | macOS | AppleScript (`osascript`) | 无需额外配置 |

**后端选择优先级：** tmux → iTerm2 → Ghostty，取第一个匹配环境的。

**Pane 管理：** backend 内部用 `driver.Chain` 维护 pane 顺序。第一个 Spawn 做垂直分割（右侧），后续做水平分割（下方）。

**Send 机制：**
- tmux：逐行 `send-keys -l`，行间 `send-keys C-m C-j`（发送 `\r\n`）
- iTerm2：`SendTextRequest` (protobuf WebSocket)，将 `\n` 替换为 `\r\n` 发送
- Ghostty：AppleScript `input text` + `send key "enter"`，用 `ScriptMultiline` 处理

**Spawn 引导命令：** `BuildBootstrap(Spec)` 生成 `cd <WorkDir> && exec <command>`，其中 command 由 `BuildCommand` 用单引号安全拼接。

### 6.2 CLI 命令

| 命令 | 调用方 | 用途 |
|---|---|---|
| `mecha` | 用户 | 等同于 `mecha run` |
| `mecha run` | 用户 | 启动 mecha，加载配置，选择 profile |
| `mecha init [-f]` | 用户 | 初始化 `~/.mecha/config.yaml` |
| `mecha ask --port <P> <role> "<task>"` | Coordinator | 派发任务 (阻塞等结果) |
| `mecha webhook --id <uuid> --port <P>` | agent hook | 从 stdin 读取 Hook JSON 转发给 mecha |
| `mecha version` | 用户 | 输出版本、构建日期和 Go 运行时信息 |

### 6.3 Hook 回调

agent 原生 Hook handler 触发时调用 `mecha webhook`，Hook JSON 通过 stdin 传入，`mecha webhook` POST 到 HTTP Server。

**注册的事件:**
| 事件 | 触发时机 |
|---|---|
| `SessionStart` | agent 启动完成 |
| `Stop` | 任务成功完成 |
| `StopFailure` | 任务执行失败 |

**事件流:**

```
Agent 触发 hook
  └── 执行: mecha webhook --id <uuid> --port <P>
       └── stdin: {"hook_event_name":"Stop", ...}
       └── POST /webhook/<uuid>
            │
            └── Core.handleWebhook()
                 ├── 从 URL path 提取 agentID
                 ├── agentByID[agentID].ParseHookEvent(body)
                 └── onEvent(agentID, event)
```

**事件处理:**

| 事件 | 状态条件 | 处理 |
|---|---|---|
| `SessionStart` | status == StatusStarting | close(inst.ready) → 状态变为 StatusRunning |
| `Stop` | status == StatusBusy | inst.result ← taskResult{output} |
| `StopFailure` | status == StatusBusy | inst.result ← taskResult{err} |

**统一事件结构:**

```go
type HookEvent struct {
    AgentID      string          `json:"agent_id"`
    Event        string          `json:"event"`
    SessionID    string          `json:"session_id,omitempty"`
    Output       string          `json:"output,omitempty"`
    OutputSource string          `json:"output_source,omitempty"`
    Error        string          `json:"error,omitempty"`
    Raw          json.RawMessage `json:"raw,omitempty"`
}
```

事件名映射（`claude/event.go`）：

```go
var eventMap = map[string]string{
    "SessionStart":  EventSessionStart,
    "PostToolBatch": EventPostToolBatch,
    "Stop":          EventStop,
    "StopFailure":   EventStopFailure,
}
```

解析逻辑从 JSON 中提取 `session_id`、`last_assistant_message` 和 `error_type`。

### 6.4 Agent 接口

```go
type AgentContext struct {
    Workspace string  // 项目根目录 (cmd.Dir)
    RoleDir   string  // agent 专属文件目录（CLAUDE.md、settings.json）
    Prompt    string  // role prompt，通过 --append-system-prompt-file 注入
    AgentID   string  // agent 唯一标识 UUID
}

type Agent interface {
    Prepare() error
    Cmd() *exec.Cmd
    ParseHookEvent(raw []byte) (HookEvent, error)
    ID() string
}

type Factory func(ctx AgentContext, cfg config.AgentConfig, runtime config.Runtime) (Agent, error)
```

不同 agent 类型通过 `registry` map 注册（`agent.go init()` 注册 `"claude"` → `claude.New`），各自实现 `ParseHookEvent`。

### 6.5 Claude Agent 实现细节

**Prepare() 步骤：**
1. 创建 `<roleDir>/` 目录
2. 写入 `CLAUDE.md`（rendered prompt）
3. 写入 `settings.json`（hooks 配置）

**Cmd() 构建逻辑：**
- `--model <model>`
- `--settings <roleDir>/settings.json` 合并 hook 配置到全局 settings
- `--append-system-prompt-file <roleDir>/CLAUDE.md` 将 role prompt 追加到 system prompt
- 合并 user params 和 `defaultParams`（`dangerously-skip-permissions: true`），按字母序输出
- 工作目录 = `<workspace>`（项目根目录，agent 无需额外告知工作路径）
- 合并 user envs 和 `defaultEnvs`（`BASH_DEFAULT_TIMEOUT_MS: 1200000`）
- 如果 `AgentConfig.Binary` 非空则使用指定 binary，否则默认 `claude`

**配置隔离策略：**
- `--settings` 将 agent hook 配置**叠加合并**到用户全局 `~/.claude/settings.json`，未指定的 key 保留原值
- `--append-system-prompt-file` 将 role prompt 追加到 system prompt 末尾
- 不设置 `CLAUDE_CONFIG_DIR`，全局 `~/.claude/`（MCP servers、credentials 等）完整保留
- agent 工作目录为 workspace，项目的 `CLAUDE.md` 和 `.claude/` 配置正常加载

---

## 7. 代码结构

```
main.go                          # 入口，调用 cmd.NewRootCmd().Execute()

cmd/
  root.go                        # 根 cobra 命令，注册子命令
  run.go                         # run 子命令 + runMecha() 启动逻辑
  init.go                        # init 子命令，输出默认 config.yaml
  ask.go                         # ask 子命令，POST /ask 并等待结果
  webhook.go                     # webhook 子命令，从 stdin 读取并 POST
  version.go                     # version 子命令

pkg/
  config/
    config.go                    # Config/Role/AgentConfig/Runtime 结构体，
                                 # LoadConfig/InitConfig 函数，
                                 # validate/complete/findAgent 逻辑
    config.yaml                  # 嵌入的默认配置 (//go:embed)

  agent/
    agent.go                     # registry map, Register/New 函数,
                                 # prompt 模板渲染 (firstLine)
    types/
      types.go                   # Agent 接口, Factory 类型,
                                 # HookEvent 结构体, 事件常量

    claude/
      claude.go                  # Claude struct, New/ID/Prepare/Cmd
      event.go                   # ParseHookEvent, eventMap

  core/
    app.go                       # Core struct, New/Start/Close,
                                 # 状态常量 StatusStarting/StatusRunning/StatusBusy,
                                 # instance/taskResult 类型
    coordinator.go               # launchCoordinator，子进程管理, 退出清理 (Coordinator 进程)
    agent.go                     # Ask, createAgent, ensureSpecialist,
                                 # cleanupSpecialist, 超时常量
    server.go                    # HTTP server, /webhook/ 和 /ask 路由,
                                 # handleWebhook/handleAsk/onEvent
    logger.go                    # initLogger, workspace 哈希

  term/
    term.go                      # Backend/Spec/Handle 类型别名,
                                 # New() 工厂函数, 后端匹配优先级
    driver/
      driver.go                  # Backend/Handle/Spec 接口定义,
                                 # Chain 类型, BuildBootstrap/BuildCommand/
                                 # QuoteShell/ScriptMultiline 工具函数

    tmux/
      tmux.go                    # Tmux struct, New/Name/Match/Spawn/Send/
                                 # Capture/CaptureAll/Kill
      cmd.go                     # tmux CLI wrapper: currentPane, splitRight,
                                 # splitDown, sendLiteral, sendEnter,
                                 # sendMultiline, captureArgs

    iterm2/
      iterm2.go                  # ITerm2 struct, New/Name/Match/Spawn/Send/
                                 # Capture/CaptureAll/Kill
      ws.go                      # WebSocket 连接: dial (含 AppleScript cookie
                                 # 认证), conn.call, splitSession, sendText,
                                 # getBuffer, closeSessions
      ws_test.go                 # sendText protobuf 序列化测试
      api/
        api.pb.go                # protoc 生成的 iTerm2 API (protobuf)

    ghostty/
      ghostty.go                 # Ghostty struct, New/Name/Match/Spawn/Send/
                                 # Capture/CaptureAll/Kill
      script.go                  # AppleScript 生成: firstSpawnScript,
                                 # splitSpawnScript, textScript, closeScript,
                                 # actionScript, runAppleScript

docs/
  DESIGN.md                      # 本文档
```

---

## 8. 日志

```
~/.mecha/logs/<workspace-path>/YYYY-MM-DD.log
```

- `workspace-path` = workspace 绝对路径去头 `/`、`/` 替换为 `_`（例如 `/Users/me/project` → `Users_me_project`），多 workspace 自动隔离
- `slog.TextHandler`，输出 `key=value` 格式，带文件名和行号
- 按天追加

---

## 9. 设计决策

| 决策 | 结论 |
|---|---|
| `mecha ask` 行为 | 同步阻塞，stdout 输出结果 / exit 0，stderr 输出错误 / exit 1 |
| ask 输出格式 | 直接输出 agent 返回内容，不包装 |
| 并发策略 | 同一 role 一次一个任务，不并发 |
| SessionStart 处理 | 首次: StatusStarting → StatusRunning；后续: 状态不变 |
| Coordinator 退出 | 级联 Kill Specialist pane + 取消等待的 Ask |
| Hook 事件 | 只注册 SessionStart / Stop / StopFailure（不含 PostToolBatch） |
| pane 分割策略 | 首次垂直（右侧），后续水平（下方） |
| 后端优先级 | tmux > iTerm2 > Ghostty |
| send 行终止符 | `\r\n`（CR+LF），确保终端正确显示和命令执行 |
| Prepare trust dialog | 通过 `dangerously-skip-permissions` 跳过，无需 `--add-dir`（agent 在 workspace 工作，roleDir 仅通过 `--settings` / `--append-system-prompt-file` 读取配置） |
| Coordinator 特殊化 | 启动方式不同（子进程 vs pane），未统一模型 |
| 任务超时 | 30 分钟，超时 Kill Specialist + 返回错误 |
| agent 启动超时 | 30 秒等待 SessionStart |
| iTerm2 连接 | 启动时立即 dial WebSocket（非 lazy），结束时自动 close |

---

## 10. 遗留项

| 遗留项 | 说明 |
|---|---|
| agent 崩溃感知 | pane 进程异常退出无感知，Ask 会阻塞到超时 |
| Coordinator 统一模型 | 启动方式与 Specialist 不同（子进程 vs pane） |
| session_id 持久化 | 仅存内存，重启后丢失 |
| backend.Send 可靠性 | 文本注入 pane 后无送达确认，pane 死亡时可能静默丢失 |
| resume 支持 | session_id 已获取，agent 会话恢复机制未接入 |
| mecha 进程崩溃 | 被 SIGKILL 后 Coordinator 和 Specialist pane 残留 |
| 日志轮转 | 按天追加，无上限 |
| PostToolBatch 事件 | eventMap 中已定义，但 settings.json 中未注册 hook |
