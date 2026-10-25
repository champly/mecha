# Mecha 后台常驻（daemon）改造架构方案

> 状态：设计中；Phase 1 MVP 已落地 `serve` / headless / 优雅退出（见 §0.1）
> 范围：让 `mecha` 在无 TTY、无前台 stdin/stdout 的环境下常驻运行，可优雅退出；`serve` 模式的日志写 stdout，由 systemd/launchd/nohup 重定向，`run` 模式保持工作区日志文件。
> 非目标：改变 agent 的 PTY/TUI 交互模型；改变 hook 协议；引入远程多租户或鉴权。

---

## 0. 结论摘要

| 决策点 | 结论 |
|---|---|
| 是否 fork/setsid daemonize | **不实现传统 fork daemonize**。由 systemd / launchd / supervisor 托管。Go 进程 `fork()` 不安全；`--detach` 仅作为 P2 的 re-exec+setsid 便利选项 |
| 新增命令 | `mecha serve`：前台常驻、headless、可托管；`mecha run` 保留原有交互语义 |
| 终端后端 | 新增 `pkg/term/headless`，直接 spawn 子进程 + 进程组管理，不依赖 tmux/iTerm2/Ghostty |
| 日志 | **serve MVP：Core 与 agentd 的 `slog` → stdout，由 supervisor 重定向，不创建日志文件**；`run` 保持 `~/.mecha/logs/<workspace>/<YYYY-MM-DD>.log`；每进程独立文件/轮转/0600 仅作为 serve 后续可选方案 |
| 配置来源 | flags > env > YAML > 默认值；集中在 `pkg/daemon.Options` 解析 |
| 信号 | Phase 1 MVP：SIGTERM/SIGINT 优雅退出；SIGHUP 重载/第二次信号强制退出为后续 |
| PID | flock 保护的单实例 pid 文件 + 原子写入的 state 文件（含实际 gRPC 地址） |
| 健康检查 | gRPC health service + 独立的 loopback HTTP admin（`/healthz`、`/readyz`、`/status`） |
| 端口 | `serve` 默认 gRPC `127.0.0.1:0`（state 文件发现），admin 默认 `127.0.0.1:7334`；可用配置固定 |
| Coordinator 退出 | `serve` **不启动 coordinator**，只提供 specialist `Ask`；`run` 保持“退出即结束”。后台监督/重启是后续功能 |
| daemon 输入通道 | 现状 `Ask` 拒绝 coordinator；后台化必须补一个输入入口。建议 P1 新增 `Chat` RPC + `mecha chat`，否则 `serve` 只能当 specialist 任务服务器 |
| 安全 | 强制 loopback（`127.0.0.0/8`、`::1`、`localhost`），不提供非 loopback 开关；serve 日志由 supervisor 重定向（可选加固为 0600 文件，后续） |

---

## 0.1 Phase 1 MVP 实现状态

| 能力 | 状态 |
|---|---|
| `mecha serve` 命令（`--workspace/--addr/--log-level/--shutdown-timeout`） | ✅ 已完成 |
| headless 终端后端（私有 PTY、`Setsid + Setctty`、poll I/O、进程组 TERM→grace→KILL） | ✅ 已完成 |
| Core `Options` / `NewWithOptions` / `Addr()` / `Run(ctx)` 与 `Serve=true` 模式 | ✅ 已完成 |
| `serve` 不启动 coordinator，仅提供 specialist `Ask` | ✅ 已完成 |
| agentd SIGTERM 优雅退出（进程组 TERM→grace→KILL + best-effort 后代扫描逐 PID 清理） | ✅ 已完成 |
| `serve` 日志统一 `slog` → stdout（不创建日志文件）；`run` 保持工作区日志文件 | ✅ 已完成 |
| `status` / `stop` / `chat` 命令 | ❌ 未完成 |
| HTTP admin（`/healthz`、`/readyz`、`/status`） | ❌ 未完成 |
| SIGHUP 配置重载与日志重开 | ❌ 未完成 |
| pid 文件 / state 文件 / 单实例保护 | ❌ 未完成 |
| `serve` 日志文件落盘、大小轮转、0600 权限 | ⏳ 后续可选，非 MVP 要求 |

MVP 限制：

- 没有 pid 文件，允许重复启动；多个实例必须使用不同 `--addr`，同一端口第二次绑定会失败。
- `--addr` 只接受 loopback；`127.0.0.1:0` 时端口是动态的。IPv6 必须使用方括号形式，例如 `[::1]:0`（裸写 `::1:0` 会被 Go 拒绝）。`OnListening` 会无条件向 stdout 打印 `mecha serve listening on <addr>`，即使日志级别为 warn/error 也能发现实际地址。
- `--shutdown-timeout` 最小 5s：agentd Close 预算 4s（2s SIGTERM 宽限 + 1s SIGKILL 后等待 + 1s webhook 关闭）+ 1s 预留，默认 10s。
- `serve` 不读 stdin、不提供 coordinator 输入通道；没有 `status/stop/chat`，停止请直接向进程发送 SIGTERM。

---

## 1. 当前交互式终端耦合清单

### 1.1 进程/生命周期

| 位置 | 现状 | 后台化问题 |
|---|---|---|
| `cmd/run.go:27-48` | `os.Getwd()` 作为 workspace；`signal.NotifyContext(SIGINT, SIGTERM)`；`core.Start(ctx)` 阻塞到 coordinator 退出 | daemon 的 cwd 可能是 `/`；无 SIGHUP；无两阶段优雅退出；coordinator 退出会带走整个服务 |
| `main.go:10-14` | 错误直接写 `os.Stderr` 并 `os.Exit(1)` | daemon 下 stderr 可能关闭/无处可去；启动早期错误不可观测 |
| `pkg/core/core.go:91-112` | 绑定 `127.0.0.1:0`；`launchCoordinator` 前台阻塞；返回后 `shutdown` | 地址不可发现；coordinator 退出即服务结束；没有 readdress/reload |
| `pkg/core/core.go:118-132` | `server.Stop()` 硬停；并行 kill specialist pane，总上限 1s | 没有等待 in-flight RPC；没有 coordinator 子进程的 graceful 信号；资源关闭顺序不完整 |
| `pkg/core/coordinator.go:29-35` | `cmd.Stdin/Stdout/Stderr = os.Stdin/out/err` | 强绑定当前 TTY；daemon 下无意义且可能写坏父进程终端 |
| `pkg/core/coordinator.go:43-46,63` | ctx 取消 `Process.Kill()`；`cmd.Wait()` 阻塞 | 无 SIGTERM→等待→SIGKILL；无进程组；无重启策略 |
| `pkg/core/specialist.go:53-57` | `backend.Spawn()` 依赖终端后端 | 无 TTY 时 `term.New()` 直接失败 |
| `pkg/agentd/agentd.go:127-130` | `Wait()` 等 `stop` | 没有 OS 信号入口；SIGTERM 默认直接杀进程，agent 可能残留 |
| `pkg/agentd/agentd.go:134-161` | Close 关 conn/webhook/log/PTY，再 `Process.Kill()` | 无 SIGTERM 优雅期；`Process.Kill` 不覆盖 agent 的子进程 |
| `pkg/agentd/task.go:34-46` | TaskChannel 断开即 `signalStop` | 合理，保留；daemon 下由 Core 重启链恢复 |

### 1.2 stdin/stdout/tty/终端 UI

