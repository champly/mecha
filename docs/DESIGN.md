# Mecha 设计文档

> mecha 是一个多 agent 编排系统。用户直接与 Coordinator 交互，Coordinator 通过 `mecha ask` 同步派发任务给 Specialist。每个 role 由一个 agentd 进程托管：agentd 拉起并常驻 agent 进程（PTY），通过 gRPC 与 Core 通信，agent 的 Hook 事件经本地 HTTP 回传给 agentd。Specialist agentd 运行在独立终端 pane 中，用户可直接围观。

---

## 1. 概览

### 1.1 解决的痛点

| 痛点 | 解法 |
|---|---|
| 单个 agent 上下文堆叠 | 每个 role agent 独立会话，职责边界清晰 |
| 角色专业度被稀释 | 每个 role 有独立 prompt、独立目录、独立 session |
| 长任务执行不可见 | specialist 在终端 pane 里运行，用户可直接围观 |
| 黑盒不可追溯 | 关键流程通过 Hook 结构化事件驱动状态机 |

### 1.2 术语

| 名词 | 含义 |
|---|---|
| mecha | 唯一的编排进程（Core），负责加载配置、启动/回收 agentd |
| Core | mecha 进程内的编排核心，暴露 gRPC 服务，管理所有 agentd 实例 |
| Coordinator | 用户默认交互角色，承担入口与派活职责 |
| Specialist | 被 Coordinator 调度的角色，处理具体任务 |
| agentd | agent 守护进程，托管一个 agent 进程（PTY），通过 gRPC 与 Core 通信；coordinator 和 specialist 是同一个 agentd 二进制，仅启动方式与用途不同 |
| role agent | 跑在 agentd PTY 里的 agent CLI 进程（Claude Code / Codex / Gemini） |
| role | agent 的职责定义，由 profile 中的角色配置描述 |
| profile | 一组角色集合，启动时选择 |
| id | agentd 实例 ID（UUID），由 Core 拉起 agentd 时分配，生命周期内不变；也是 gRPC 协议中的唯一标识 |
| session_id | agent CLI 自身的会话 ID，由 SessionStart 事件获取 |
| pane | 终端面板（tmux / iTerm2 / Ghostty），承载一个 specialist agentd |

---

## 2. 架构

### 2.1 进程模型

```
mecha run
  │
  └─ Core (gRPC 127.0.0.1:<随机端口>)
      │  api.Core 服务:
      │    ├── Register      ← agentd 注册，换取 agent 配置
      │    ├── ReportStatus  ← agentd 上报 started/exited
      │    ├── Ask           ← mecha ask CLI（阻塞等结果）
      │    └── TaskChannel   ← agentd 双向流，下发任务/回传结果
      │
      ├─ agentd (coordinator role) ── 前台子进程，接管当前终端
      │   ├─ gRPC Register + TaskChannel → Core
      │   ├─ HTTP 127.0.0.1:<随机端口> POST /webhook ← agent hook
      │   └─ agent 进程 (PTY)：只发 mecha ask，不接收任务
      │
      └─ agentd (specialist role) ─── 终端 pane（tmux / iTerm2 / Ghostty）
          ├─ gRPC Register + TaskChannel → Core
          ├─ HTTP 127.0.0.1:<随机端口> POST /webhook ← agent hook
          └─ agent 进程 (PTY)：只接收任务，不能派发
```

- **Coordinator agentd** 是 Core 的 `exec.Command` 前台子进程，stdin/stdout/stderr 直通当前终端
- **Specialist agentd** 通过终端后端 `Spawn()` 在独立 pane 中启动
- agentd 功能完全对等：coordinator 与 specialist 的区别仅在于使用方式（一个只发 `mecha ask`，一个只接任务）
- agent 的 hook 目标是 agentd 的本地 HTTP 端口，agent 不感知 Core
- role agent 之间不直接通信，产物落在 workspace 文件系统中

### 2.2 生命周期

