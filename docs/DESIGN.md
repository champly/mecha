# Mecha 设计文档

> 本文档根据当前代码实现整理，描述系统**实际**行为而非原始设计意图。
>
> mecha 是一个多 agent 编排系统：用户与 Coordinator 对话，Coordinator 通过 `mecha ask` 把任务同步派发给 Specialist。每个 role 由一个 agentd 进程托管——agentd 用 PTY 拉起常驻 agent CLI，经 gRPC 与 Core 通信；agent 的 Hook 事件经本地 HTTP 回传给 agentd。Specialist agentd 运行在独立终端 pane 中，执行过程可直接围观。

---

## 1. 概览

### 1.1 术语

| 名词 | 含义 |
|---|---|
| mecha | 唯一二进制，包含 Core、agentd、CLI 全部功能 |
| Core | `mecha run` 进程内的编排核心，暴露 gRPC 服务，管理所有 agentd 实例 |
| Coordinator | 用户交互角色；prompt 注入派发指令（available_roles），Core 拒绝向它派发任务 |
| Specialist | 被 Coordinator 调度的角色，处理具体任务 |
| agentd | agent 守护进程，托管一个 agent 进程（PTY），经 gRPC 与 Core 通信；coordinator 和 specialist 是同一个 agentd 二进制，仅启动方式不同 |
| role agent | 跑在 agentd PTY 里的 agent CLI 进程（Claude / Codex / Gemini / CodeBuddy / Pi） |
| role | agent 的职责定义（name + prompt + agent 配置） |
| profile | 一组 role 集合，启动时由 `profile` 字段选择 |
| id | agentd 实例 ID（UUID），由 Core 拉起 agentd 时分配；gRPC 协议中的唯一标识 |
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
      ├─ agentd (coordinator role) ── exec.Command 前台子进程，stdio 直连当前终端
      │   ├─ gRPC Register + TaskChannel → Core
      │   ├─ HTTP 127.0.0.1:<随机端口> POST /webhook ← agent hook
      │   └─ agent 进程 (PTY) ↔ agentd stdio 直通终端
      │
      └─ agentd (specialist role) ─── term.Backend.Spawn() 终端 pane
          ├─ gRPC Register + TaskChannel → Core
          ├─ HTTP 127.0.0.1:<随机端口> POST /webhook ← agent hook
          └─ agent 进程 (PTY) ↔ agentd stdio 直通 pane
```

- Coordinator agentd 是 Core 的前台子进程，stdin/stdout/stderr 直连终端；Core 用 `context.AfterFunc` 保证 ctx 取消时强杀 coordinator
- Specialist agentd 通过终端后端 `Spawn()` 在独立 pane 中启动
- 二者功能对等，区别仅在使用方式（coordinator 只发 `mecha ask`，specialist 只接任务）
- agent 的 hook 目标是 agentd 的本地 HTTP 端口，agent 不感知 Core 的存在（`NewFromConfig` 下发的 Runtime 只含 `MechaBinary`，不含 Core 地址）
- role agent 之间不直接通信，产物落在 workspace 文件系统中

### 2.2 生命周期

| 触发 | 动作 |
|---|---|
| `mecha run` | 加载配置 → 检测终端后端 → 打开日志文件 → 绑定 gRPC（127.0.0.1:0）→ 拉起 coordinator agentd（前台，等 register 5s + ready 30s）→ 阻塞等其退出 |
| agentd 启动 | 本地 webhook HTTP → gRPC 连接 → Register 换取配置 → 建立 TaskChannel（先于 agent 启动）→ Prepare + PTY 启动 agent → hookLoop + supervise |
| 首次 `ask <role>` | Spawn 新 pane 启动 specialist agentd，等 register（5s）+ ready（30s） |
| 再次 `ask <role>` | 复用健康实例，经 TaskChannel 下发任务 |
| 任务完成 | agent 触发 Stop/StopFailure hook → agentd 回传 TaskResult → 实例回到 running |
| 任务超时（30min） | Core 侧返回错误且实例保持 unhealthy；agentd 侧直接 signalStop 自杀 |
| agent 退出 | agentd 失败在途任务、上报 exited；Core 立即失败等待中的 Ask，实例标记 unhealthy |
| 下次 `ask <role>` 且 unhealthy | destroy 旧实例（Kill pane），重建新 pane + 新 agentd + 新 agent |
| Coordinator 退出 | shutdown：Kill 所有 pane → GracefulStop（5s 超时强制 Stop）→ 再兜底 Kill 一遍 → backend.Close |

### 2.3 约束

- 1 role = 1 个活跃实例；同一 role 任务串行（per-instance `taskMu`），不同 role 可并行
- specialist 的查找/重建按 **per-role 锁**串行（`spawnLocks` map），不同 role 的 spawn 互不阻塞
- 每个 profile 必须且只能有一个 `is_coordinator: true` 的 role（config 校验保证）
- 协议中只有 `id`：Register 只携带 id，role 由 Core 实例表反查；TaskChannel 通过 gRPC metadata `instance-id` 标识连接
- Ask 明确拒绝 coordinator role（`FailedPrecondition`）和未知 role（`NotFound`）

---

## 3. 配置

### 3.1 文件与路径

| 路径 | 内容 |
|---|---|
| `~/.mecha/config.yaml` | 全局配置（`mecha init` 写出，`mecha run` 加载，可用 `--config` 覆盖） |
| `~/.mecha/logs/<workspace 扁平化>/<YYYY-MM-DD>.log` | Core 与 agentd 共用日志（追加） |
| `<workspace>/.mecha/roles/<role>/` | role 生成物（prompt 文件、hook settings），项目级 |

日志目录名规则：workspace 路径去掉前导 `/`，其余 `/` 替换为 `_`（如 `/Users/x/proj` → `Users_x_proj`），文件名为本地时区日期。

### 3.2 配置结构

```go
type Config struct {
    Agent    string                    `yaml:"agent"`    // 默认 agent 名
    Agents   []AgentConfig             `yaml:"agents"`
    Profile  string                    `yaml:"profile"`  // 激活的 profile
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
    Type   string            `yaml:"type"`   // claude / codex / gemini / codebuddy / pi
    Binary string            `yaml:"binary,omitempty"`
    Model  string            `yaml:"model"`
    Params map[string]any    `yaml:"params"`
    Envs   map[string]string `yaml:"envs"`
}