| 位置 | 现状 | 后台化问题 |
|---|---|---|
| `pkg/term/term.go:23-40` | 自动检测 tmux → iTerm2 → Ghostty，全不匹配返回 `ErrUnsupported` | daemon/supervisor 环境无 `TMUX`/`TERM_PROGRAM`，Core 无法启动 |
| `pkg/term/tmux/tmux.go:34-37`、`ghostty/ghostty.go:55-58`、`iterm2/iterm2.go:47-50` | `Match()` 依赖环境变量/GUI | 同上 |
| `pkg/term/iterm2/iterm2.go:35,77` | `Label()` 直接写 `os.Stdout` 转义序列 | daemon 下 stdout 可能是日志/关闭，写终端序列无意义 |
| `pkg/agentd/process.go:61-64` | PTY 输出 `io.Copy(activityWriter{w: os.Stdout})` | daemon 下必须改为 discard 或独立 agent 输出日志 |
| `pkg/agentd/process.go:71-95` | `forwardStdin` 阻塞读 `os.Stdin` 并写 PTY | daemon 下 stdin 是 `/dev/null`，应立即 EOF；不应启动该 goroutine |
| `pkg/agentd/process.go:147-160` | `pty.GetsizeFull(os.Stdin)`，失败退化 24×80；`pty.StartWithSize` | headless 应显式使用配置尺寸，不探测 stdin；PTY 本身必须保留（agent 是 TUI） |
| `pkg/agentd/process.go:164-174` | `makeRawIfTerminal()` 改当前 stdin 终端模式 | daemon 下不能改任何共享终端；应显式 headless 跳过 |
| `pkg/agentd/process.go:200-222` | `SIGWINCH` + `os.Stdin` size → PTY | headless 无窗口尺寸来源，跳过 |
| `pkg/agentd` 历史实现 | agentd 按 workspace 自行派生日志文件 | 多进程写同一文件；无轮转；`serve` 下改为 stdout（`--log-stdout`），`run` 保持文件日志 |
| `cmd/webhook.go:31` | `io.ReadAll(cmd.InOrStdin())` | **不要改**：这是 agent hook 子进程的 stdin，不是 daemon 的 stdin |
| `cmd/ask.go:42` | 结果写 stdout | 客户端命令，保留；daemon 化不需要改 |

### 1.3 日志与配置（Phase 1 MVP）

| 位置 | 现状 | 说明 |
|---|---|---|
| `pkg/core`、`pkg/agentd` | `serve` 下 logger 注入，默认 `slog` text handler 写 stdout；`run` 使用工作区日志文件 | `serve` 不创建日志文件，由 supervisor 重定向；`run` 写 `~/.mecha/logs/<workspace>/<YYYY-MM-DD>.log` |
| `pkg/config/config.go` | YAML 只有 agents/profiles | `server/logging/runtime` 段尚未实现 |
| `cmd/serve.go` | `--log-level` 控制 slog level；实际监听地址无条件打印到 stdout | 动态端口在 warn/error 下也可发现 |

### 1.4 关键结论

1. **PTY 必须保留**：agent CLI 是 TUI，headless 不是“去掉 PTY”，而是“去掉 PTY 与外部 TTY 的桥接”。agentd 仍然用 `pty.StartWithSize` 拉起 agent。
2. **真正需要替换的是终端 pane 后端和 stdio 桥接**：新增 headless backend + agentd 的 headless 模式。
3. **daemon 的核心难点是生命周期**：稳定监听地址、coordinator 子进程监督、进程组回收、优雅退出、单实例、日志归属。

---

## 2. 目标架构

### 2.1 进程模型（serve 模式）

```
launchd / systemd / supervisor
  └─ mecha serve  (Core, 单实例, pidfile + statefile)
      ├─ admin HTTP 127.0.0.1:7334   /healthz /readyz /status
      ├─ gRPC Core  127.0.0.1:<addr>  api.Core + grpc health
      ├─ agentd(coordinator)  ← headless child (Setsid + Setctty)
      │    ├─ webhook HTTP 127.0.0.1:0
      │    └─ agent CLI ← PTY (Setsid + Setctty)
      └─ agentd(specialist)   ← headless child (Setsid + Setctty)
           ├─ webhook HTTP 127.0.0.1:0
           └─ agent CLI ← PTY
```

- `serve` 不读 stdin；所有诊断通过 `slog` 写 stdout，由 nohup/systemd/launchd 重定向，不创建日志文件。`run` 模式保持原有工作区日志文件。
- 所有子进程由 Core 统一回收；headless backend 用进程组信号保证 agentd 子树退出。
- headless backend 用 `SysProcAttr{Setsid: true, Setctty: true}` 拉起 agentd，因此 agentd 自身也是独立 session/进程组（`pgid == pid`）。agent 又由 agentd 通过 `pty.StartWithSize` 放入另一个独立 session，因此 **agentd 必须先 SIGTERM 优雅关闭 agent 进程组，再由 Core 对 agentd 做 SIGKILL 兜底**；SIGKILL 无法跨 session 回收 agent。agentd 还会在 `ps` 可用时对后代快照逐 PID 补发 TERM/KILL，覆盖工具自行 `setsid(2)` 出去的子进程；`ps` 不可用或格式不兼容时只保证进程组清理，硬保证仍依赖 systemd cgroup / launchd job 清理。

### 2.2 `run` vs `serve`

| 维度 | `mecha run`（默认，交互） | `mecha serve`（daemon） |
|---|---|---|
| 终端后端 | `auto`：tmux/iTerm2/Ghostty，缺失则报错 | 默认 `headless` |
| stdio | 直连当前终端 | 不读 stdin；日志写 stdout（supervisor 重定向） |
| gRPC 地址 | `127.0.0.1:0` | 配置，默认 `127.0.0.1:0` + state 文件 |
| admin HTTP | 关闭 | 默认开启 |
| coordinator 退出 | 整个进程退出（保留现状） | MVP 不启动 coordinator；后台监督/重启为后续 |
| pid/state | 不写 | 写 |
| 信号 | SIGINT/SIGTERM | SIGINT/SIGTERM/SIGHUP |
| 日志 | `~/.mecha/logs/<workspace>/<YYYY-MM-DD>.log` | stdout（向 specialist agentd 传 `--log-stdout`）；不创建日志文件，文件/轮转后续可选 |

建议实现上让 `run` 复用 `serve` 的 `Core.Run` 生命周期，只是传入不同 `Options`（`Interactive=true`、`RestartCoordinator=false`、`AdminAddr=""`）。

---

## 3. 关键设计

### 3.1 不实现 fork daemonize

- Go runtime 多线程下 `fork()` 后仅允许立即 `exec`；在库内模拟 `daemon(3)` 会死锁/丢锁。
- 推荐托管方式：
  - Linux：systemd **user** unit（需要 Keychain/凭据时应避免 system unit），`Restart=on-failure`、`KillMode=mixed`、`TimeoutStopSec=30`。
  - macOS：LaunchAgent（Aqua 用户会话，能访问 Keychain），`RunAtLoad=true`、`KeepAlive={SuccessfulExit:false}`、`ThrottleInterval=10`。
- 若确需无 supervisor 的“后台启动”，P2 提供 `mecha serve --detach`：父进程 `exec` 自身 + `Setsid` + stdio 重定向 + readiness pipe；**不 fork、不 setsid 当前进程**。该模式不提供自动重启/开机启动，文档需明确。
- `make install-service` 可作为 P2 便利目标，读取模板生成 plist/unit。

### 3.2 终端后端选择

```go
// pkg/term
type Options struct {
    Kind        string // "auto" | "headless" | "tmux" | "iterm2" | "ghostty"
    AllowHeadless bool // auto 模式下无终端时是否回退 headless
}
func New(opts Options) (Backend, error)
```

- `serve`：`Kind` 默认 `headless`，绝不因缺少 TTY 失败。
- `run`：`Kind` 默认 `auto`，保持 tmux → iTerm2 → Ghostty，缺失返回 `ErrUnsupported`（交互体验不变）。
- 显式指定 `tmux`/`iterm2`/`ghostty` 时，即使 `serve` 也允许（例如在 tmux 中手动跑 daemon 并保留 pane），但文档标注风险：终端关闭后 pane 消失。
- `headless` 不实现 `Label`（no-op）。

### 3.3 headless backend

```go
// pkg/term/driver
type Spec struct {
    WorkDir string
    Command []string
    Role    string
    Env     []string   // 追加环境变量，nil 表示继承
    Stdout  io.Writer  // 可选，nil => io.Discard
    Stderr  io.Writer
}
```