| 触发 | 动作 |
|---|---|
| mecha 启动 | 加载配置，Core 绑定 gRPC 监听，拉起 coordinator agentd（前台） |
| agentd 启动 | 起本地 webhook HTTP → gRPC 连接 Core → Register 换取配置 → 建立 TaskChannel → 启动 agent（PTY） |
| 首次 `ask <role>` | Spawn 新 pane 启动 specialist agentd，等待 Register（5s 超时）与 started（30s 超时） |
| 再次 `ask <role>` | 复用已有健康实例，经 TaskChannel 下发任务 |
| 任务完成 | agent 触发 Stop/StopFailure hook → agentd 回传 TaskResult → 实例回到 running |
| agent 退出 | agentd 上报 exited，Core 标记实例 unhealthy，不自动重启 |
| 下一次 `ask <role>` 且 unhealthy | 清理旧 pane，重建新 pane + 新 agentd + 新 agent |
| Coordinator 退出 | 级联 Kill 所有 specialist pane，gRPC server 优雅停止（5s 超时强制 Stop） |

### 2.3 约束

- 1 role = 1 个活跃实例，同一 role 任务串行（Core 侧 per-instance `taskMu`）
- role 目录在 agent `Prepare()` 时生成
- 每个 profile 必须且只能有一个 `is_coordinator: true` 的 role
- 协议中只有 `id`（agentd 实例 ID）：Register 只携带 id，role 由 Core 的实例表反查；TaskChannel 通过 gRPC metadata `instance-id` 标识连接

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

      - name: coder
        prompt: |
          你是一个开发者...
        agent:
          name: claude-sonnet-4-6
```

**配置解析规则：**
- `agent` 指向默认 agent，role 可通过 `agent.name` 覆盖
- `config.complete()` 做三件事：trim whitespace、解析 role 级别 agent 覆盖（Type/Binary/Model/Params/Envs 合并到 base agent）、写回 map
- `config.validate()` 检查：agent name 非空且唯一、agent type 已注册、默认 agent 存在、每个 role 的 agent 可解析、每个 profile 恰好一个 Coordinator
- 默认配置通过 `//go:embed config.yaml` 嵌入二进制，`mecha init` 写出到 `~/.mecha/config.yaml`（已存在时备份为 `.bak`，`-f` 直接覆盖）

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
    Addr        string  // Core gRPC 监听地址（host:port）
}
```

`Runtime` 在启动时构造，用于渲染 role prompt（coordinator 的 `<available_roles>` 块需要 `--addr`）。

---

## 4. 启动与退出

### 4.1 启动流程

```
mecha run
  │
  ├── config.LoadConfig("")            # ~/.mecha/config.yaml
  ├── core.New(workspace, cfg)
  │   ├── term.New()                   # 自动检测终端 (tmux > iTerm2 > Ghostty)
  │   └── initLogger()                 # ~/.mecha/logs/<路径下划线分隔>/YYYY-MM-DD.log
  └── c.Start(ctx)
      ├── 1. 绑定 TCP listener (127.0.0.1:0)
      ├── 2. 注册 gRPC api.Core 服务并 Serve
      ├── 3. launchCoordinator()：
      │   ├── 找 coordinator role，分配 id，登记实例表
      │   ├── exec.Command(mecha agentd --id <id> --addr <addr>) 前台启动
      │   ├── waitRegistered (5s) + waitReady (30s)
      │   └── cmd.Wait() 阻塞直到 coordinator 退出
      └── 4. shutdown()（coordinator 退出后）
             ├── 级联 Kill 所有 specialist pane（每个 5s 超时）
             └── GracefulStop（5s 超时后强制 Stop）
```

agentd 启动流程（`mecha agentd --id <id> --addr <addr>`）：

```
1. 启动本地 webhook HTTP server (127.0.0.1:0)
2. gRPC 连接 Core，Register(id) 换取 workspace/prompt/role/agent 配置/mechaBinary
3. 建立 TaskChannel 双向流（metadata 携带 instance-id）
   —— 先于 agent 启动建立，保证 Core 判定 ready 后即可下发任务