type Runtime struct {           // 非 yaml，启动期构造
    MechaBinary string          // mecha 二进制路径
    Addr        string          // Core gRPC 地址（仅用于渲染 coordinator prompt）
}
```

### 3.3 加载、校验与补全

`LoadConfig(path, validType)`：path 为空用默认路径 → yaml 解析 → `validate` → `complete`。

**validate 规则**（对**所有** profile 生效，不只激活的）：

- agent：name 必填且唯一；`validType` 非 nil 时 type 必须在注册表中（`run` 传入 `agent.ValidateAgentType`）
- 默认 agent（`Config.agent`）非空时必须存在于 agents 列表
- `profile` 必填且必须存在于 `profiles`
- role：name 必填、profile 内唯一；agent 引用可解析（`role.agent.name` 为空回落到默认 agent）；role 级 `agent.type` 非空时同样过类型校验
- 每个 profile **恰好一个** coordinator（0 个或多个都报错）

**complete 合并规则**：

- trim 各字段空白；`params` 空值归一为非 nil 空 map
- 每个 role 的 `agent` 被重写为解析后的完整 AgentConfig：以引用的 agent 为 base，role 级非空的 `type`/`binary`/`model` 逐字段覆盖；`params`/`envs` 按 key 合并、**role 赢**（base 的 map 不被污染）

**InitConfig(force)**（`mecha init`）：

- 目标固定 `~/.mecha/config.yaml`，内容来自 `//go:embed` 的默认配置
- 已存在且 `force=false`：旧文件 rename 为 `config.yaml.bak`（只保留一代，旧 .bak 被删除）后写入
- `force=true`：直接覆盖，不备份

### 3.4 mecha 二进制解析

`resolveMechaBinary()`：

1. `config.MechaBinary != "mecha"`（构建时经 ldflags `-X .../config.MechaBinary=<path>` 覆盖）→ 用覆盖值
2. 否则 `os.Executable()`（当前二进制绝对路径）

保证 hook 命令和 agentd 拉起不依赖 PATH。

### 3.5 默认嵌入配置

`agent: claude-sonnet-4-6`，`profile: softwarecompany`。agents 定义 4 个（claude-sonnet-4-6 / claude-opus-4-8 / codebuddy / pi）；profile 含 5 个 role：`lead`（coordinator）、`architect`、`coder`、`tester`、`reviewer`。

---

## 4. 启动与退出

### 4.1 Core 启动（`mecha run`，裸 `mecha` 等同）