`pkg/term/headless`：

- `Spawn`：
  - `exec.CommandContext(ctx, spec.Command[0], spec.Command[1:]...)`
  - `Dir=WorkDir`；`Env = os.Environ() + spec.Env`
  - `SysProcAttr{Setsid: true, Setctty: true}`（PTY 为 controlling terminal；`setsid` 同时保证 `pgid == pid`，因此 `kill(-pid, ...)` 仍覆盖整个 headless 子树。不要同时设置 `Setpgid`，Darwin 上会 EPERM）
  - PTY master 设为非阻塞，使用 `poll` 读写；输出写入 `spec.Stdout`，stderr 可独立写入 `spec.Stderr`。Phase 1 MVP 由 `term.Options.Stdout/Stderr` 注入 `os.Stdout/os.Stderr`；轮转 writer 为后续可选
  - 不用 `CommandContext` 的默认取消语义；自行起 watchdog：SIGTERM 进程组 → 等待 grace → SIGKILL 进程组，避免 `WaitDelay` 只杀 leader、漏掉 agent 子进程
  - `Start` 后 goroutine `Wait`；leader 回收后仍保留 handle，直至整个进程组消失后再从 map 删除
- `Kill`：
  1. `syscall.Kill(-pid, SIGTERM)`；
  2. 等待 `grace`（默认 10s）或 ctx 截止；
  3. `syscall.Kill(-pid, SIGKILL)`；
  4. 回收 `Wait`。
- `Close`：对所有存活 handle 执行 Kill，等待，返回。
- Handle：`ID=role-pid`，`PaneID=strconv.Itoa(pid)`。

注意：`agentd` 的 agent 是独立 session，进程组信号打不到；必须依赖 agentd 自己的 SIGTERM handler。agentd 除 `kill(-pgid)` 外，还会在本机 `ps` 可用且兼容时 best-effort 扫描后代并逐 PID 终止，覆盖工具用 `setsid(2)` 脱离进程组的命令；`ps` 不可用时退回 group-only。在扫描前已 reparent 到 PID 1 的进程无法追溯。因此 `agentd` 的优雅退出是 P0，不可省略。

### 3.4 日志落盘与轮转（后续可选，非 MVP 要求）

> **Phase 1 MVP 的 `serve` 模式不落盘**：Core 和 agentd（`--log-stdout`）都使用
> `slog` 写 `os.Stdout`，由 nohup/systemd/launchd 重定向；`run` 模式保持原有工作区日志文件。以下“每进程独立文件 + 轮转 + 0600”是后续可选的
> 运维增强方案，不是当前实现要求。


#### 后续可选方案的目录与文件（Phase 1 MVP 当前不创建）

```
~/.mecha/logs/<workspace-key>/
  <process>-<role>-<id>.log       # 每个进程独立文件（后续方案，当前不创建）
  <agent>-<role>-<id>.out         # 可选：PTY 输出（agent_output=file，后续）
  launchd.out.log / launchd.err.log  # supervisor 捕获的崩溃输出（可选）
```

`workspace-key`：绝对路径去掉前导 `/`、`/`→`_`，再加 8 位 sha256 前缀，避免 `a/b` 与 `a_b` 冲突。旧路径 `<YYYY-MM-DD>.log` 不再用于 `serve`；`run` 可短期兼容。

#### 归属规则

- **禁止 Core 与 agentd 多进程写同一轮转文件**：轮转会互相踩踏。
- 后续若启用文件日志：Core 进程拥有自己的日志文件；每个 agentd 通过显式路径接收自己的文件。
- Phase 1 MVP 中 agentd 的 stdout/stderr（panic traces）经 headless backend 写入 `os.Stdout/os.Stderr`。
- PTY 输出默认 `discard`（TUI 控制序列巨大且通常无价值）；`agent_output=file` 时写独立轮转文件，默认 10 MiB × 2。

#### 轮转实现

新增 `pkg/logging`（stdlib，不引入新依赖）：

```go
type Config struct {
    Dir        string
    File       string        // 基础文件名
    Level      string        // debug|info|warn|error
    Format     string        // text|json
    MaxSize    int64         // 字节，<=0 关闭轮转
    MaxBackups int
    MaxAge     time.Duration
    Perm       os.FileMode   // 默认 0600
}

func New(cfg Config) (*slog.Logger, *Writer, error) // 进程级 logger
func NewWriter(cfg Config) (*Writer, error)         // 仅需要 io.Writer 时（如 agent PTY 输出）
func (w *Writer) Reopen() error                     // SIGHUP 用
func (w *Writer) Close() error
```

`Writer` 要点：

- `sync.Mutex` 保护 `file/size`；`O_CREATE|O_WRONLY|O_APPEND`，目录 `0700`，文件 `0600`。
- 写入前判断 `size+len(p) > MaxSize && size>0` → close → rename `<file>.<UTC时间戳>` → reopen → prune（保留 `MaxBackups` 个、清理超过 `MaxAge` 的）。
- 单条记录超过 `MaxSize` 时仍完整写入（避免截断日志）。
- `slog` handler：`AddSource=true`，`ReplaceAttr` 把 source 路径改为 basename；level 用 `slog.LevelVar` 以便 SIGHUP 动态调整。
- 不实现跨进程文件锁；设计上避免共享文件。
- 如果 coder 更倾向成熟方案，可替换为 `lumberjack`，但需保持“每进程独立文件”的约束。

### 3.5 配置来源与优先级

**优先级：显式 flag > 环境变量 > YAML > 内置默认值。**

新增 YAML 段（全部可选，旧配置无需修改）：

```yaml
server:
  addr: 127.0.0.1:0          # Core gRPC；仅允许 loopback
  admin_addr: 127.0.0.1:7334 # health/status HTTP（后续）

logging:
  dir: ""                    # 空 => ~/.mecha/logs/<workspace-key>
  level: info
  format: text               # text|json
  max_size_mb: 50
  max_backups: 5
  max_age_days: 14
  agent_output: discard      # discard|file

runtime:
  backend: headless          # auto|headless|tmux|iterm2|ghostty
  pid_file: ""               # 空 => ~/.mecha/run/<workspace-key>.pid
  state_file: ""             # 空 => ~/.mecha/run/<workspace-key>.json
  shutdown_timeout: 20s
  restart_coordinator: true
```

对应 Go 结构：

```go
type Config struct {
    Agent    string
    Agents   []AgentConfig
    Profile  string
    Profiles map[string]ProfileConfig
    Server   ServerConfig  `yaml:"server"`
    Logging  LoggingConfig `yaml:"logging"`
    Runtime  RuntimeConfig `yaml:"runtime"`
}
```

新增环境变量：

```
MECHA_CONFIG, MECHA_WORKSPACE, MECHA_PROFILE
MECHA_ADDR, MECHA_ADMIN_ADDR
MECHA_LOG_DIR, MECHA_LOG_LEVEL, MECHA_LOG_FORMAT, MECHA_AGENT_OUTPUT
MECHA_BACKEND, MECHA_PID_FILE, MECHA_STATE_FILE, MECHA_SHUTDOWN_TIMEOUT
```

解析集中在 `pkg/daemon/options.go`：

```go
type Options struct {
    Workspace          string        // 绝对路径
    ConfigPath         string
    Profile            string        // 覆盖 config.profile
    Addr               string
    AdminAddr          string
    LogDir             string
    LogLevel           string
    LogFormat          string
    AgentOutput        string
    Backend            string
    PidFile            string
    StateFile          string
    ShutdownTimeout    time.Duration
    RestartCoordinator bool
    Interactive        bool
}

func Resolve(in FlagValues, cfg config.Config) (Options, error) // FlagValues 见 4.3
func (o Options) Validate() error
```

`Validate()` 负责：workspace 存在且绝对化、loopback 校验、backend 合法、时长合法、agent_output 合法、HOME 可用。