4. startAgent：agent.NewFromConfig → Prepare()（写 role 目录）→ PTY 启动
   ├── io.Copy: PTY ↔ agentd stdio 双向转发
   ├── watchWinch: SIGWINCH → 调整 PTY 尺寸
   └── waitAgent: 等退出 → 失败在途任务 → 关 PTY → close(stop)
5. hookLoop（消费 webhook 事件）+ supervise（退出后上报 exited 并清理）
```

### 4.2 生成物

```
<workspace>/.mecha/roles/<role-name>/
├── CLAUDE.md                          # role prompt，通过 --append-system-prompt-file 注入
└── settings.json                      # hook 配置，通过 --settings 合并到全局配置
```

（Codex 为 `AGENTS.md` + `--config` 注入；Gemini 为 `GEMINI.md` + `.gemini/settings.json`。）

### 4.3 settings.json

```json
{
  "hooks": {
    "SessionStart": [{
      "hooks": [{
        "type": "command",
        "command": "mecha",
        "args": ["webhook", "--addr", "<agentd-addr>"]
      }]
    }],
    "Stop": [{
      "hooks": [{
        "type": "command",
        "command": "mecha",
        "args": ["webhook", "--addr", "<agentd-addr>"]
      }]
    }],
    "StopFailure": [{
      "hooks": [{
        "type": "command",
        "command": "mecha",
        "args": ["webhook", "--addr", "<agentd-addr>"]
      }]
    }]
  }
}
```

每个 hook 触发时执行 `mecha webhook --addr <agentd-addr>`，hook JSON 通过 stdin 传入，再由 `mecha webhook` POST 到 agentd 的 `http://<agentd-addr>/webhook`。

### 4.4 CLAUDE.md

Coordinator 的 prompt 额外包含 `<available_roles>` 块（由 Core 在 Register 时用 `agent.RenderPrompt` 渲染）：

```
<available_roles>
You can delegate tasks by running:
	mecha ask --addr <ADDR> <role> "<task>"

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
coordinator 执行: mecha ask --addr <ADDR> <role> "<task>"
  │
  └── gRPC Core.Ask(role, task)  (阻塞)
       │
       └── grpcService.Ask → ensureSpecialist(role) → inst.execute(ctx, task)
            │
            ├── ensureSpecialist(role)
            │   ├── 未知 role / coordinator role → 直接拒绝，不 spawn
            │   ├── 已有健康实例 → 复用
            │   ├── unhealthy → 销毁旧实例（Kill pane），重建
            │   └── 不存在 → backend.Spawn() 新 pane 启动 agentd
            │       ├── 等待 register (5s 超时 → 销毁失败)
            │       └── 等待 ready = 任务流挂载 + started (30s 超时 → 销毁失败)
            │
            └── inst.execute(task)
                ├── taskMu 加锁（同 role 任务串行）
                ├── status = busy
                ├── TaskChannel 下发 TaskRequest{id, task}
                │   └── agentd: 写入 agent PTY (task + "\r")，等待 hook
                ├── 等待 TaskResult（Stop → success+output; StopFailure → error）
                │   ├── agentd 断连 → 立即返回错误
                │   ├── ctx 取消 → 返回 ctx.Err()
                │   └── 超时 30min → 返回错误
                ├── status = running
                └── return AskResponse
```

- `mecha ask` 同步阻塞，成功时 stdout 直接输出 agent 返回内容，exit 0
- 失败时 stderr 输出错误信息，exit 1

### 5.2 Agent 状态机

