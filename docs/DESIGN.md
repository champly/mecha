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
      │    ├── ReportStatus  ← agentd 上报 started/failed/exited
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
| `mecha run` | 加载配置 → 检测终端后端 → 打开日志文件 → 绑定 gRPC（127.0.0.1:0）→ 拉起 coordinator agentd（前台，等 register 5s + ready 不限时）→ 阻塞等其退出 |
| agentd 启动 | 本地 webhook HTTP → gRPC 连接 → Register 换取配置 → 设置 pane 标题 → `--dir` chdir → Prepare + PTY 启动 agent → 建立 TaskChannel → hookLoop + supervise；TUI 确认可操作后才上报 started |
| 首次 `ask <role>` | Spawn 新 pane 启动 specialist agentd，等 register（5s）+ ready（45s） |
| 再次 `ask <role>` | 复用健康实例，经 TaskChannel 下发任务 |
| 任务完成 | agent 触发 Stop/StopFailure hook → agentd 回传 TaskResult → 实例回到 running |
| 任务超时（30min） | Core 侧返回错误且实例保持 unhealthy；agentd 侧直接 signalStop 自杀 |
| agent 退出 | agentd 失败在途任务、上报 exited；Core 立即失败等待中的 Ask，实例标记 unhealthy |
| 下次 `ask <role>` 且 unhealthy | destroy 旧实例（Kill pane），重建新 pane + 新 agentd + 新 agent |
| Coordinator 退出 | shutdown：立即 Stop gRPC → 并行 Kill 所有 pane（总上限 1s）→ backend.Close |

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

`agent: claude-sonnet-4-6`，`profile: softwarecompany`。agents 定义 5 个（codex / claude-sonnet-4-6 / claude-opus-4-8 / codebuddy / pi）；profile 含 5 个 role：`lead`（coordinator）、`architect`、`coder`、`tester`、`reviewer`。`architect` 负责设计分析与指定的 `docs/*.md` 文档，`coder` 只改代码；`coder` 显式使用 Codex；`tester` 不声明 role 级 agent，继承默认 Claude；其余 role 使用显式配置。

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
       ├── 取 profile 中第一个 IsCoordinator 的 role
       ├── newInstance(uuid) + registry.add
       ├── exec.Command(mechaBinary, "agentd", "--id", id, "--addr", addr, "--dir", workspace)
       │   stdio 直连终端；context.AfterFunc(ctx, Process.Kill)
       ├── waitRegistered(5s) + waitReady(不限时)，失败则 Kill + Wait 收割后报错
       └── cmd.Wait() 阻塞至退出；ctx 已取消视为正常关停
4. shutdown()
   ├── server.Stop()             # 立即中断 Ask/TaskChannel，避免退出被活跃 RPC 拖住
   ├── killAllPanes(ctx)         # 所有 pane 并行 Kill，共享 1s 总截止时间
   ├── backend.Close()
   └── logFile.Close()
```

### 4.2 agentd 启动（`mecha agentd --id <id> --addr <addr> --dir <workspace>`）

严格顺序，任一步失败 `Close()` 后返回：

```
1. `--dir` 非空时先 chdir，确保日志与 agent 工作目录固定
2. initLogging（与 Core 同一日志文件，失败静默）
3. WebhookServer 起 HTTP（127.0.0.1:0，POST /webhook）
4. grpc.NewClient(CoreAddr, insecure)（lazy dial）+ Register(id)（metadata 带 instance-id）
   ← 返回 workspace / prompt（已渲染）/ role_name / mecha_binary / agent 配置
5. 向自身 stdout 写 OSC 0 pane 标题（serve/headless 跳过）
6. startAgent：
   ├── agent.NewFromConfig → Prepare()（写 role 文件）→ webhook.SetParseFunc(ParseHookEvent)
   ├── 强制 provider 实现 TUIAgent，缺失 profile 时启动失败
   ├── launchPTY：取 stdin 窗口尺寸（失败退化 24×80）→ pty.StartWithSize
   ├── makeRawIfTerminal：stdin 是终端则切 raw，退出时恢复
   └── goroutines: output pump（ptmx → screen parser → stdout）、input arbiter、session actor、
       forwardStdin、watchWinch、waitAgent