`--workspace` 解析规则：
1. flag/env 指定 → `filepath.Abs` + `EvalSymlinks`（失败则 Abs）+ 目录存在校验；
2. 否则 `os.Getwd()`；若为 `/` 或不可读 → 报错并提示 `--workspace`。
daemon 绝不能静默把 `/` 当成 workspace。

### 3.6 PID 文件与 state 文件

路径：

```
~/.mecha/run/<workspace-key>.pid    # flock 锁文件，内容为 pid
~/.mecha/run/<workspace-key>.json   # 启动成功后写入的运行状态
```

```go
// pkg/daemon/pidfile.go
type PIDFile struct {
    Path string
    f    *os.File
}
var ErrAlreadyRunning = errors.New("daemon: already running")

func AcquirePIDFile(path string) (*PIDFile, error)
func (p *PIDFile) Close() error
```

- `AcquirePIDFile`：`O_RDWR|O_CREATE` 0600 → `flock(LOCK_EX|LOCK_NB)`；失败读 pid 返回 `ErrAlreadyRunning{PID}`；成功后 `truncate` + 写 pid，fd 保持打开直到进程退出。
- 启动顺序：**先拿 pid 锁，再 gRPC listen，再 admin listen，全部成功后才写 state**。
- `Close`：`unlink` 后 `flock(LOCK_UN)` + close（best effort）。
- 不支持 Windows；项目本身依赖 pty/osascript，Unix-only 可接受。

```go
// pkg/daemon/state.go
type State struct {
    PID            int       `json:"pid"`
    Addr           string    `json:"addr"`
    AdminAddr      string    `json:"admin_addr,omitempty"`
    Workspace      string    `json:"workspace"`
    Profile        string    `json:"profile"`
    Version        string    `json:"version"`
    StartedAt      time.Time `json:"started_at"`
    Backend        string    `json:"backend"`
}

func WriteState(path string, s State) error  // temp + rename, 0600
func ReadState(path string) (State, error)
func RemoveState(path string) error
```

`mecha ask` 在 `--addr` 为空时读取当前 workspace 的 state 文件；读不到再提示传 `--addr`。保持向后兼容（传了 `--addr` 优先）。`ask`/`status`/`stop` 都提供 `--workspace`，避免 cwd 与 daemon workspace 不一致时找不到 state。

`mecha status` / `mecha stop`（P1）：

- `status`：读 state → 检查 pid 存活（`kill(pid,0)`）→ 可选 HTTP `/status` → 打印 JSON 或表格。
- `stop`：读 state → `SIGTERM` → 轮询退出（默认 30s）→ 超时提示；不直接 SIGKILL，交给 supervisor / 用户。

### 3.7 信号处理

`cmd/serve.go`：

| 信号 | 行为 |
|---|---|
| SIGTERM / SIGINT | 第一次：进入优雅关闭（见 3.9），退出码 0；第二次：日志后 `os.Exit(130/143)` |
| SIGHUP | 后续：重载配置文件 + `logging.Reopen()` + `core.Reload(cfg)`；失败保留旧配置并记录 error；1s 内去抖 |
| SIGQUIT | 保留 Go 默认 stack dump（由 supervisor 捕获 stderr） |
| SIGWINCH | serve 忽略；agentd 交互模式转发 PTY，headless 忽略 |

`cmd/agentd.go`：

- `cmd/agentd.go` 在 `Start()` 之前建立 `signal.NotifyContext(ctx, SIGINT, SIGTERM)`，并把它作为 `Options.Context` 传入；`Register`/`TaskChannel` 启动 RPC 使用该 ctx + 10s 超时。
- 收到信号：`signalStop` → 先完整清理 agent（进程组 SIGTERM，2s grace，SIGKILL，1s 有界回收；同时 best-effort 扫描后代并逐 PID 发送 TERM/KILL）→ 取消 TaskChannel → 关闭 webhook（最多 1s）→ 关闭 Core conn。
- `Close` 自身也调用 `signalStop`；总预算 4s（2+1+1），Core 最小 `ShutdownTimeout=5s`，留 1s 余量。
- agentd 不处理 SIGHUP（无配置可重载）。

SIGHUP 重载语义（必须写进文档）：

- 生效：日志级别/格式、新 spawn 的 specialist 的 role/agent 配置、后续 `Register` 返回的配置。`profile` 覆盖需重新执行校验/`complete()`，实现上建议 `config.LoadConfigWithProfile(path, name, validType)`。
- 不生效：已存在 agent/agentd 的 prompt、模型、env；监听地址/backend/pid 路径变更需重启。
- 若 coordinator role 或 prompt 变更，默认不自动重启 coordinator；P2 提供 `mecha restart-coordinator` 或 `serve --reload-restarts-coordinator`。

### 3.8 监听地址、健康检查与状态

#### Core gRPC

- `server.addr` / `--addr`；`serve` 默认 `127.0.0.1:0`，启动后通过 `OnListening` 暴露实际地址（MVP 无 state 文件）。
- 校验：`cmd/serve.go` 与 `Core.listen` 双重调用 `ValidateLoopbackAddr`；仅接受 `127.0.0.0/8`、`::1`、`localhost`（解析出的所有 IP 都必须是 loopback）。空 host、`0.0.0.0`、`::`、公网 IP 一律报错，不提供 `allow_non_loopback` 开关。原因：无鉴权的 Ask 可驱动带 `--dangerously-skip-permissions` 的 agent。
- 注册 `grpc_health_v1`：
  - `""`/`"api.Core"`：启动后 `SERVING`，shutdown 时 `NOT_SERVING`。
  - 可选：coordinator 未 ready 时 `"api.Core"` 返回 `NOT_SERVING`，把 readiness 语义交给 `/readyz` 即可。

#### Admin HTTP

```go
// pkg/admin
type Snapshot struct {
    PID         int
    Addr        string
    Workspace   string
    Profile     string
    State       string           // starting|ready|degraded|stopping
    StartedAt   time.Time
    Coordinator *InstanceSnapshot
    Specialists []InstanceSnapshot
    LastError   string
}
type InstanceSnapshot struct {
    Role, ID, State string
    Restarts        int
    LastError       string
}

func New(addr string, snapshot func() Snapshot, logger *slog.Logger) (*Server, error)
func (s *Server) Addr() string
func (s *Server) Close(ctx context.Context) error
```

端点：

| 路径 | 语义 |
|---|---|
| `GET /healthz` | 进程存活且未进入 stopping → 200；否则 503 |
| `GET /readyz` | coordinator 已 register + ready，且 gRPC 可用 → 200；启动中/重启中 → 503 + JSON reason |
| `GET /status` | 200 JSON：进程、地址、profile、coordinator、specialists、restarts、last_error |

- 默认 `serve` admin `127.0.0.1:7334`；冲突时启动失败并提示改 `--admin-addr`。`run` 默认关闭。
- 只读、无鉴权、仅 loopback；不提供写操作。
- HTTP server 超时：`ReadHeaderTimeout=5s`、`ReadTimeout=10s`、`WriteTimeout=10s`、`IdleTimeout=60s`。
- `Snapshot()` 只做内存读取，禁止阻塞在网络/子进程等待上。

### 3.9 优雅关闭顺序（Phase 1 MVP 实现）

`Core` 的关闭由 `sync.Once` 保护，幂等；ctx 取消（SIGTERM/SIGINT）时：