```
1. config.LoadConfig(configPath, agent.ValidateAgentType)
2. core.New(workspace, cfg)
   ├── term.New()                # 按 tmux → iTerm2 → Ghostty 顺序检测，全不匹配报 ErrUnsupported
   ├── config.NewFileLogger()    # ~/.mecha/logs/<workspace>/<date>.log
   └── resolveMechaBinary()
3. c.Start(ctx)（ctx 监听 SIGINT/SIGTERM）
   ├── net.Listen("tcp", "127.0.0.1:0")
   ├── grpc.NewServer() + api.RegisterCoreServer，goroutine Serve
   └── launchCoordinator(ctx) 阻塞：
       ├── 取 profile 中第一个 IsCoordinator 的 role；backend.Label(roleName)（仅 Warn）
       ├── newInstance(uuid) + registry.add
       ├── exec.Command(mechaBinary, "agentd", "--id", id, "--addr", addr)
       │   stdio 直连终端；context.AfterFunc(ctx, Process.Kill)
       ├── waitRegistered(5s) + waitReady(30s)，失败则 Kill + Wait 收割后报错
       └── cmd.Wait() 阻塞至退出；ctx 已取消视为正常关停
4. shutdown()
   ├── killAllPanes()            # 第一遍：让任务流断开，GracefulStop 才能返回
   ├── GracefulStop，5s 超时后 server.Stop() 强停
   ├── killAllPanes()            # 第二遍：兜底关停期间新 spawn 的 pane
   ├── backend.Close()
   └── logFile.Close()
```

### 4.2 agentd 启动（`mecha agentd --id <id> --addr <addr>`）

严格顺序，任一步失败 `Close()` 后返回：

```
1. initLogging（与 Core 同一日志文件，失败静默）
2. WebhookServer 起 HTTP（127.0.0.1:0，POST /webhook）
3. grpc.NewClient(CoreAddr, insecure)（lazy dial）+ Register(id)（metadata 带 instance-id）
   ← 返回 workspace / prompt（已渲染）/ role_name / mecha_binary / agent 配置
4. connectTaskChannel()：先于 agent 建立 bidi 流（保证 Core 判 ready 后可立即下发任务），go taskLoop
5. startAgent：
   ├── agent.NewFromConfig → Prepare()（写 role 文件）→ webhook.SetParseFunc(ParseHookEvent)
   ├── launchPTY：取 stdin 窗口尺寸（失败退化 24×80）→ pty.StartWithSize → 固定 sleep 100ms
   ├── makeRawIfTerminal：stdin 是终端则切 raw，退出时恢复
   └── goroutines: io.Copy(ptmx → activityWriter → stdout)、forwardStdin（4KB buf）、
       watchWinch（SIGWINCH 调 PTY 尺寸）、watchReady（TUI 就绪判定）、waitAgent（等退出）
6. go hookLoop + go supervise；Start 返回，CLI 层 d.Wait() 阻塞在 <-stop
```

**退出链**：`waitAgent`（agent 退出）/ `taskLoop`（流断开，视为 Core 已死）/ 任务超时 → `signalStop()` → `supervise` 先 `ReportStatus(exited)` 再 `Close()`。`Close`（closeOnce 保护）：关 gRPC conn → webhook Shutdown（5s）→ logFile → 关 PTY（hangup agent）→ `Process.Kill` 兜底。

### 4.3 生成物与 prompt 渲染

role 目录 `<workspace>/.mecha/roles/<role>/` 的内容因 agent 类型而异（见 §7）。

prompt 由 Core 在 Register 时用 `agent.RenderPrompt` 渲染，模板三段：

1. `<your_assigned_role>`：role 原始 prompt
2. `<working_directory>`：提示真实项目路径
3. `<available_roles>`：**仅 coordinator** 追加，格式：

```
You can delegate tasks by running:
	<mechaBinary> ask --addr <ADDR> <role> "<task>"

Available roles:
- <name>: <prompt 首行>      # 在第一个 \n 或中文句号处截断
```

Specialist 不含此块，不能调度子 agent。模板执行失败时降级返回原始 prompt。

---

## 5. 任务分派（Ask）

### 5.1 Core 侧流程

```
mecha ask --addr <ADDR> <role> "<task>"   (unary，阻塞)
  └── grpcService.Ask
       ├── ensureSpecialist(ctx, role)
       │   ├── findRole 查不到            → NotFound
       │   ├── role 是 coordinator        → FailedPrecondition
       │   ├── 取 per-role 锁
       │   ├── 已有实例且非 unhealthy     → 复用
       │   ├── unhealthy                  → destroy（remove + Kill pane 5s）
       │   └── Spawn(Spec{WorkDir, Command, Role}) 新 pane
       │       ├── waitRegistered (5s)   ─┐
       │       └── waitReady (30s)        ┴─ 任一失败 destroy 并返回错误
       │          ready = stream attached + SessionStart（maybeReady，与到达顺序无关）
       └── inst.execute(ctx, task)
           ├── taskMu 加锁；快照 stream/resultCh/discCh
           ├── 状态 → busy
           ├── 先 drain resultCh 中的陈旧结果（上一个超时/取消任务遗留）
           ├── stream.Send(TaskRequest{uuid, task})
           └── 单一 30min timer 循环 select：
               ├── resultCh：id 不匹配 → 丢弃继续等（不重设 deadline）；匹配 → 返回
               ├── discCh 关闭（agentd 断连 / agent 退出）→ 立即返回 "agentd disconnected during task"
               ├── ctx.Done → 返回 ctx.Err()
               └── timer 到 → 返回 "core: task timeout"，状态保持 unhealthy（不恢复 running）
```