7. connectTaskChannel()：TUI 尚未 ready，此时到达的任务只会进入 session 队列
8. go hookLoop + go supervise；session 在 raw + InputEmpty 的内容 hash 稳定后上报 started；Start 返回，CLI 层 d.Wait() 阻塞在 <-stop
```

**退出链**：`waitAgent`（agent 退出）/ `taskLoop`（流断开，视为 Core 已死）/ 任务超时 → `signalStop()` → session 先失败所有 pending → `supervise` 上报 exited 并 `Close()`。`Close`（closeOnce 保护）：终止 agent 进程组 → 停止状态投递 → 取消 task stream → webhook Shutdown → gRPC conn → logFile。

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
       │       └── waitReady (45s)        ┴─ 任一失败 destroy 并返回错误
       │          ready = TaskChannel attached + agentd 上报 TUI 可操作（与到达顺序无关）
       │          ReportStatus(failed, reason) 会立即终止等待并返回具体启动原因
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
  1. 在写入任何 PTY 字节前登记唯一 pending
  2. 把 TaskRequest 作为事件交给 session actor；Core 提前送来的任务只排队
  3. 只有 session 进入 phaseIdle + raw mode + InputEmpty + 无用户 dirty 时才开始提交
  4. input arbiter 单写者按事务写入 paste start/正文/end，再根据 Draft/Busy/PromptSubmit 确认 Enter
  5. select：
     ├── pending resultCh 收到结果/带原因失败 → 回 TaskResult
     ├── stop → "agent exited during task"
     └── 30min 超时 → 日志报错并 signalStop() 自杀（不回结果，让 Core 判 unhealthy 重建）
```

**就绪判定**：PTY 输出经 `charmbracelet/x/vt` 转成不可变屏幕快照，provider profile 分类为 `InputEmpty/InputDraft/InputBusy/InputBlocked/InputUnknown`。只有 `termios raw + InputEmpty + 可见内容 hash 300ms 不变` 才上报 started；光标闪烁、颜色或纯样式刷新不会重置 settle。静默窗口只用于去抖，不是就绪证明；未知状态和未知弹窗 fail closed，30s 后带原因上报 failed。Codex 额外解析 `OSC 7501;state=idle|working:app=codex` 作为首选状态信号，文本锚点只作兜底。

**输入仲裁**：stdin 用户字节、任务文本和 Enter 全部经过 `inputArbiter`。任务事务期间用户输入进入有界缓冲，事务结束后再写入 PTY；用户按键会立即设置 dirty，只有后续屏幕重新确认 Empty 才解除。PTY 直接写路径已删除。虚拟终端的 DSR/DA/CPR 查询回复由 parser 内部 pipe 异步排空并通过同一 arbiter 回写，避免 Emulator.Write 被无消费者回复阻塞；外层终端产生的重复自动回复会被过滤，焦点/设备查询回复不会把 pane 误标为用户占用。

**Hook 与归属**：session actor 统一处理 provider hook。

| 事件 | 动作 |
|---|---|
| UserPromptSubmit / BeforeAgent | 完整 prompt 匹配 pending 后绑定 turn；不匹配视为用户轮次 |
| Stop / StopFailure | 只有当前 Core turn 的结果才回传；用户轮次的结果丢弃 |
| Interrupt | 匹配当前 Core turn 时立即带 "interrupted by user" 失败 |
| SessionStart | 记录 source；`clear` 会失效当前 Core turn，永不作为 ready 信号 |
| Notification / PermissionRequest | 识别权限、登录等交互阻塞；运行中失败当前任务并保留 agent |