1. `Core.Run` 从 `ctx.Done()` 返回；`shutdown()` 立即调用 `grpc.Server.Stop()`。MVP **不使用 `GracefulStop`**：`TaskChannel` 是常驻双向流，GracefulStop 会挂起等待；直接 Stop 会断开流并促使 agentd 自行退出。
2. 所有 specialist pane 并行 `backend.Kill`，共享同一个 `ShutdownTimeout` 预算（`Core.Options.ShutdownTimeout`，默认 10s，最小 5s）。headless backend 先向进程组发 SIGTERM，等待 agentd 的 2s 宽限期，再 SIGKILL；ctx 到期会强制升级到 SIGKILL。agentd 自身在 2s+1s 预算内先对 `ps -axo pid=,ppid=` 快照中的 agent 后代逐个 PID 发 SIGTERM，宽限期内每 150ms 重扫并补发 TERM，SIGKILL 阶段重扫后对快照并集逐 PID SIGKILL，同时保留 `kill(-pgid)` 兜底。单次扫描的 deadline 为 `min(phaseDeadline, now+300ms)`；`ps -axo` 失败时尝试 `ps -A -o pid=,ppid=`，两者都失败才 Warn（每次 shutdown 最多一次）并退回进程组行为。
3. `backend.Close()`；Close 的错误会被记录，不静默丢弃。
4. 最后记录 `core shutdown complete`（顺序在 backend.Close 之后）。

`serve` 不启动 coordinator，因此没有 coordinator 终止步骤。`run` 模式保持现有交互语义：coordinator 由前台 `launchCoordinator` 管理，ctx 取消时沿用原有 `Process.Kill()`。

预算说明：`--shutdown-timeout` 必须 >= 5s：agentd Close 预算为 4s（2s SIGTERM grace + 1s SIGKILL 后等待 + 1s webhook Close），Core 再留 1s reserve。**SIGTERM 会中止在途任务，不会等待 30min 的任务超时**。

agentd `Close` 内部顺序固定为：`signalStop` → 完整 stopAgent（进程组 TERM→2s→KILL→1s 回收，叠加 best-effort 后代扫描与逐 PID TERM/KILL）→ 取消 TaskChannel → webhook Close（最多 1s）→ Core conn Close。agent 位于独立 session，Core 无法跨 session 回收，因此 **agent 清理必须先于 webhook/conn 等可能阻塞的步骤**。

残留风险：若 agent 的后代在第一次 `ps` 扫描前就已经 `setsid(2)` 并 reparent 到 PID 1，`ps` 进程树已无法从 agent root 追溯，agentd 也不会跨 PID 1 全局搜索，这类进程仍可能残留；同理，在两次扫描间隔内新 spawn 且父进程先退出、被 reparent 到 PID 1 的后代也可能漏掉。扫描依赖本机有可用的 `ps`；BusyBox 等实现可能不支持 `-axo`，会先尝试 `-A -o pid=,ppid=`，仍失败则只保证进程组清理。PID 复用窗口约为 `agentStopGrace+agentKillWait`（默认 ~3s，因为初始快照会保留到 SIGKILL 阶段），只对快照中出现过的 PID 发信号，属 best-effort。需要强保证时应依赖 systemd/launchd 的 cgroup/job 级清理。

实现注意：`grpc.Server.Serve` 在 Stop 后返回 `grpc.ErrServerStopped`，该错误会被过滤；非预期 Serve 错误通过 channel 返回给 `Run`，不会让进程在服务已死后继续阻塞到 ctx 取消。

### 3.10 Coordinator 监督（后续设计；Phase 1 MVP 不启用）

> Phase 1 MVP 的 `serve` **不启动 coordinator**，因此本节不参与当前实现。
> `run` 模式仍前台运行 coordinator，不自动重启。以下为后续阶段的监督方案。

`pkg/core/coordinator.go` 后续可改为循环：

```go
func (c *Core) superviseCoordinator(ctx context.Context) error {
    backoff := time.Second
    for {
        err := c.runCoordinatorOnce(ctx)
        if ctx.Err() != nil { return nil }
        if !c.opts.RestartCoordinator { return err }
        c.setCoordinatorState(degraded, err)
        c.logger.Error("coordinator exited, restarting", "err", err, "backoff", backoff)
        select {
        case <-ctx.Done(): return nil
        case <-time.After(jitter(backoff)):
        }
        backoff = min(backoff*2, 60*time.Second)
        // runCoordinatorOnce 成功 ready 持续 60s 后重置 backoff
    }
}
```

- `runCoordinatorOnce`：spawn `mecha agentd` → `waitRegistered(5s)` → `waitReady(30s)` → 标记 ready → `Wait`。
- 重启不会影响已存在的 specialist；`/readyz` 在重启期间 503。
- `run` 模式 `RestartCoordinator=false`，保持现有“coordinator 退出即整体退出”。
- 连续失败时日志限流（避免刷爆日志）；`/status` 暴露 `restarts` 和 `last_error`。

### 3.11 agentd headless 改造

```go
// pkg/agentd（Phase 1 MVP 实际接口）
type Options struct {
    ID       string
    CoreAddr string
    Logger    *slog.Logger // nil => LogStdout ? stdout : workspace log file
    LogStdout bool         // serve 模式：日志写 stdout，不建文件
    Context   context.Context // nil => Background；启动 RPC 和连接生命周期
}

func New(opts Options) *Agentd
func (a *Agentd) Start() error
func (a *Agentd) Stop()
func (a *Agentd) Wait()
func (a *Agentd) Close()
```

- `cmd/agentd.go` 暴露 `--id` / `--addr` / `--log-stdout`，在 `Start()` 前建立 `signal.NotifyContext(SIGINT, SIGTERM)` 并注入 `Options.Context`。
- `Register` 与 `TaskChannel` 建流使用该 ctx + 10s 启动超时；启动阶段收到 SIGTERM 会快速退出。
- `serve`（`--log-stdout`）下日志统一 `slog` → stdout，不创建/关闭日志文件；默认（`run`）写工作区日志文件。
- `startAgent` 用 `pty.StartWithSize` 启动 agent；agent 是独立 session/进程组。
- 退出：
  - 单一 `cmd.Wait()` 所有者（`waitAgent`），避免重复 Wait。
  - `waitAgent` 记住 `agentPID`，leader 退出后仍可清理后代进程组。
  - 任务超时/失败 → `signalStop`；supervise 收到 stop 后尽力 `ReportStatus(exited)`（失败仅 Warn），随后 Close。
- `Close` 顺序与预算：
  1. `signalStop()`（保证直接调用 Close 也能唤醒 Wait）；
  2. `stopAgent()`：进程组 SIGTERM → 2s grace → SIGKILL → 1s 有界回收；
  3. 取消 TaskChannel；
  4. webhook `Shutdown`，最多 1s；
  5. Core gRPC conn Close。
  总预算约 4s；Core 最小 `ShutdownTimeout=5s`，先等 agentd 完成，再对 agentd 组做 SIGKILL 兜底。webhook/conn 错误均记录 `slog.Warn`。

### 3.12 环境变量与启动预检

supervisor 启动的环境通常很干净，必须显式处理：

- `HOME` 缺失 → 启动失败（agent 配置/认证依赖 HOME）。
- `PATH` 缺失/不包含 agent 二进制 → 预检失败；鼓励在 `agents[].binary` 写绝对路径。
- `TERM` 缺失 → 为 agent 环境注入 `TERM=xterm-256color`（`BuildEnv` defaults 或 agentd spawn 时）；不要覆盖用户显式值。
- `LANG` 缺失 → 可注入 `en_US.UTF-8`（可选，文档说明）。
- macOS Keychain：必须用 LaunchAgent（用户 Aqua 会话），不能用系统 LaunchDaemon。
- 预检项：config 可读且校验通过、profile/coordinator 存在、所有 active role 的 agent binary 可执行、workspace 可写、日志/run 目录可创建、监听地址可绑定、pid 锁可获取。
- 预检失败：写日志 + stderr，退出码 1，不写 state，不启动子进程。

### 3.13 关键产品语义：daemon 模式下 coordinator 的输入从哪来

这是本次改造**必须先确认的产品边界**，否则会出现“服务常驻但没人能对它说话”的空转。

现状：

- `pkg/core/specialist.go:28-34` 的 `ensureSpecialist` 明确拒绝 coordinator role（`FailedPrecondition`）。
- 因此现有 `Ask` 只能驱动 specialist；coordinator 只能通过 agentd 的 stdin 转发接收用户键盘输入。
- daemon 没有 stdin，coordinator 会启动并一直空闲；`serve` 实际只能作为“specialist 任务服务器”。