- 结果投递（`deliverResult`）**永不阻塞**：resultCh 容量 1，满时 evict 旧结果再放新结果（满缓冲里只可能是陈旧结果，丢新结果会让 execute 挂满 30min）
- TaskChannel handler：metadata 取 `instance-id`（缺失 InvalidArgument），registry 查不到 NotFound；`attach` + `defer detach`；EOF/Canceled 视为正常拆流
- `detach` 只在 **stream 指针相同**时生效：stale 流拆除不会误伤重连后的新流；生效时置 nil、`closeDisc()`、状态 → unhealthy
- `markExited`（ReportStatus exited）同样 `closeDisc()`：agent 退出时在途任务立即失败，不等 agentd 断连或 30min 超时

### 5.2 agentd 侧任务执行

```
taskLoop: stream.Recv() 出错 → signalStop()（视为 Core 已死，避免孤儿 agent；恢复靠 Core 重建链）
handleTask（串行）:
  1. 等 <-ready（TUI 就绪）或 <-stop（回 "agent exited during task"）
  2. ptmx == nil → "agent not running"
  3. 分两次写 PTY：先写 task 文本，sleep 100ms，再写 "\r"
     （一次写入时 \r 可能先于文本被 TUI 事件循环处理，长任务回车被吞）
  4. select：
     ├── taskCh 收到 hook 结果 → 回 TaskResult{Id, Success, Result}
     ├── stop → "agent exited during task"
     └── 30min 超时 → 日志报错并 signalStop() 自杀（不回结果，让 Core 判 unhealthy 重建）
```

**TUI 就绪闸门（watchReady）**：Core 的 ready（任务流 + SessionStart）在 agent 启动后约 0.3–1s 即达成，但 TUI 尚未完成初始化，此时写入 PTY 会丢回车。因此 agentd 增加第二层判定：

- `activityWriter` 在 PTY → stdout 链路上记录最后输出时间（unix nano）
- 每 50ms 轮询：有过输出且静默超过 **1.5s** → 判定 quiet；再固定等 **500ms** grace period（防初始化步骤间的短暂停顿误判）→ close(ready)
- **30s** 内未 quiet → Warn 日志兜底放行（照样 close ready）
- 任何退出路径都保证 close(ready)（defer）

**hook 处理（handleHook）**：只处理三种事件——

| 事件 | 动作 |
|---|---|
| SessionStart | ReportStatus(started) |
| Stop | 有在途任务 → taskCh ← {success, output}；无任务静默丢弃 |
| StopFailure | 有在途任务 → taskCh ← {failure, error}；无任务静默丢弃 |

`ReportStatus` 每次 RPC 带 **2s deadline**（防 Core 挂死阻塞 hookLoop 进而堵死 agent 的 hook 进程）。

### 5.3 状态机

```
                                 register + started
                  Spawn agentd ──► starting ────────────────────────► running
                    │                │                              │
                    │      5s 注册超时 / 30s ready 超时             │ Ask 下发任务
                    │                ▼                              ▼
                    │         启动失败并清理                       busy
                    │                                               │
                    │            ┌──────────────┬───────────────────┼──────────┐
                    │            ▼              ▼                   ▼          ▼
                    │       Stop (完成)    StopFailure (失败)   agent 退出   任务 30min 超时
                    │            └──────┬──────┘              断连            │
                    │                   ▼                        └─────┬──────┘
                    │                running                            ▼
                    │                                              unhealthy
                    │                                                 │
                    └──────────────────────────────── 下次 ask 时 destroy + 重建
```

| 状态 | 值 | 含义 |
|---|---|---|
| starting | 1 | 已 spawn，等待注册和 agent 启动完成 |
| running | 2 | 就绪，可接收任务 |
| busy | 3 | 任务在途，等待 Stop / StopFailure / 退出 / 超时 |
| unhealthy | 4 | agent 退出 / 断连 / 任务超时，禁止接任务，下次 ask 重建 |