支持轮次标识的 provider 优先使用 `(session_id, turn_id)` 精确归属：Codex `turn_id`、CodeBuddy `generation_id`、Claude `prompt_id`（以实测 payload 为准）。无标识时退化为单 pending + prompt 完整匹配。

**丢 Stop 兜底**：若某轮曾确认进入 `InputBusy`，随后 Busy 标记消失且 10s 内没有 Stop/StopFailure/Interrupt，则把 Core 任务以 `"turn ended without a result (interrupted?)"` 失败；用户轮次静默回 Idle。该计时只处理丢事件，不用于证明 ready 或 submitted。

状态上报和任务结果投递不阻塞 session actor。`ReportStatus(failed, reason)` 由独立 delivery worker 发送，失败原因会经 Core `waitReady` 返回给调用方。

**session 关键转换：**

| 当前状态 | 事件 | 结果 |
|---|---|---|
| Booting | raw + Empty + 内容 hash 稳定 | Idle，上报 started |
| Booting | 用户 PromptSubmit | Running(User)，上报 started |
| Idle | Core task + Empty + 无用户 dirty | Submitting |
| Submitting | pending PromptSubmit / 屏幕 Busy | Running(Core)，绑定 turn |
| Submitting | 匹配 Stop 早到 | 暂存，绑定 turn 后交付 |
| Submitting | 不同 prompt / Interrupt | pending 失败 |
| Submitting | 仍 Draft | 最多 3 次 Enter |
| Running(Core) | 匹配 Stop / StopFailure / Interrupt | 交付结果或失败 |
| Running(Core) | Blocked / `SessionStart(clear)` | 失败当前任务 |
| Running(User) | Stop / StopFailure / Interrupt | Idle，结果不交 Core |
| InteractionRequired | Blocked 解除 + Empty | Idle |
| 任意 | agent 退出 | 失败所有 pending |

如果一轮曾进入 Busy，随后 Busy 标记消失且 10s 无 Stop/StopFailure/Interrupt，则以丢事件熔断结束该轮。

**provider 归属能力：**

| provider | 提交信号 | 轮次标识 | 粘贴模式 |
|---|---|---|---|
| Codex | `UserPromptSubmit` | `turn_id` | bracketed |
| CodeBuddy | `UserPromptSubmit` | `generation_id`（可选） | bracketed |
| Claude | `UserPromptSubmit` | `prompt_id`（需实测） | bracketed |
| Gemini | `BeforeAgent` | 无，prompt 完整匹配 | literal |
| Pi | 无可靠提交 hook | 屏幕 Busy / 超时确认 | literal |

### 5.3 状态机

```
                                 register + started
                  Spawn agentd ──► starting ────────────────────────► running
                    │                │                              │
                    │      5s 注册超时 / 45s ready 超时             │ Ask 下发任务
                    │                ▼                              ▼
                    │       failed(reason) / 启动失败清理          busy
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
| running | 2 | TaskChannel 已挂载且 agentd 上报 TUI 可操作，可接收任务 |
| busy | 3 | 任务在途，等待 Stop / StopFailure / 退出 / 超时 |
| unhealthy | 4 | 启动 failed / agent 退出 / 断连 / 任务超时，禁止接任务，下次 ask 重建 |

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
    registerCh, readyCh, failedCh chan struct{}
    readyClosed, failedClosed bool
    failureReason string
}

type Agentd struct {
    opts     Options{ID, CoreAddr}
    client   api.CoreClient
    conn     *grpc.ClientConn
    webhook  *WebhookServer
    hookCh   chan types.HookEvent  // 容量 32

    ptmx     *os.File
    cmd      *exec.Cmd
    stop     chan struct{}
    profile  provider.TUIProfile
    screen   ScreenParser
    input    *inputArbiter         // 唯一 PTY 写者
    session  *sessionActor         // 唯一任务状态写者
    statusCh chan statusReport     // 异步状态投递
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
| `ReportStatus` | unary | agentd → Core | 上报 `started`（TUI 确认可操作后）/ `failed`（带启动失败原因）/ `exited` |
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
message StatusRequest { string id = 1; string status = 2; string reason = 3; }
message AskRequest    { string role = 1; string task = 2; }
message AskResponse   { string id = 1; bool success = 2; string result = 3; }
message TaskRequest   { string id = 1; string task = 2; }
message TaskResult    { string id = 1; bool success = 2; string result = 3; }
```