建议方案（P1，推荐）：

1. **新增 `Chat` RPC，不动 `Ask` 语义**（保持向后兼容）：
   ```proto
   message ChatRequest {
     string role = 1;    // 空 => coordinator
     string message = 2;
   }
   message ChatResponse {
     bool success = 1;
     string result = 2;
   }
   service Core {
     // ... 现有 rpc
     rpc Chat(ChatRequest) returns (ChatResponse);
   }
   ```
2. Core 侧实现 `ensureCoordinator(ctx)`：复用 `instance`/`execute` 的任务机制；
   - coordinator 健康 → 直接复用；
   - coordinator 正在重启 → 返回 `Unavailable`，客户端可重试；
   - coordinator 未启动/已退出且 supervision 未覆盖 → 用与 specialist 相同的 spawn 工厂启动。
3. 新增 CLI：
   ```bash
   mecha chat --addr 127.0.0.1:<port> "请评估当前服务状态"
   mecha chat --role architect "..."   # 等价于 ask，便于统一脚本
   ```
4. 并发与超时：复用 per-instance `taskMu` 串行；默认 30min（`api.TaskTimeout`），CLI 可 `--timeout`；结果来自 hook `Stop` 的 `Output`，不是 PTY 原始输出。
5. **Phase 1 MVP 的 `serve` 固定为纯 specialist 任务服务器（不提供 `--no-coordinator` flag）**；此时 `/readyz`（后续实现）只要求 gRPC 可用，不要求 coordinator。

若产品上只需要后台 specialist、不需要对话 coordinator，则：
- `serve` 可以不启动 coordinator（`--no-coordinator`），直接暴露 `Ask`；
- 文档必须明确 coordinator 角色在 daemon 模式下不可用。

不推荐的做法：

- 直接放开 `Ask` 接受 coordinator：会破坏现有“Ask 只用于 specialist”的不变量和测试语义。
- 让用户 `attach` 到 PTY 再输入：仍依赖终端，违背本次目标（可作为 P3 的调试功能）。

### 3.14 `serve` 启动与失败顺序（Phase 1 MVP 实现）

1. `cmd/serve.go` 解析 `--workspace`（默认 cwd，必须是已存在目录）、`--addr`、`--log-level`、`--shutdown-timeout`。
2. 校验 `--addr` 为 loopback；校验 `--shutdown-timeout >= 5s`。
3. 加载配置；`term.New(term.Options{Kind:"headless", Stdout:os.Stdout, Stderr:os.Stderr})`，让 agentd slog、启动错误和 panic 在终端可见（Core spawn specialist agentd 时附带 `--log-stdout`）。
4. `slog.SetDefault`（stdout text handler）；`core.NewWithOptions` 注入 backend/logger/addr/timeout/`Serve:true`/`OnListening`。
5. 建立 SIGINT/SIGTERM `signal.NotifyContext`；`core.Run(ctx)` 内部 listen gRPC。
6. `OnListening` 中无条件向 stdout 打印 `mecha serve listening on <addr>`，不受日志级别影响。
7. `Serve=true`：不启动 coordinator，阻塞到 ctx 取消或非预期 gRPC Serve 错误。
8. 关闭：gRPC `Stop` → 并行 pane Kill（共享 `ShutdownTimeout`）→ `backend.Close` → 记录 `core shutdown complete`。pane 清理受 `ShutdownTimeout` 约束；backend Close 是之后由 backend 自身实现界定的最终升级。
9. listen 失败也调用 `shutdown()` 关闭注入的 backend；`cmd/serve.go` 另有幂等 `defer backend.Close()` 兜底。

MVP 不创建 pid/state/admin/log 文件，也不安装 SIGHUP。任一步失败返回可操作错误，不留下监听或子进程。

---

## 4. 关键接口设计（供 coder 直接实现）

### 4.1 `pkg/core`

```go
type Options struct {
    Workspace       string
    Config          config.Config
    Backend         term.Backend // nil => term.New()
    Addr            string       // "" => 127.0.0.1:0
    Logger          *slog.Logger // nil => stdout text handler
    ShutdownTimeout time.Duration
    Serve           bool // true => 不启动 coordinator，Run 阻塞到 ctx 取消
    OnListening     func(addr string) // net.Listen 成功后回调实际地址
}

type Core struct { /* ... */ }

func NewWithOptions(opts Options) (*Core, error)
func New(workspace string, cfg config.Config) (*Core, error) // 兼容 run 模式
func (c *Core) Start(ctx context.Context) error // run 模式
func (c *Core) Run(ctx context.Context) error   // serve/run 通用
func (c *Core) Addr() string
```

- `serve` 模式下 `Core` 不创建/关闭日志文件，logger 由 cmd 注入，默认写 stdout；兼容构造函数 `New` 在 `run` 模式打开并持有工作区日志文件。
- `Core.Run`/`Start` 负责关闭传入的 backend（所有权转移给 Core）；listen 失败同样走 shutdown。
- `ShutdownTimeout` 是所有 specialist pane 共享的总预算；`<=0` 取 10s，`<5s` 提升到 5s。

### 4.2 `pkg/term`

```go
type Backend = driver.Backend
type Options struct {
    Kind          string
    AllowHeadless bool
    Stdout        io.Writer // 仅 headless 使用；nil => discard
    Stderr        io.Writer // 仅 headless 使用；nil => 合并到 PTY
}
func New(opts ...Options) (Backend, error) // 无参等价 Options{Kind:"auto"}
```

`driver.Spec` 增加 `Env/Stdout/Stderr`（见 3.3）。tmux/iTerm2/Ghostty 忽略新字段。

### 4.3 `pkg/daemon`

```go
type Options struct { /* 见 3.5 */ }
// FlagValues 由 cmd 层填充，pkg/daemon 不依赖 cobra。
type FlagValues struct {
    Config, Workspace, Profile                string
    Addr, AdminAddr                           string
    LogDir, LogLevel, LogFormat, AgentOutput  string
    Backend, PidFile, StateFile               string
    ShutdownTimeout                           time.Duration
    RestartCoordinator                        *bool
    Interactive                               bool
}

func Resolve(in FlagValues, cfg config.Config) (Options, error)
func (o Options) Validate() error

type PIDFile struct { /* ... */ }
func AcquirePIDFile(path string) (*PIDFile, error)

type State struct { /* 见 3.6 */ }
func WriteState(path string, s State) error
func ReadState(path string) (State, error)
func RemoveState(path string) error
func DefaultPIDPath(workspace string) (string, error)
func DefaultStatePath(workspace string) (string, error)
func WorkspaceLogDir(workspace, override string) (string, error)
```

### 4.4 `pkg/logging`

```go
type Config struct { /* 见 3.4 */ }
func New(cfg Config) (*slog.Logger, *Writer, error)
func NewWriter(cfg Config) (*Writer, error)
func (w *Writer) Reopen() error
func (w *Writer) Close() error
```

### 4.5 `pkg/admin`

```go
type Snapshot struct { /* 见 3.8 */ }
func New(addr string, snapshot func() Snapshot, logger *slog.Logger) (*Server, error)
func (s *Server) Addr() string
func (s *Server) Close(ctx context.Context) error
```

---

## 5. 文件清单与改动要点

> 下表是完整设计范围。Phase 1 MVP 实际落地的是 `pkg/term/headless`、Core 的
> serve 模式、agentd 优雅退出与 `cmd/serve.go`；`pkg/logging`、`pkg/daemon`、
> admin/pid/state 等属于后续阶段。日志落盘/轮转不作为 MVP 要求。


### 5.1 新增