### 5.4 关键结构体

```go
type Core struct {
    cfg         config.Config
    workspace   string
    logger      *slog.Logger
    logFile     *os.File
    mechaBinary string

    backend    term.Backend
    registry   *registry              // id/role 双索引，内部加锁
    spawnMu    sync.Mutex             // 只保护 spawnLocks map 本身
    spawnLocks map[string]*sync.Mutex // per-role spawn 锁

    addr   string
    server *grpc.Server
}

type instance struct {
    id, role string
    state    atomic.Int32
    taskMu   sync.Mutex        // 任务串行

    mu       sync.Mutex        // 保护以下字段
    handle   term.Handle       // specialist pane；coordinator 为 nil
    stream   grpc.BidiStreamingServer[api.TaskResult, api.TaskRequest]
    resultCh chan *api.AskResponse  // 容量 1，attach 时重建
    discCh   chan struct{}          // 断连信号，attach 时重建
    streamUp, agentUp, registered bool
    registerCh, readyCh chan struct{}
    readyClosed bool
}

type Agentd struct {
    opts     Options{ID, CoreAddr}
    client   api.CoreClient
    conn     *grpc.ClientConn
    webhook  *WebhookServer
    hookCh   chan types.HookEvent  // 容量 32

    ptmx     *os.File
    cmd      *exec.Cmd
    taskCh   chan taskResult       // 容量 1
    stop     chan struct{}
    ready    chan struct{}         // TUI 就绪后 close
    hasTask  bool
    lastOutput atomic.Int64
    closeOnce, stopOnce sync.Once
}
```

`registry.remove(inst)` 只删除仍指向该实例的条目——防止旧实例的 destroy 删掉同 role 新实例的索引。

### 5.5 Coordinator 与 Specialist 对比

| | Coordinator | Specialist |
|---|---|---|
| 二进制 | 同一个 `mecha agentd` | 同一个 `mecha agentd` |
| 启动方式 | `exec.Command` 前台子进程 | `backend.Spawn()` 终端 pane |
| stdin/stdout | 直连终端 | 终端 pane |
| 有 term.Handle | ❌ | ✅ |
| 可接收任务 | ❌（Ask 拒绝 coordinator role） | ✅ |
| 可调度子 agent | ✅（prompt 含 available_roles） | ❌ |

---

## 6. 通信

### 6.1 Core gRPC 协议（`pkg/api/api.proto`，proto3）

service `api.Core`，四个方法：

| 方法 | 类型 | 调用方 | 用途 |
|---|---|---|---|
| `Register` | unary | agentd → Core | 注册实例（只携带 id），返回 workspace/prompt/role/agent 配置/mecha_binary |
| `ReportStatus` | unary | agentd → Core | 上报 `started`（SessionStart 后）/ `exited`（agent 退出后） |
| `Ask` | unary | mecha ask → Core | 派发任务，阻塞等待结果 |
| `TaskChannel` | bidi stream | agentd → Core | 下行 `TaskRequest`，上行 `TaskResult`；连接以 metadata `instance-id` 标识 |

**消息定义：**

```proto
message RegisterRequest  { string id = 1; }
message RegisterResponse {
  string workspace = 1;
  string prompt = 2;        // 已渲染（coordinator 含 available_roles）
  string role_name = 3;
  string mecha_binary = 4;
  AgentConfig agent = 5;
}
message AgentConfig {
  string type = 1; string binary = 2; string model = 3;
  google.protobuf.Struct params = 4;   // yaml map[string]any → Struct
  map<string, string> envs = 5;
}
message StatusRequest { string id = 1; string status = 2; string msg = 3; }
message AskRequest    { string role = 1; string task = 2; }
message AskResponse   { string id = 1; bool success = 2; string result = 3; }
message TaskRequest   { string id = 1; string task = 2; }
message TaskResult    { string id = 1; bool success = 2; string result = 3; }
```

**实现细节：**

- 状态常量仅 `started` / `exited`；`msg` 字段目前未被使用
- `ReportStatus` 对未知 id 返回成功（空响应），不报错
- 任务超时 `TaskTimeout = 30min` 为协议常量，Core 与 agentd 两侧共用
- 无 TLS、无鉴权，监听仅 127.0.0.1

### 6.2 agentd 本地 HTTP（webhook）

| 项 | 值 |
|---|---|
| 监听 | `127.0.0.1:0`（随机端口） |
| 路由 | 仅 `POST /webhook`，其他方法 405 |
| body | 原始 hook JSON，上限 1 MiB（超限/读失败 400） |
| parseFn 未设置（agent 未 Prepare 完） | 400 "agent not ready" |
| 解析失败 | 400（body 截断 512 字符记日志） |
| 成功 | 200；server 关闭中阻塞的 handler 以 503 解阻 |
| 关闭 | `Shutdown` 5s 超时 |