**实现细节：**

- 状态常量为 `started` / `failed` / `exited`；`failed` 携带可读 `reason`
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
| 入队 | 最多等待 250ms；队列满返回 503，避免拖住短超时 Hook（如 Interrupt 默认 1s） |
| 关闭 | `Shutdown` 1s 超时 |

### 6.3 Hook 事件流

```
Agent 触发 hook
  └── 执行 hook 命令: <mechaBinary> webhook --addr <agentd-addr>   (hook JSON 经 stdin)
       │    mecha webhook：10s 客户端超时；非 200 读最多 1 MiB 错误体
       └── POST http://<agentd-addr>/webhook
            └── agentd: parseFn(raw) → hookCh(容量32) → handleHook
                 ├── screen/termios ready → ReportStatus(started) → Core ready
                 ├── UserPromptSubmit/BeforeAgent → 绑定 pending turn
                 ├── Stop/StopFailure → 匹配 turn 后回传 TaskResult
                 ├── Interrupt → 立即失败当前 Core turn
                 └── Notification/PermissionRequest → 标记交互阻塞
```

**统一事件结构（`pkg/agent/provider`）：**

```go
type HookEvent struct {
    Event            string `json:"event"`
    SessionID        string `json:"session_id,omitempty"`
    TurnID           string `json:"turn_id,omitempty"`
    Prompt           string `json:"prompt,omitempty"`
    Source           string `json:"source,omitempty"`
    Output           string `json:"output,omitempty"`
    Error            string `json:"error,omitempty"`
    NotificationType string `json:"notification_type,omitempty"`
    Message          string `json:"message,omitempty"`
}
```

内部事件包括 `SessionStart`、`UserPromptSubmit`、`Stop`、`StopFailure`、`Interrupt` 和 `InteractionRequired`。Codex 不支持 StopFailure，已从 eventMap 和启动参数移除。

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

**共享工具（`pkg/agent/provider`，shell 引号工具见 `pkg/shellquote`）：**

- `MergeMap(defaults, user)`：defaults 为底、user 覆盖，返回新 map
- `BuildArgs(user, defaults)`：合并后按 key 排序输出 `--key value`；**bool true → 裸 `--key`；bool false → 整个省略**（pflag 风格 CLI 中 `--key false` 会把 "false" 漏成位置参数）
- `BuildEnv(user, defaults)`：三层合并，后层覆盖前层——`os.Environ()` → defaults → user；排序输出。继承进程环境保证 `TERM`/`COLORTERM`/`HOME`/`PATH`/API key 正常传递
- `ResolveBinary`：`cfg.Binary` 或默认 binary，factory 时即 `exec.LookPath` 校验存在性
- `WriteJSONFile`：临时文件 + rename 原子写
- `shellquote.Quote`（`pkg/shellquote`，agent 层与终端层共用，无内部依赖）：白名单字符（`[A-Za-z0-9_./:@%+=,-]`）不引用，否则单引号包裹
- `WebhookCommand(binary, addr)` → `<binary> webhook --addr <addr>`（均过 `shellquote.Quote`）

### 7.2 五种 agent 对照