```
                                   register + started
                    Spawn agentd ──► starting ─────────────────────────► running
                      │                │                               │
                      │                │ 5s 注册超时 / 30s 启动超时     │ Ask 下发任务
                      │                ▼                               ▼
                      │           启动失败并清理                      busy
                      │                                                │
                      │                              ┌─────────────────┼──────────┐
                      │                              ▼                 ▼          ▼
                      │                         Stop (完成)      StopFailure   agent 退出
                      │                              │              (失败)        │
                      │                              └──────┬──────────┘          │
                      │                                     ▼                     ▼
                      │                                  running             unhealthy
                      │                                                                 │
                      └───────────────────────────────────────────────── 下次 ask 时重建
```

| 状态 | 含义 |
|---|---|
| starting | agentd 已 spawn，等待注册和 agent 启动完成 |
| running | agentd 和 agent 就绪，可接收任务 |
| busy | 正在执行任务，等待 Stop / StopFailure / agent 退出 |
| unhealthy | agent 已退出，当前 pane 禁止接任务，下次 ask 重建 |

### 5.3 关键数据结构

```go
type Core struct {
    cfg         config.Config
    workspace   string
    logger      *slog.Logger
    logFile     *os.File
    mechaBinary string

    backend  term.Backend
    registry *registry    // id/role 双索引，内部加锁
    spawnMu  sync.Mutex   // 串行化 specialist 的查找与重建

    addr   string
    server *grpc.Server
}

type registry struct {
    byID   map[string]*instance
    byRole map[string]*instance
}

type instance struct {
    id     string
    role   string
    handle term.Handle   // specialist pane；coordinator 为 nil

    state  atomic.Int32  // starting/running/busy/unhealthy
    taskMu sync.Mutex    // 任务串行

    stream      grpc.BidiStreamingServer[api.TaskResult, api.TaskRequest]
    resultCh    chan *api.AskResponse
    streamUp    bool           // 任务流已挂载
    agentUp     bool           // started 已上报
    registerCh  chan struct{}  // Register 到达后 close
    readyCh     chan struct{}  // streamUp && agentUp 后 close（与到达顺序无关）
}
```

### 5.4 Coordinator 与 Specialist 对比

| | Coordinator | Specialist |
|---|---|---|
| 二进制 | 同一个 `mecha agentd` | 同一个 `mecha agentd` |
| 启动方式 | `exec.Command` 前台子进程 | `backend.Spawn()` 终端 pane |
| stdin/stdout | 直连终端 | 终端 pane |
| 有 term.Handle | ❌ | ✅ |
| 可接收任务 | ❌（TaskChannel 建立但不下发） | ✅ |
| 可调度子 agent | ✅（prompt 含 available_roles） | ❌ |

---

## 6. 通信

### 6.1 Core gRPC 协议（`pkg/api/api.proto`）

service `api.Core`，四个方法：

| 方法 | 类型 | 调用方 | 用途 |
|---|---|---|---|
| `Register` | unary | agentd → Core | 注册实例（只携带 id），返回 workspace/prompt/role/agent 配置/mechaBinary |
| `ReportStatus` | unary | agentd → Core | 上报 `started`（SessionStart 后）/ `exited`（agent 退出后） |
| `Ask` | unary | mecha ask → Core | 派发任务，阻塞等待结果 |
| `TaskChannel` | bidi stream | agentd → Core | Core 下发 `TaskRequest`，agentd 回传 `TaskResult`；连接以 metadata `instance-id` 标识 |

**关键消息：**

```proto
message RegisterRequest  { string id = 1; }
message RegisterResponse {
  string workspace = 1;
  string prompt = 2;        // 已渲染（含 available_roles）
  string role_name = 3;
  string mecha_binary = 4;
  AgentConfig agent = 5;    // type/binary/model/params/envs
}
message StatusRequest { string id = 1; string status = 2; string msg = 3; }
message AskRequest    { string role = 1; string task = 2; }
message AskResponse   { string id = 1; bool success = 2; string result = 3; }
message TaskRequest   { string id = 1; string task = 2; }
message TaskResult    { string id = 1; bool success = 2; string result = 3; }
```