### 6.3 Hook 事件流

```
Agent 触发 hook
  └── 执行 hook 命令: <mechaBinary> webhook --addr <agentd-addr>   (hook JSON 经 stdin)
       │    mecha webhook：10s 客户端超时；非 200 读最多 1 MiB 错误体
       └── POST http://<agentd-addr>/webhook
            └── agentd: parseFn(raw) → hookCh(容量32) → handleHook
                 ├── SessionStart → ReportStatus(started) → Core: markStarted → ready
                 ├── Stop         → taskCh ← {success, output} → TaskResult
                 └── StopFailure  → taskCh ← {failure, error} → TaskResult
```

**统一事件结构（`pkg/agent/types`）：**

```go
type HookEvent struct {
    Event     string `json:"event"`
    SessionID string `json:"session_id,omitempty"`
    Output    string `json:"output,omitempty"`
    Error     string `json:"error,omitempty"`
}
```

事件常量 4 个：`SessionStart` / `PostToolBatch` / `Stop` / `StopFailure`。其中 `PostToolBatch` 仅 claude 的 eventMap 能识别，**没有任何 agent 的 hook 配置注册它**，agentd 的 handleHook 也不处理。

统一解析框架 `ParseHook(prefix, raw, eventMap, extract)`：读 `hook_event_name` 查 eventMap（未知事件报错）→ 提取顶层 `session_id` → 调 agent 自定义的 extract 填 output/error。

### 6.4 CLI 命令

| 命令 | 调用方 | 说明 |
|---|---|---|
| `mecha` / `mecha run` | 用户 | 启动 Core + coordinator；`--config <path>` 指定配置（默认 `~/.mecha/config.yaml`） |
| `mecha init [-f]` | 用户 | 写出默认配置；存在时备份 `.bak`，`-f` 直接覆盖不备份 |
| `mecha ask --addr <ADDR> <role> "<task>"` | Coordinator | 阻塞等结果；成功 stdout 原样输出（**无结尾换行**）exit 0；失败 stderr + exit 1 |
| `mecha webhook --addr <ADDR>` | agent hook | stdin → POST agentd `/webhook`（10s 超时） |
| `mecha agentd --id <id> --addr <ADDR>` | Core | 运行 agentd（内部命令） |
| `mecha version` | 用户 | 输出 Version / Date / Go 运行时（ldflags 注入，默认 Unknown / n/a） |

`--config` 是 root 的 persistent flag，但只有 run/裸命令读取它。

---

## 7. Agent 实现

### 7.1 接口与共享件

```go
type Agent interface {
    Prepare() error
    Cmd() *exec.Cmd
    ParseHookEvent(raw []byte) (HookEvent, error)
}

type AgentContext struct {
    Workspace   string  // 项目根目录
    RoleDir     string  // <workspace>/.mecha/roles/<role>
    Prompt      string  // 渲染后的 role prompt
    WebhookAddr string  // agentd 本地 webhook 地址
}

type Factory func(ctx AgentContext, cfg config.AgentConfig, runtime config.Runtime) (Agent, error)
```

注册表在 `pkg/agent` 的 `init()` 中注册 5 种 type：`claude` / `codebuddy` / `codex` / `gemini` / `pi`。

**共享工具（`pkg/agent/types`）：**

- `MergeMap(defaults, user)`：defaults 为底、user 覆盖，返回新 map
- `BuildArgs(user, defaults)`：合并后按 key 排序输出 `--key value`；**bool true → 裸 `--key`；bool false → 整个省略**（pflag 风格 CLI 中 `--key false` 会把 "false" 漏成位置参数）
- `BuildEnv(user, defaults)`：三层合并，后层覆盖前层——`os.Environ()` → defaults → user；排序输出。继承进程环境保证 `TERM`/`COLORTERM`/`HOME`/`PATH`/API key 正常传递
- `ResolveBinary`：`cfg.Binary` 或默认 binary，factory 时即 `exec.LookPath` 校验存在性
- `WriteJSONFile`：临时文件 + rename 原子写
- `QuoteShell`：白名单字符（`[A-Za-z0-9_./:@%+=,-]`）不引用，否则单引号包裹
- `WebhookCommand(binary, addr)` → `<binary> webhook --addr <addr>`（均过 QuoteShell）

### 7.2 五种 agent 对照