| 文件 | 要点 | 优先级 | 风险 |
|---|---|---|---|
| `pkg/logging/logging.go` | slog 构造、level/format、`Writer.Reopen` | P2（后续可选） | 中 |
| `pkg/logging/rotate.go` | 大小轮转、保留/清理、并发安全 | P2（后续可选） | 中（丢日志） |
| `pkg/logging/rotate_test.go` | 轮转/并发/超大单条测试 | P2（后续可选） | 低 |
| `pkg/daemon/options.go` | flags/env/YAML 优先级、workspace 绝对化、校验 | P0 | 中 |
| `pkg/daemon/pidfile.go` | flock 单实例、stale 处理 | P0 | 中 |
| `pkg/daemon/state.go` | 原子 state 读写 | P0 | 低 |
| `pkg/daemon/*_test.go` | 锁竞争、state、优先级 | P0 | 低 |
| `pkg/term/headless/headless.go` | 进程组 spawn/kill、输出重定向 | P0 | 高 |
| `pkg/term/headless/headless_test.go` | 子进程树回收、ctx 取消、grace | P0 | 中 |
| `pkg/admin/server.go` | `/healthz` `/readyz` `/status` | P1 | 低 |
| `pkg/api/api.proto` + 生成代码 | 新增 additive `Chat` RPC（若采纳 3.13） | P1 | 中 |
| `pkg/admin/server_test.go` | 状态码/JSON | P1 | 低 |
| `cmd/serve.go` | 常驻入口：预检、日志、pid/state、信号、admin、core.Run | P0 | 高 |
| `cmd/chat.go` | daemon 对话入口（P1 产品接口） | P1 | 低 |
| `cmd/status.go` | 读 state + `/status` | P1 | 低 |
| `cmd/stop.go` | SIGTERM + 等待 | P1 | 低 |
| `deploy/systemd/mecha.service` | user unit 模板 | P1 | 低 |
| `deploy/launchd/com.champly.mecha.plist` | LaunchAgent 模板 | P1 | 低 |
| `docs/DAEMON.md` | 运维手册：安装、启停、日志、排障 | P1 | 低 |

### 5.2 修改

| 文件 | 改动要点 | 优先级 | 风险 |
|---|---|---|---|
| `cmd/root.go` | 注册 `serve`/`status`/`stop`；保留 `mecha` 默认 `run`；新增持久 flags 或命令级 flags | P0 | 低 |
| `cmd/run.go` | 复用 serve 生命周期；`Interactive=true`、`Backend=auto`、`RestartCoordinator=false`、admin 关闭；保持原行为 | P0 | 中 |
| `cmd/agentd.go` | 仅保留 `--id/--addr`；`signal.NotifyContext(SIGINT, SIGTERM)`；注入 `Options.Context`；`Start/Stop/Wait` | P0 | 中 |
| `cmd/ask.go` | `--addr` 可选：缺省读当前 workspace state；保留显式 addr | P1 | 低 |
| `main.go` | 保持 CLI 错误 stderr；必要时增加已定义退出码（1 启动/运行，3 已运行） | P0 | 低 |
| `pkg/config/config.go` | 新增 `Server/Logging/Runtime` 段与默认值；新增 workspace-key 日志路径/run 路径 helper；`NewFileLogger` 改为兼容 wrapper 或删除 | P0 | 中 |
| `pkg/config/config.yaml` | 增加注释掉的 `server/logging/runtime` 示例，保持旧配置可用 | P1 | 低 |
| `pkg/core/core.go` | `Options/New/Run/Addr/Reload/Snapshot`；可配置监听；graceful shutdown；`serve` 不再持有日志文件（`run` 兼容构造函数保留文件日志）；health service | P0 | 高 |
| `pkg/core/coordinator.go` | headless 子进程（stdio 重定向）；进程组 SIGTERM→SIGKILL；重启循环 + 退避；单一 Wait 所有者 | P0 | 高 |
| `pkg/core/specialist.go` | Spawn 传 `Env/Stdout/Stderr`；日志路径下发；配置快照读取 | P0 | 中 |
| `pkg/core/server.go` | `Ask` 拒绝 stopping；新增 `Chat`（P1）；Register/ReportStatus 用配置快照；状态快照 | P0 | 中 |
| `pkg/core/instance.go` | 暴露 Snapshot（state/role/id/restarts）；不改变任务状态机 | P1 | 中 |
| `pkg/core/registry.go` | `snapshot()` 返回稳定排序的角色状态 | P1 | 低 |
| `pkg/agentd/agentd.go` | `Options{ID,CoreAddr,Logger,LogStdout,Context}`；Start/Stop/Wait/Close；Close 先清理 agent 进程组再关 webhook/conn；`serve` 日志 stdout，`run` 写工作区日志文件 | P0 | 高 |
| `pkg/agentd/process.go` | 输出 sink；headless 跳过 stdin/raw/winch；PTY 尺寸显式；agent 进程组优雅 kill | P0 | 高 |
| `pkg/agentd/webhook.go` | `Close` 的 `Shutdown` 最多 1s，超时强制关闭连接，避免阻塞 agent 清理 | P0 | 中 |
| `pkg/term/term.go` | `New(Options)` 显式选择 + headless 注册；保留 `New()` 兼容 wrapper | P0 | 中 |
| `pkg/term/driver/driver.go` | `Spec` 增加 `Env/Stdout/Stderr` | P0 | 低 |
| `pkg/term/iterm2/iterm2.go` | headless 不调用；`Label` 仅在交互模式；无需大改 | P1 | 低 |
| `README.md` / `README.zh-CN.md` | 新增后台运行章节、命令表、`serve` 日志 stdout 与 `run` 日志文件、仅 loopback 与关闭预算说明 | P1 | 低 |
| `docs/DESIGN.md` | 更新进程模型、日志、生命周期、信号、headless | P1 | 低 |
| `Makefile` | 可选 `build` 后提示；新增 `service-install`（P2） | P2 | 低 |

### 5.3 明确不改

- `cmd/webhook.go`：hook 子进程 stdin 语义必须保留。
- `cmd/ask.go` 的输出格式（无结尾换行）必须保留，避免破坏 coordinator prompt/脚本。
- `pkg/api/*.proto`：`Ask` 及现有消息不变；若采纳 3.13，仅新增 `Chat` RPC/消息（additive，兼容旧客户端）。
- 任务状态机、hook 事件解析、agent provider 的 CLI 参数拼装：不改。

---

## 6. 实施步骤

### Phase 0：基础件（可独立合并）

1. （P2 可选）`pkg/logging` + 测试：轮转、并发、Reopen。Phase 1 MVP 的 `serve` 使用 stdout，不落盘。
2. `pkg/daemon`：Options 解析、PID flock、state 原子读写 + 测试。
3. `pkg/term/headless` + `driver.Spec` 扩展 + 测试。
4. `pkg/config` 新配置段与默认值；保留旧字段兼容。

**验收**：`go test ./pkg/logging ./pkg/daemon ./pkg/term/headless ./pkg/config` 通过；旧 `mecha run` 不受影响。

### Phase 1：Core 生命周期重构 + serve 入口

1. `pkg/core` 引入 `Options/New/Run/Addr/Reload/Snapshot`；`Core.cfg` 原子快照；监听地址可配置；gRPC health。
2. `coordinator.go`（后续）：serve 不启动 coordinator；run 保持现状。headless spawn/进程组优雅终止/重启循环留给后续阶段。
3. `cmd/serve.go`：预检 → 日志 → pid 锁 → core.Run → 信号 → state → admin → 清理。
4. `cmd/agentd.go` + `pkg/agentd`：headless、信号、优雅退出。
5. `cmd/run.go` 迁移到新生命周期，保持交互行为。

**验收**：无 TTY 下 `setsid mecha serve ... </dev/null` 常驻；`mecha ask` 可用；SIGTERM 后无残留进程。

### Phase 2：可观测性与运维

1. `pkg/admin` + `/healthz` `/readyz` `/status`。
2. `cmd/status.go`、`cmd/stop.go`；`ask` 自动发现 state。
3. SIGHUP 配置重载 + 日志 Reopen。
4. deploy 模板、DAEMON.md、README/DESIGN 更新。