| | claude | codex | gemini | codebuddy | pi |
|---|---|---|---|---|---|
| 默认 binary | `claude` | `codex` | `gemini` | `codebuddy` | `pi` |
| Prepare 写文件 | `CLAUDE.md` + `settings.json` | 仅 `AGENTS.md` | `GEMINI.md` + `.gemini/settings.json` | `CODEBUDDY.md` + `settings.json` | `PI.md` + `.pi/settings.json` |
| defaultParams | `dangerously-skip-permissions: true` | `dangerously-bypass-approvals-and-sandbox: true` + `dangerously-bypass-hook-trust: true` | `yolo: true` | `dangerously-skip-permissions: true` | 无（Pi 无权限系统） |
| defaultEnvs | `BASH_DEFAULT_TIMEOUT_MS=1200000` | 无 | 无 | `CODEBUDDY_CODE_MAX_OUTPUT_TOKENS=8192` | 无 |
| prompt 注入 | `--append-system-prompt-file <CLAUDE.md>` | `--config model_instructions_file=<AGENTS.md>` | cmd.Dir=RoleDir，CLI 自行发现 `GEMINI.md` | `--append-system-prompt <prompt 原文>`（内联） | `--append-system-prompt <prompt 原文>` + cmd.Dir=RoleDir（双路） |
| cmd.Dir | Workspace | Workspace（另加 `--cd <Workspace>`） | **RoleDir** | Workspace | **RoleDir** |
| hook 注入 | settings.json，command+args exec 数组 | 无 settings 文件，`--config hooks.<Event>=[...]` 内联 TOML，`type=command` + shell 字符串 command | `.gemini/settings.json`，shell 字符串 command | settings.json，**shell 字符串** command（CodeBuddy 只支持 shell-form） | `.pi/settings.json`，shell 字符串 command |
| 注册的 hook 事件 | SessionStart / UserPromptSubmit / Stop / StopFailure / Notification | SessionStart / UserPromptSubmit / Stop / Interrupt | SessionStart / **BeforeAgent** / **AfterAgent** / Notification | SessionStart / UserPromptSubmit / Stop / StopFailure / Notification | SessionStart / Stop |
| 事件映射 | UserPromptSubmit、Notification | UserPromptSubmit、Interrupt；**无 StopFailure** | BeforeAgent → PromptSubmit；AfterAgent → Stop | UserPromptSubmit、Notification | SessionStart / Stop 同名映射 |
| 轮次标识 | `prompt_id`（需实测确认 Stop payload） | `turn_id` | 无，prompt 完整匹配 | `generation_id`（可选） | 无，屏幕 Busy 兜底 |
| TUI paste | bracketed | bracketed | literal | bracketed | literal |
| Stop output 字段 | `last_assistant_message` | `last_assistant_message` | `prompt_response` | `last_assistant_message` | `last_assistant_message` |
| StopFailure error 字段 | `error_type` 优先，否则 `error` | —（无此事件） | —（无此事件） | 同 claude | —（无此事件） |

所有 agent 的 `--model <model>` 仅在 cfg.Model 非空时输出；settings 类文件均经 `WriteJSONFile` 原子写入。

---

## 8. 终端后端

### 8.1 接口与检测

```go
type Backend interface {
    Spawn(ctx context.Context, spec Spec) (Handle, error)
    Kill(ctx context.Context, handle Handle) error
    Close() error
}

type Spec struct {
    WorkDir string
    Command []string
    Role    string  // specialist 角色，headless 用于 handle 命名
}

type Handle interface {
    ID() string     // 展示 ID（如 tmux-1），包内 atomic 序号生成
    PaneID() string // 后端原生 pane/session/terminal ID
}
```

**检测优先级（`term.New()`）**：tmux → iTerm2 → Ghostty，auto 取第一个 Match 且 anchor 可用的后端；显式 `Kind` 返回该后端的 anchor 错误；auto 跳过 anchor 不可用的后端，仅在 `AllowHeadless` 时回退 headless，否则返回 `ErrUnsupported`（保留 anchor 原因）。

| 后端 | Match 条件 | 平台 |
|---|---|---|
| tmux | `TMUX` 环境变量非空 **且** `tmux` binary 存在 | macOS / Linux |
| iTerm2 | `TERM_PROGRAM`（小写）包含 `iterm` | macOS |
| Ghostty | `TERM_PROGRAM`（小写）包含 `ghostty` | macOS |