| | claude | codex | gemini | codebuddy | pi |
|---|---|---|---|---|---|
| 默认 binary | `claude` | `codex` | `gemini` | `codebuddy` | `pi` |
| Prepare 写文件 | `CLAUDE.md` + `settings.json` | 仅 `AGENTS.md` | `GEMINI.md` + `.gemini/settings.json` | `CODEBUDDY.md` + `settings.json` | `PI.md` + `.pi/settings.json` |
| defaultParams | `dangerously-skip-permissions: true` | `dangerously-bypass-approvals-and-sandbox: true` | `yolo: true` | `dangerously-skip-permissions: true` | 无（Pi 无权限系统） |
| defaultEnvs | `BASH_DEFAULT_TIMEOUT_MS=1200000` | 无 | 无 | `CODEBUDDY_CODE_MAX_OUTPUT_TOKENS=8192` | 无 |
| prompt 注入 | `--append-system-prompt-file <CLAUDE.md>` | `--config model_instructions_file=<AGENTS.md>` | cmd.Dir=RoleDir，CLI 自行发现 `GEMINI.md` | `--append-system-prompt <prompt 原文>`（内联） | `--append-system-prompt <prompt 原文>` + cmd.Dir=RoleDir（双路） |
| cmd.Dir | Workspace | Workspace（另加 `--cd <Workspace>`） | **RoleDir** | Workspace | **RoleDir** |
| hook 注入 | settings.json，command+args exec 数组 | 无 settings 文件，`--config hooks.<Event>=[...]` 内联 TOML | `.gemini/settings.json`，shell 字符串 command | settings.json，**shell 字符串** command（CodeBuddy 只支持 shell-form） | `.pi/settings.json`，shell 字符串 command |
| 注册的 hook 事件 | SessionStart / Stop / StopFailure | SessionStart / Stop / StopFailure | SessionStart（matcher startup）/ **AfterAgent**（matcher `*`） | SessionStart / Stop / StopFailure | SessionStart（startup）/ Stop（`*`） |
| 事件映射 | 四事件同名映射（含 PostToolBatch） | 同名映射 | AfterAgent → Stop | 同名映射 | 同名映射，**无 StopFailure** |
| Stop output 字段 | `last_assistant_message` | `last_assistant_message` | `prompt_response` | `last_assistant_message` | `last_assistant_message` |
| StopFailure error 字段 | `error_type` 优先，否则 `error` | 同 claude | —（无此事件） | 同 claude | — |

所有 agent 的 `--model <model>` 仅在 cfg.Model 非空时输出；settings 类文件均经 `WriteJSONFile` 原子写入。

---

## 8. 终端后端

### 8.1 接口与检测

```go
type Backend interface {
    Spawn(ctx context.Context, spec Spec) (Handle, error)
    Kill(ctx context.Context, handle Handle) error
    Label(text string) error  // 给当前进程所在 pane 打标签（badge）
    Close() error
}

type Spec struct {
    WorkDir string
    Command []string
    Role    string  // specialist spawn 时传入，用于 pane badge
}

type Handle interface {
    ID() string     // 展示 ID（如 tmux-1），包内 atomic 序号生成
    PaneID() string // 后端原生 pane/session/terminal ID
}
```

**检测优先级（`term.New()`）**：tmux → iTerm2 → Ghostty，取第一个 Match 的，全不匹配返回 `ErrUnsupported`。

| 后端 | Match 条件 | 平台 |
|---|---|---|
| tmux | `TMUX` 环境变量非空 **且** `tmux` binary 存在 | macOS / Linux |
| iTerm2 | `TERM_PROGRAM`（小写）包含 `iterm` | macOS |
| Ghostty | `TERM_PROGRAM`（小写）包含 `ghostty` | macOS |

**Anchor 机制**：三个后端都在初始化时钉住 coordinator 所在的 pane/session/terminal（tmux 用 `TMUX_PANE`，iTerm2 解析 `ITERM_SESSION_ID` 取 GUID，Ghostty 用 AppleScript 捕获 front window 的 focused terminal），保证用户切换窗口/标签后 Spawn 仍落在 coordinator 处。

**driver.Chain**：`[]string` 栈，按分割顺序记录已 spawn pane 的原生 ID；后续分割以 `Last()` 为目标；Kill 时无论成败都 Remove（防 stale ID 成为后续分割目标）。

**BuildCommand**：每个 arg 过 `QuoteShell` 后空格连接；`WorkDir` 非空时前置 `cd <dir> && `。

### 8.2 后端实现对照