**失败策略：**
- agent 退出后不自动重启当前 pane，实例标记 unhealthy
- 若当时有 in-flight 任务：agentd 先回传失败结果；agentd 断连时 Core 也会主动失败等待中的 Ask
- 下一次同 role `ask` 时重建新 pane + 新 agentd + 新 agent

### 6.2 agentd 本地 HTTP

| 路由 | 调用方 | 请求 | 响应 |
|---|---|---|---|
| `POST /webhook` | mecha webhook CLI | 原始 hook JSON（stdin 透传） | 200 OK / 400 |

监听 `127.0.0.1:0`，仅本机可达；agent 不感知 Core 地址。

### 6.3 Hook 事件流

```
Agent 触发 hook (SessionStart / Stop / StopFailure)
  └── 执行: mecha webhook --addr <agentd-addr>     (hook JSON 经 stdin)
       └── POST http://<agentd-addr>/webhook
            └── agentd: agent.ParseHookEvent(raw) → hookCh → handleHook
                 ├── SessionStart → ReportStatus(started) → Core: close(ready)，状态 running
                 ├── Stop         → taskCh ← {success, output} → TaskResult
                 └── StopFailure  → taskCh ← {error} → TaskResult
```

**统一事件结构（`pkg/agent/types`）：**

```go
type HookEvent struct {
    Event        string          `json:"event"`
    SessionID    string          `json:"session_id,omitempty"`
    Output       string          `json:"output,omitempty"`
    OutputSource string          `json:"output_source,omitempty"`
    Error        string          `json:"error,omitempty"`
    Raw          json.RawMessage `json:"raw,omitempty"`
}
```

解析逻辑从 agent 原生 hook JSON 中提取 `session_id`、`last_assistant_message`（Claude/Codex）或 `prompt_response`（Gemini）、`error_type`。

### 6.4 Terminal Backend 接口

```go
type Backend interface {
    Spawn(ctx context.Context, spec Spec) (Handle, error)
    Kill(ctx context.Context, handle Handle) error
}

type Spec struct {
    WorkDir string
    Command []string
    Env     map[string]string
}

type Handle interface {
    ID() string     // mecha 侧展示 ID（如 tmux-1）
    PaneID() string // 后端原生 pane/session/terminal ID
}
```

**后端实现：**

| 后端 | 平台 | 通信方式 | Spawn 引导 | Kill |
|---|---|---|---|---|
| tmux | macOS/Linux | tmux CLI | 新 pane 后 `send-keys -l` + `C-m C-j` | `kill-pane` |
| iTerm2 | macOS | WebSocket (protobuf)，启动即 dial | `SendTextRequest`（`\r\n` 结尾） | `CloseRequest`，pane 全空后断开连接 |
| Ghostty | macOS | AppleScript (`osascript`) | spawn 脚本内 `input text` + enter | AppleScript `close` |

- **后端选择优先级：** tmux → iTerm2 → Ghostty，取第一个匹配环境的
- **Pane 分割策略：** 第一个 Spawn 垂直分割（右侧），后续水平分割（下方）；backend 内部用 `driver.Chain` 维护 pane 顺序
- **引导命令：** `driver.BuildCommand(Spec)` 用 shell 安全引号拼接（含 `env KEY=V` 前缀）
- **iTerm2 前提：** 需开启 Preferences → General → Magic → Enable Python API（WebSocket + AppleScript cookie 认证）

### 6.5 CLI 命令

| 命令 | 调用方 | 用途 |
|---|---|---|
| `mecha` / `mecha run` | 用户 | 启动 Core + coordinator |
| `mecha init [-f]` | 用户 | 写出默认 `~/.mecha/config.yaml`（存在则备份 .bak，-f 覆盖） |
| `mecha ask --addr <ADDR> <role> "<task>"` | Coordinator | gRPC `Core.Ask`，阻塞等结果 |
| `mecha webhook --addr <ADDR>` | agent hook | stdin 读取 hook JSON，POST 到 agentd `/webhook` |
| `mecha agentd --id <id> --addr <ADDR>` | Core | 运行 agentd（用户不直接调用） |
| `mecha version` | 用户 | 输出版本、构建日期和 Go 运行时信息 |