**验收**：systemd/launchd 启停；`/readyz` 在 coordinator 重启时正确 503→200；SIGHUP 生效。

### Phase 3（可选）：体验增强

1. `mecha serve --detach`（re-exec + setsid + readiness pipe）。
2. `mecha attach <role>`（读取 agent PTY 输出 ring buffer）。
3. `/metrics`、`restart-coordinator` 子命令、`--reload-restarts-coordinator`。
4. 孤儿 agent 清理（启动时按 state/pid 对账）。

---

## 7. 验收标准

### 7.1 功能验收（Phase 1 MVP）

1. **无 TTY 常驻**
   ```bash
   # Linux / macOS
   nohup ./mecha serve --workspace /path/to/project \
     --config ~/.mecha/config.yaml --log-level info \
     </dev/null >/tmp/mecha.out 2>&1 &
   disown
   ```
   父 shell 退出后进程仍在；`ps -o tty= -p <pid>` 为空；不读取 stdin。
2. **地址发现**
   - `127.0.0.1:0` 启动后 stdout 无条件出现 `mecha serve listening on 127.0.0.1:<port>`，`--log-level error` 时也一样。
   - `0.0.0.0:0`、`[::]:0`、`:0`、非 loopback IP 启动失败，退出码 1。
3. **任务可用**
   ```bash
   ./mecha ask --addr 127.0.0.1:<port> <specialist-role> "task"
   ```
   `serve` 不启动 coordinator；coordinator 角色由 `Ask` 拒绝。
4. **日志可见**
   specialist agentd 通过 `--log-stdout` 把 slog 写到 stdout，启动失败和 panic 经 headless backend 转发到 `serve` 的 stdout/stderr；`serve` 不创建日志文件。
5. **优雅退出**
   ```bash
   kill -TERM <pid>
   ```
   在没有在途任务时退出码 0；有在途任务时在 `--shutdown-timeout` 内退出，agent 进程组不残留。
6. **交互模式回归**
   在 tmux/iTerm2/Ghostty 中 `mecha`：pane 正常创建、键盘输入转发、coordinator 退出后清理 pane，行为与改造前一致。

以下为后续阶段验收，不属于 Phase 1 MVP：`chat`/`status`/`stop`、pid/state 单实例、SIGHUP 重载、admin `/healthz` `/readyz` `/status`、coordinator 崩溃重启、日志文件轮转、supervisor 模板。

### 7.2 质量门禁

- `go test ./...` 全绿。
- `go vet ./...` 通过。
- `gofmt -l .` 为空。
- Phase 1 MVP 新增测试覆盖：headless 进程树回收、controlling terminal、有界 Wait、Core 关闭预算、loopback 校验、agentd 启动取消。
- 手工验收覆盖 `serve` + `ask` + SIGTERM + 进程组无残留。轮转、pid/state、SIGHUP、admin、coordinator 重启属于后续阶段。

---

## 8. 风险与缓解

| 风险 | 影响 | 缓解 |
|---|---|---|
| headless 下 agent TUI 行为差异 | 任务无法完成/readiness 卡住 | 固定 PTY 80×24/可配置；注入 `TERM=xterm-256color`；对 5 种 agent 做集成验证；保留交互模式作为对照 |
| SIGKILL agentd 导致 agent 孤儿 | 资源泄漏、重复进程 | 正常路径 SIGTERM→等待→SIGKILL；agentd 必须优雅处理 TERM；systemd `KillMode=mixed`/launchd job 最终清理；P3 启动对账 |
| 多实例/端口冲突 | 启动失败或写错 state | workspace-key 的 pid flock；启动前 listen；admin 端口冲突报错并提示改 `--admin-addr` |
| Core/agentd 共享日志轮转踩踏 | 日志损坏/丢失 | 后续若启用文件日志，保持每进程独立文件；`serve` MVP 全部写 stdout |
| SIGHUP 重载竞态 | 请求读到半更新配置 | `atomic.Pointer[Config]`；先 validate 再 swap；失败保留旧值 |
| 30min 任务与 20s shutdown 冲突 | 在途任务被中止 | 文档明确；`Ask` 在 stopping 时返回 Unavailable；supervisor 超时兜底 |
| launchd/systemd 环境缺 HOME/PATH/凭据 | agent 无法认证 | 预检 + 文档；user unit/LaunchAgent；`agents[].binary` 绝对路径；`EnvironmentFile`/plist `EnvironmentVariables` |
| 无鉴权本地端口 | 本机任意进程可驱动 agent | 强制 loopback；不提供非 loopback 开关；文档安全警告 |
| coordinator 在 daemon 下无输入通道 | 服务常驻但无法对话 | 实现 3.13 的 `Chat`，或明确 `--no-coordinator` 语义 |
| agent 首次登录/认证需要交互 | daemon 启动后卡住或任务失败 | 文档要求先在交互终端完成 agent 登录；预检只验证 binary，不尝试认证 |
| 日志含敏感 prompt/输出 | 信息泄露 | 0600/0700；默认丢弃 TUI 输出；不记录 env 值 |
| 现有测试依赖 `New(workspace,cfg)` | 重构破坏测试 | 保留兼容构造函数或提供 test helper；分阶段迁移 |

---

## 9. 运维模板要点

### 9.1 systemd user unit（Linux）

```ini
[Unit]
Description=mecha orchestrator
After=network.target

[Service]
Type=simple
WorkingDirectory=%h/project
ExecStart=%h/go/bin/mecha serve --config %h/.mecha/config.yaml --workspace %h/project
Restart=on-failure
RestartSec=5
KillMode=mixed
TimeoutStopSec=30
Environment=HOME=%h
Environment=PATH=%h/.local/bin:/usr/local/bin:/usr/bin:/bin
EnvironmentFile=-%h/.mecha/mecha.env

[Install]
WantedBy=default.target
```

- `KillMode=mixed`：先只给主进程 SIGTERM，让 Core 优雅回收；超时后 SIGKILL 整个 cgroup（含独立 session 的 agent）。
- 需要开机即启动：`loginctl enable-linger $USER`。

### 9.2 launchd LaunchAgent（macOS）

```xml
<key>Label</key><string>com.champly.mecha</string>
<key>ProgramArguments</key>
<array>
  <string>/Users/champly/go/bin/mecha</string>
  <string>serve</string>
  <string>--config</string><string>/Users/champly/.mecha/config.yaml</string>
  <string>--workspace</string><string>/Users/champly/project</string>
</array>
<key>WorkingDirectory</key><string>/Users/champly/project</string>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict>
<key>ThrottleInterval</key><integer>10</integer>
<key>ProcessType</key><string>Background</string>
<key>StandardOutPath</key><string>/Users/champly/.mecha/logs/.../launchd.out.log</string>
<key>StandardErrorPath</key><string>/Users/champly/.mecha/logs/.../launchd.err.log</string>
<key>EnvironmentVariables</key>
<dict><key>PATH</key><string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin</string></dict>
```

- 必须用 LaunchAgent（用户会话）而非 LaunchDaemon，否则 Keychain/自动化权限不可用。
- 优雅退出返回 0，`KeepAlive` 不会重启；崩溃/非 0 才重启。

---

## 10. 待确认/后续

1. `serve` 默认 gRPC 端口：保持 `127.0.0.1:0` + state 发现，还是固定一个默认端口？本方案选择前者，避免多 workspace 冲突；需要外部固定接入时显式 `--addr 127.0.0.1:7333`。
2. admin 默认端口 `7334` 是否可接受；多实例需 `--admin-addr` 覆盖。
3. SIGHUP 是否应自动重启 coordinator 以应用新 prompt（本方案默认不重启）。
4. 是否需要 `mecha attach` 查看 agent TUI 输出（P3）。
5. 是否引入 `lumberjack` 替代自研轮转（本方案默认自研，少一个依赖）。
6. **是否采纳 3.13 的 `Chat` RPC**：这是 daemon 可用性的关键产品决策；若不采纳，`serve` 只能服务 specialist。