**Anchor 机制**：三个后端都钉住 coordinator 所在的 pane/session/terminal（tmux 用 `TMUX_PANE`，iTerm2 解析 `ITERM_SESSION_ID` 取 GUID，Ghostty 在 `New()` 用 `ps`/`tty` 同步捕获进程的控制终端 tty，并在首次 Spawn 时通过 AppleScript 终端列表按 tty 懒解析出唯一 terminal id），保证用户切换窗口/标签后 Spawn 仍落在 coordinator 处。anchor 不可用时三个后端一律 fail closed：返回明确错误且不创建 pane，绝不回退到 active/current focus。`New()` 阶段 anchor 不可用（`TMUX_PANE`/`ITERM_SESSION_ID` 为空、Ghostty 取不到控制 tty）会包装 `driver.ErrAnchorUnavailable`：显式 `Kind` 返回该 anchor 错误、绝不回退；auto 跳过该后端，仅在 `AllowHeadless` 时回退 headless，否则返回 `ErrUnsupported`（保留 anchor 原因）。

**driver.Chain**：`[]string` 栈，按分割顺序记录已 spawn pane 的原生 ID；后续分割以 `Last()` 为目标；Kill 时无论成败都 Remove（防 stale ID 成为后续分割目标）。

**BuildCommand**：每个 arg 过 `shellquote.Quote` 后空格连接，不再注入 `cd &&`。agentd 自己接收 `--dir <workspace>` 并 chdir，spawn 命令保持为单条可执行命令。

**Pane 标题**：agentd 在 Register 后向自身 stdout 写 OSC 0 标题 `\x1b]0;<role>\x07`。tmux/iTerm2/Ghostty 等终端识别时显示角色名；`--log-stdout`/headless 或 stdout 非 TTY 时跳过，不会把转义写进日志。原 `Backend.Label` 接口已删除。

### 8.2 后端实现对照

| | tmux | iTerm2 | Ghostty |
|---|---|---|---|
| 通信方式 | tmux CLI | WebSocket（protobuf over unix socket） | AppleScript（`osascript -e`，无持久连接） |
| 连接时机 | 每次调用起进程 | 首次 Spawn 时 dial；出错置 dead，下次 Spawn/Kill 自动重拨 | 每次调用起 osascript（`New()` 只捕获 tty） |
| 认证 | 无 | osascript 取 cookie/key（10s 超时），header 携带 | macOS Automation 权限 |
| 首个分割 | `split-window -h -p 50`（右侧） | SplitPaneRequest **VERTICAL**（右侧） | 按控制 tty 懒解析唯一 anchor id，再 `split <anchor> direction right` |
| 后续分割 | `split-window -v -p 50`（下方，目标 chain.Last） | **HORIZONTAL**（下方，无比例参数） | `split <last> direction down` |
| 新 pane ID | `-P -F #{pane_id}` 返回 | SplitPane 响应 | `split` 命令直接返回新 terminal（无计数竞态） |
| 引导发送 | `split-window <cmd>` 直接执行，无 shell 回显/history | 先 `GetBuffer` 等 shell 打印提示符，再 `SendTextRequest` | `input text` + `send key "enter"`（AppleScript 无 buffer 读取） |
| WorkDir | `-c <dir>` + agentd `--dir` | agentd `--dir` | agentd `--dir` |
| 引导失败回滚 | kill-pane | closeOrphan（连接死了先重拨再 close session） | — |
| Kill | `kill-pane` | `CloseRequest`；**chain 空时主动断开 WebSocket** | AppleScript `close` |
| Pane 标题 | agentd OSC 0 | agentd OSC 0 | agentd OSC 0 |
| Close | no-op | 关 WebSocket | no-op |

iTerm2 协议细节：自增 seq 作请求 id，写超时 10s、读超时 30s，跳过 id=0 的通知帧，id 不匹配或解码失败即 fail（关连接置 dead）。socket 路径 `~/Library/Application Support/iTerm2/private/socket`，子协议 `api.iterm2.com`。