`<ADDR>` 统一为 `host:port`；Core 默认监听 `127.0.0.1:<随机端口>`。

### 6.6 Agent 接口

```go
type AgentContext struct {
    Workspace   string  // 项目根目录 (cmd.Dir)
    RoleDir     string  // agent 专属文件目录（CLAUDE.md、settings.json）
    Prompt      string  // 渲染后的 role prompt
    WebhookAddr string  // agentd 本地 webhook 地址
}

type Agent interface {
    Prepare() error
    Cmd() *exec.Cmd
    ParseHookEvent(raw []byte) (HookEvent, error)
}

type Factory func(ctx AgentContext, cfg config.AgentConfig, runtime config.Runtime) (Agent, error)
```

不同 agent 类型通过 `registry` map 注册（`agent.go init()` 注册 `"claude"` / `"codex"` / `"gemini"`）。agentd 用 `agent.NewFromConfig` 按 Register 响应中的配置构造 agent。

### 6.7 Claude Agent 实现细节

**Prepare() 步骤：**
1. 创建 `<roleDir>/` 目录
2. 写入 `CLAUDE.md`（渲染后的 prompt）
3. 写入 `settings.json`（hooks 配置）

**Cmd() 构建逻辑：**
- `--model <model>`
- `--settings <roleDir>/settings.json` 合并 hook 配置到全局 settings
- `--append-system-prompt-file <roleDir>/CLAUDE.md` 将 role prompt 追加到 system prompt
- 合并 user params 和 `defaultParams`（`dangerously-skip-permissions: true`），按字母序输出
- 工作目录 = `<workspace>`（项目根目录）
- 合并 user envs 和 `defaultEnvs`（`BASH_DEFAULT_TIMEOUT_MS: 1200000`）
- 如果 `AgentConfig.Binary` 非空则使用指定 binary，否则默认 `claude`

**配置隔离策略：**
- `--settings` 将 agent hook 配置**叠加合并**到用户全局 `~/.claude/settings.json`，未指定的 key 保留原值
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
  ask.go                         # ask 子命令，gRPC Core.Ask
  webhook.go                     # webhook 子命令，stdin → POST agentd /webhook
  agentd.go                      # agentd 子命令（Core 拉起，用户不直接调用）
  version.go                     # version 子命令

pkg/
  api/
    api.proto                    # Core gRPC 服务定义
    api.pb.go / api_grpc.pb.go   # protoc 生成
    types.go                     # instance-id metadata、Status 常量、AgentConfig 转换

  config/
    config.go                    # Config/Role/AgentConfig/Runtime 结构体，
                                 # LoadConfig/InitConfig，validate/complete/findAgent
    config.yaml                  # 嵌入的默认配置 (//go:embed)

  agent/
    agent.go                     # registry、NewFromConfig、RenderPrompt（prompt 模板）
    types/
      types.go                   # Agent 接口、Factory、AgentContext、HookEvent、
                                 # 事件常量、MergeMap/BuildArgs 工具
    claude/
      claude.go                  # Claude struct、New/Prepare/Cmd
      event.go                   # ParseHookEvent、eventMap
    codex/                       # 同构（AGENTS.md + --config 注入）
    gemini/                      # 同构（GEMINI.md + .gemini/settings.json）

  agentd/
    agentd.go                    # Agentd struct、Start/supervise/hookLoop/Close
    agent.go                     # startAgent、launchPTY、waitAgent、watchWinch
    task.go                      # connectTaskChannel/taskLoop/handleTask
    hook.go                      # WebhookServer（POST /webhook）

  core/
    core.go                      # Core struct、New/Start、shutdown、role 查询
    coordinator.go               # launchCoordinator：前台子进程（stdio 直通终端）+ 退出清理
    specialist.go                # ensureSpecialist（校验/复用/重建/spawn）、destroy
    instance.go                  # instance 状态机、attach/detach、execute、等待信号
    registry.go                  # registry：id/role 双索引，内部加锁
    server.go                    # grpcService：api.CoreServer 实现，RPC → registry/instance
    logger.go                    # initLogger：~/.mecha/logs/<workspace>/<date>.log

  term/
    term.go                      # Backend/Spec/Handle 类型别名、New() 工厂、匹配优先级
    driver/
      driver.go                  # Backend/Handle/Spec 接口、Chain、
                                 # BuildCommand/QuoteShell 工具
    tmux/                        # tmux CLI 后端
    iterm2/                      # WebSocket (protobuf) 后端
      api/                       # protoc 生成的 iTerm2 API
    ghostty/                     # AppleScript 后端