| | tmux | iTerm2 | Ghostty |
|---|---|---|---|
| 通信方式 | tmux CLI | WebSocket（protobuf over unix socket） | AppleScript（`osascript -e`，无持久连接） |
| 连接时机 | 每次调用起进程 | **New() 立即 dial**；出错置 dead，下次 Spawn/Kill 自动重拨 | 每次调用起 osascript |
| 认证 | 无 | osascript 取 cookie/key（10s 超时），header 携带 | macOS Automation 权限 |
| 首个分割 | `split-window -h -p 50`（右侧） | SplitPaneRequest **VERTICAL**（右侧） | `split <anchor> direction right` |
| 后续分割 | `split-window -v -p 50`（下方，目标 chain.Last） | **HORIZONTAL**（下方，无比例参数） | `split <last> direction down` |
| 新 pane ID | `-P -F #{pane_id}` 返回 | SplitPane 响应 | `split` 命令直接返回新 terminal（无计数竞态） |
| 引导发送 | `send-keys -l <cmd>` + `C-m C-j`（CR+LF） | `SendTextRequest`（`cmd + "\r\n"`）；Role 非空时前缀 badge printf | `input text` + `send key "enter"` |
| WorkDir | `-c <dir>` 参数 **和** `cd &&` 前缀双保险 | `cd &&` 前缀 | `cd &&` 前缀 |
| 引导失败回滚 | kill-pane | closeOrphan（连接死了先重拨再 close session） | — |
| Kill | `kill-pane` | `CloseRequest`；**chain 空时主动断开 WebSocket** | AppleScript `close` |
| Label | no-op | **真实实现**：向自身 stdout 写 `SetBadgeFormat` 转义序列 | no-op |
| Close | no-op | 关 WebSocket | no-op |

iTerm2 协议细节：自增 seq 作请求 id，写超时 10s、读超时 30s，跳过 id=0 的通知帧，id 不匹配或解码失败即 fail（关连接置 dead）。socket 路径 `~/Library/Application Support/iTerm2/private/socket`，子协议 `api.iterm2.com`。

Ghostty 注意：必须用 AppleScript `split` 命令（返回值即新 terminal）；不要用 `perform action "new_split:*"` + 计数反查（异步竞态会拿到旧终端）。AppleScript 字符串先转义反斜杠再转义双引号（未转义的反斜杠会被静默吞掉）。

---

## 9. 数值常量汇总

| 常量 | 值 | 用途 |
|---|---|---|
| registerTimeout | 5s | Core 等 agentd Register |
| agentStartTimeout | 30s | Core 等 ready（任务流挂载 + SessionStart） |
| paneKillTimeout | 5s | 单次 Kill pane |
| serverStopTimeout | 5s | GracefulStop 兜底后强制 Stop |
| TaskTimeout | 30min | 任务上限，Core/agentd 共用（`api.TaskTimeout`） |
| readyQuietPeriod | 1.5s | agentd 输出静默判定 TUI 就绪 |
| readyTimeout | 30s | 就绪判定兜底放行 |
| 就绪 grace period | 500ms | 静默后再固定等待 |
| 就绪轮询间隔 | 50ms | watchReady |
| PTY 启动后 sleep | 100ms | launchPTY 给 TUI 初始化 |
| task 与回车写入间隔 | 100ms | handleTask 两次写 PTY |
| ReportStatus deadline | 2s | agentd 每次状态上报 |
| webhook Shutdown | 5s | agentd 关闭 |
| webhook body 上限 | 1 MiB | MaxBytesReader |
| mecha webhook 客户端超时 | 10s | CLI → agentd POST |
| forwardStdin 缓冲 | 4 KiB | stdin → PTY |
| PTY 默认尺寸 | 24×80 | 取不到终端尺寸时兜底 |
| resultCh / taskCh 容量 | 1 | 结果通道 |
| hookCh 容量 | 32 | hook 事件通道 |

---

## 10. 已知限制

| 限制 | 说明 |
|---|---|
| session_id 不持久化 | HookEvent 携带 session_id，但只存内存，重启后丢失 |
| 无 resume | 未接入 agent 会话恢复机制 |
| mecha 被 SIGKILL | coordinator 与 specialist pane 残留 |
| TaskChannel 不重连 | agentd 侧流断开即 signalStop 退出，实例靠 Core 下次 ask 重建 |
| PostToolBatch 未启用 | 常量与 claude eventMap 已定义，但无 hook 注册、handleHook 不处理 |
| 任务超时的体验 | 30min 超时后实例标记 unhealthy 重建，agent 内可能仍在生成 |
| 无鉴权 | 所有监听仅绑 127.0.0.1，本机同用户任意进程可调用 Ask（见 README 安全说明） |