Ghostty 注意：必须用 AppleScript `split` 命令（返回值即新 terminal）；不要用 `perform action "new_split:*"` + 计数反查（异步竞态会拿到旧终端）。AppleScript 字符串先转义反斜杠再转义双引号（未转义的反斜杠会被静默吞掉）。首个 Spawn 先执行 `listTerminalsScript` 列出所有 terminal 的 `id|tty`，按本进程控制 tty 精确匹配；0 个或多个匹配即报错并放弃创建 pane。

---

## 9. 数值常量汇总

| 常量 | 值 | 用途 |
|---|---|---|
| registerTimeout | 5s | Core 等 agentd Register |
| agentStartTimeout | 45s | Core 等 specialist ready；coordinator 不限时 |
| paneKillTimeout | 5s | 单次 Kill pane |
| paneShutdownTimeout | 1s | Coordinator 退出时并行 Kill 所有 pane 的总上限 |
| TaskTimeout | 30min | 任务上限，Core/agentd 共用（`api.TaskTimeout`） |
| screen settle | 300ms | InputEmpty 后的渲染去抖；未知状态不得放行 |
| bootTimeout | 30s | 等待正面可操作 TUI 的上界，超时上报 failed |
| task render | 1s | 等待任务文本进入 Draft/Busy 的 liveness 上界 |
| Enter confirmation | 3s × 最多 3 次 | 只对仍为 Draft 的 pending 重发 Enter |
| queue timeout | 10min | 用户占用 agent 时 Core 任务排队上界 |
| interrupt fallback | 10s | Busy 标记消失且无 Stop/Interrupt 时的丢事件熔断 |
| user input buffer | 1MiB | 提交事务期间的用户输入缓冲上限 |
| ReportStatus deadline | 2s | agentd 每次状态上报 |
| webhook enqueue | 250ms | 队列满时快速返回 503 |
| webhook Shutdown | 1s | agentd 关闭 |
| webhook body 上限 | 1 MiB | MaxBytesReader |
| mecha webhook 客户端超时 | 10s | CLI → agentd POST |
| forwardStdin 缓冲 | 4 KiB | stdin → PTY |
| PTY 默认尺寸 | 24×80 | 取不到终端尺寸时兜底 |
| pending resultCh 容量 | 1 | 每个任务唯一结果通道 |
| hookCh 容量 | 32 | hook 事件通道 |
| session event 容量 | 256 | 状态机事件队列 |
| screen coalescing | 1 | 只保留最新屏幕快照 |

---

## 10. 已知限制

| 限制 | 说明 |
|---|---|
| session_id 不持久化 | HookEvent 携带 session_id，但只存内存，重启后丢失 |
| 无 resume | 未接入 agent 会话恢复机制 |
| mecha 被 SIGKILL | coordinator 与 specialist pane 残留 |
| TaskChannel 不重连 | agentd 侧流断开即 signalStop 退出，实例靠 Core 下次 ask 重建 |
| 任务超时的体验 | 30min 超时后实例标记 unhealthy 重建，agent 内可能仍在生成 |
| TUI profile 版本耦合 | agent 升级改版可能导致 InputUnknown；系统会 fail closed 并返回原因，需要更新 profile fixture |
| 运行中交互阻塞 | 权限/登录弹窗会快速失败当前 Core 任务，但保留 agent；用户处理后该轮结果不再回传 |
| 用户占用排队 | 用户草稿、用户 running 或弹窗期间最多排队 10min，超时任务带原因失败 |
| Ghostty pane 引导竞态 | AppleScript 无法直接执行命令或读取 buffer，仍只能 `input text` + Enter；启动命令回显竞态尚未消除 |
| coordinator 无启动超时 | 用户可处理首次信任/登录弹窗；若 agent 进程存活但 TUI 永远不可操作，会持续等待，需用户退出或干预 |
| 无鉴权 | 所有监听仅绑 127.0.0.1，本机同用户任意进程可调用 Ask（见 README 安全说明） |