docs/
  DESIGN.md                      # 本文档
```

---

## 8. 日志

当前使用 `slog.Default()`（stderr 文本输出）。旧架构的落盘日志（`~/.mecha/logs/...`）已随重构移除，见遗留项。

---

## 9. 设计决策

| 决策 | 结论 |
|---|---|
| `mecha ask` 行为 | 同步阻塞，stdout 输出结果 / exit 0，stderr 输出错误 / exit 1 |
| ask 输出格式 | 直接输出 agent 返回内容，不包装 |
| 并发策略 | 同一 role 一次一个任务（per-instance `taskMu`），不并发 |
| 实例标识 | 协议统一使用 agentd 实例 id；TaskChannel 经 gRPC metadata `instance-id` 关联 |
| agentd 模型 | coordinator 与 specialist 同一二进制，仅启动方式不同（前台子进程 vs pane） |
| TaskChannel 建立时机 | agentd 先于 agent 启动建立；Core 侧 ready = 任务流挂载 + SessionStart 双条件，与到达顺序无关 |
| ask 目标校验 | 未知 role / coordinator role 直接拒绝，不 spawn pane |
| 任务下发方式 | TaskChannel 收到任务后写入 agent PTY（task + `\r`） |
| Hook 回调路径 | agent → `mecha webhook` CLI → agentd 本地 HTTP `/webhook`；agent 不感知 Core |
| agent 退出策略 | 不自动重启，标记 unhealthy，下次 ask 重建新 pane |
| 断连处理 | agentd 断连时 Core 立即失败等待中的 Ask（不等 30min 超时） |
| Coordinator 退出 | 级联 Kill specialist pane（每个 5s 超时），gRPC GracefulStop（5s 后强制 Stop） |
| 启动失败清理 | register/ready 超时即清理实例表并 Kill pane |
| Backend 接口 | 只保留 Spawn/Kill；任务不经过 pane 文本注入 |
| pane 分割策略 | 首次垂直（右侧），后续水平（下方） |
| 后端优先级 | tmux > iTerm2 > Ghostty |
| Prepare trust dialog | 通过 `dangerously-skip-permissions` 跳过 |
| 任务超时 | 30 分钟 |
| 注册/启动超时 | register 5s / agent started 30s |
| Hook 事件 | 只注册 SessionStart / Stop / StopFailure（不含 PostToolBatch） |
| iTerm2 连接 | 启动时立即 dial WebSocket（非 lazy），pane 全空后断开 |

---

## 10. 遗留项

| 遗留项 | 说明 |
|---|---|
| 落盘日志 | 旧架构的 `~/.mecha/logs/` 文件日志已移除，当前仅 stderr slog，后续可恢复 |
| session_id 持久化 | 仅存内存（HookEvent 携带），重启后丢失 |
| resume 支持 | session_id 已获取，agent 会话恢复机制未接入 |
| mecha 进程崩溃 | 被 SIGKILL 后 coordinator 和 specialist pane 残留 |
| TaskChannel 断线重连 | agentd 侧 stream 断开后不自动重连，实例只能等重建 |
| PostToolBatch 事件 | eventMap 中已定义，但 settings.json 中未注册 hook |
