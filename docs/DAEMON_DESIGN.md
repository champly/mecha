# Mecha 后台常驻（daemon）改造架构方案

> 状态：设计稿，供 coder 实施
> 范围：让 `mecha` 在无 TTY、无前台 stdin/stdout 的环境下常驻运行，可优雅退出、日志落盘、可被 systemd/launchd 托管。
> 非目标：改变 agent 的 PTY/TUI 交互模型；改变 hook 协议；引入远程多租户或鉴权。

---

## 0. 结论摘要

| 决策点 | 结论 |
|---|---|
| 是否 fork/setsid daemonize | **不实现传统 fork daemonize**。由 systemd / launchd / supervisor 托管。Go 进程 `fork()` 不安全；`--detach` 仅作为 P2 的 re-exec+setsid 便利选项 |
| 新增命令 | `mecha serve`：前台常驻、headless、可托管；`mecha run` 保留原有交互语义 |
| 终端后端 | 新增 `pkg/term/headless`，直接 spawn 子进程 + 进程组管理，不依赖 tmux/iTerm2/Ghostty |
| 日志 | 每个进程独立日志文件 + 进程内大小轮转；不再让 Core/agentd 多进程写同一文件 |
| 配置来源 | flags > env > YAML > 默认值；集中在 `pkg/daemon.Options` 解析 |
| 信号 | SIGTERM/SIGINT 优雅退出；SIGHUP 重载配置并重开日志；第二次 TERM/INT 强制退出 |
| PID | flock 保护的单实例 pid 文件 + 原子写入的 state 文件（含实际 gRPC 地址） |
| 健康检查 | gRPC health service + 独立的 loopback HTTP admin（`/healthz`、`/readyz`、`/status`） |
| 端口 | `serve` 默认 gRPC `127.0.0.1:0`（state 文件发现），admin 默认 `127.0.0.1:7334`；可用配置固定 |
| Coordinator 退出 | `serve` 模式下 Core 不退出，按指数退避重启 coordinator；`run` 模式保持“退出即结束” |
| daemon 输入通道 | 现状 `Ask` 拒绝 coordinator；后台化必须补一个输入入口。建议 P1 新增 `Chat` RPC + `mecha chat`，否则 `serve` 只能当 specialist 任务服务器 |
| 安全 | 监听地址只允许 loopback，除非显式 `allow_non_loopback`；日志 0600 |

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
| `pkg/agentd/agentd.go:209-218` | `initLogging` 用 `os.Getwd()` + 共享日期文件 | cwd 不稳定；多进程写同一文件；无轮转 |
| `cmd/webhook.go:31` | `io.ReadAll(cmd.InOrStdin())` | **不要改**：这是 agent hook 子进程的 stdin，不是 daemon 的 stdin |
| `cmd/ask.go:42` | 结果写 stdout | 客户端命令，保留；daemon 化不需要改 |

### 1.3 日志与配置

| 位置 | 现状 | 后台化问题 |
|---|---|---|
| `pkg/config/config.go:53-66` | 日志路径 `~/.mecha/logs/<workspace>/<YYYY-MM-DD>.log` | 文件名在启动时固定；跨天不换文件；Core/agentd 多进程 append，无法安全轮转 |
| `pkg/config/config.go:68-96` | `slog` text handler，文件 0644，`AddSource` | 权限偏宽；无 level/format 配置；无 reopen |
| `pkg/config/config.go:138-159` | YAML 只有 agents/profiles | 缺 server/logging/runtime 段；无 env/flag 覆盖 |
| `pkg/core/core.go:52-56`、`agentd/agentd.go:209-218` | 各自打开日志文件 | 无统一日志生命周期；失败静默回退 stderr |

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

- `serve` 不读 stdin，不写 stdout；所有诊断写日志。
- 所有子进程由 Core 统一回收；headless backend 用进程组信号保证 agentd 子树退出。
- headless backend 用 `SysProcAttr{Setsid: true, Setctty: true}` 拉起 agentd，因此 agentd 自身也是独立 session/进程组（`pgid == pid`）。agent 又由 agentd 通过 `pty.StartWithSize` 放入另一个独立 session，因此 **agentd 必须先 SIGTERM 优雅关闭 agent 进程组，再由 Core 对 agentd 做 SIGKILL 兜底**；SIGKILL 无法跨 session 回收 agent，最终由 systemd cgroup / launchd job 清理。

### 2.2 `run` vs `serve`

| 维度 | `mecha run`（默认，交互） | `mecha serve`（daemon） |
|---|---|---|
| 终端后端 | `auto`：tmux/iTerm2/Ghostty，缺失则报错 | 默认 `headless` |
| stdio | 直连当前终端 | `/dev/null` 或日志 |
| gRPC 地址 | `127.0.0.1:0` | 配置，默认 `127.0.0.1:0` + state 文件 |
| admin HTTP | 关闭 | 默认开启 |
| coordinator 退出 | 整个进程退出（保留现状） | 指数退避重启，服务继续 |
| pid/state | 不写 | 写 |
| 信号 | SIGINT/SIGTERM | SIGINT/SIGTERM/SIGHUP |
| 日志 | 可沿用旧路径 | 每进程独立 + 轮转 |

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
  - PTY master 设为非阻塞，使用 `poll` 读写；输出写入 `spec.Stdout`，stderr 可独立写入 `spec.Stderr`（Core 传入带轮转的 writer，或 `io.Discard`）
  - 不用 `CommandContext` 的默认取消语义；自行起 watchdog：SIGTERM 进程组 → 等待 grace → SIGKILL 进程组，避免 `WaitDelay` 只杀 leader、漏掉 agent 子进程
  - `Start` 后 goroutine `Wait`；leader 回收后仍保留 handle，直至整个进程组消失后再从 map 删除
- `Kill`：
  1. `syscall.Kill(-pid, SIGTERM)`；
  2. 等待 `grace`（默认 10s）或 ctx 截止；
  3. `syscall.Kill(-pid, SIGKILL)`；
  4. 回收 `Wait`。
- `Close`：对所有存活 handle 执行 Kill，等待，返回。
- Handle：`ID=role-pid`，`PaneID=strconv.Itoa(pid)`。

注意：`agentd` 的 agent 是独立 session，进程组信号打不到；必须依赖 agentd 自己的 SIGTERM handler。因此 `agentd` 的优雅退出是 P0，不可省略。

### 3.4 日志落盘与轮转

#### 目录与文件

```
~/.mecha/logs/<workspace-key>/
  core.log                        # Core + admin + headless 子进程 stderr
  agentd-<role>-<shortid>.log     # agentd 自身 slog
  agent-<role>-<shortid>.out      # 可选：PTY 输出（agent_output=file）
  launchd.out.log / launchd.err.log  # supervisor 捕获的崩溃输出（可选）
```

`workspace-key`：绝对路径去掉前导 `/`、`/`→`_`，再加 8 位 sha256 前缀，避免 `a/b` 与 `a_b` 冲突。旧路径 `<YYYY-MM-DD>.log` 不再用于 `serve`；`run` 可短期兼容。

#### 归属规则

- **禁止 Core 与 agentd 多进程写同一轮转文件**：轮转会互相踩踏。
- Core 进程拥有 `core.log`；每个 agentd 通过 `--log-file` 接收自己的绝对路径。
- agentd 的 stdout/stderr（panic traces）由 headless backend 写入 Core 的 `core.log`（单写者，安全），或写入独立 `.stderr.log`。
- PTY 输出默认 `discard`（TUI 控制序列巨大且通常无价值）；`agent_output=file` 时写独立轮转文件，默认 10 MiB × 2。

#### 轮转实现

新增 `pkg/logging`（stdlib，不引入新依赖）：

```go
type Config struct {
    Dir        string
    File       string        // 基础文件名，如 core.log
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
  addr: 127.0.0.1:0          # Core gRPC
  admin_addr: 127.0.0.1:7334 # health/status HTTP
  allow_non_loopback: false

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
MECHA_ADDR, MECHA_ADMIN_ADDR, MECHA_ALLOW_NON_LOOPBACK
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
    AllowNonLoopback   bool
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
| SIGHUP | 重载配置文件 + `logging.Reopen()` + `core.Reload(cfg)`；失败保留旧配置并记录 error；1s 内去抖 |
| SIGQUIT | 保留 Go 默认 stack dump（由 supervisor 捕获 stderr） |
| SIGWINCH | serve 忽略；agentd 交互模式转发 PTY，headless 忽略 |

`cmd/agentd.go`：

- `signal.NotifyContext(ctx, SIGINT, SIGTERM)` → `a.Run(ctx)`。
- 收到信号：停止接收新任务 → 失败在途任务 → `ReportStatus(exited)`（尽力）→ 先 SIGTERM agent 进程组，等待 grace（默认 10s）→ SIGKILL → 关 PTY/webhook/log。
- agentd 不处理 SIGHUP（无配置可重载）。

SIGHUP 重载语义（必须写进文档）：

- 生效：日志级别/格式、新 spawn 的 specialist 的 role/agent 配置、后续 `Register` 返回的配置。`profile` 覆盖需重新执行校验/`complete()`，实现上建议 `config.LoadConfigWithProfile(path, name, validType)`。
- 不生效：已存在 agent/agentd 的 prompt、模型、env；监听地址/backend/pid 路径变更需重启。
- 若 coordinator role 或 prompt 变更，默认不自动重启 coordinator；P2 提供 `mecha restart-coordinator` 或 `serve --reload-restarts-coordinator`。

### 3.8 监听地址、健康检查与状态

#### Core gRPC

- `server.addr` / `--addr`；`serve` 默认 `127.0.0.1:0`，启动后把实际地址写入 state。
- 校验：解析 host:port；host 为空或非 loopback 时，除非 `allow_non_loopback=true`，否则报错。原因：Ask 可驱动带 `--dangerously-skip-permissions` 的 agent。
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

### 3.9 优雅关闭顺序

`Core.Run(ctx)` 在 ctx 取消（信号）时：

1. 标记 `state=stopping`；`/readyz` 返回 503；拒绝新 `Ask`（`grpcService.Ask` 检查 `stopping` 标志，返回 `Unavailable`）。
2. **先关闭 gRPC listener**（停止接受新连接），但保留已有 stream；不能直接 `GracefulStop`：`TaskChannel` 是常驻双向流，GracefulStop 会一直等它，直到超时。
3. 向 coordinator 进程组发 SIGTERM；等待其退出（上限 grace，默认 10s）；超时 SIGKILL。
4. 对所有 specialist 并行 `backend.Kill`（headless 内部 SIGTERM→SIGKILL；tmux/iTerm2/Ghostty 原有逻辑），总上限 `ShutdownTimeout - reserve`。agentd 退出时会关闭 `TaskChannel` 与 gRPC conn。
5. 此时再 `GracefulStop()`，预算剩余时间（reserve 默认 5s）；超时则 `Stop()`。在途 `Ask` 因 agentd 断开而快速失败，不会等 30min。
6. 关闭 admin server（`Shutdown`，5s）。
7. `backend.Close()` → 关闭 log writers（flush）→ 移除 state 文件 → 释放 pid 锁。
8. 记录 `shutdown complete signal=... duration=...`，返回 nil。

预算默认：`shutdown_timeout=20s`，supervisor `TimeoutStopSec=30`，留 10s 余量。文档明确：**SIGTERM 会中止在途任务，不会等待 30min 的任务超时**。

实现注意：关闭 listener 后 `grpc.Server.Serve` 会返回错误；若此时已处于 stopping，应视为正常退出，不要记为致命错误。

### 3.10 Coordinator 监督

`serve` 模式下 `pkg/core/coordinator.go` 改为循环：

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
// pkg/agentd
type Options struct {
    ID              string
    CoreAddr        string
    Workspace       string   // 不再依赖 cwd
    LogFile         string   // 绝对路径；空则按 workspace 派生
    LogLevel        string
    Headless        bool
    AgentOutput     string   // discard|file
    AgentLogFile    string
    Cols, Rows      uint16   // 默认 80×24
    ShutdownTimeout time.Duration
}

func New(opts Options) *Agentd
func (a *Agentd) Run(ctx context.Context) error
```

- `Run`：等价于 `Start` + 等 ctx/stop；由 `cmd/agentd.go` 注入 `signal.NotifyContext`。
- `startAgent`：
  - 输出：`headless + discard` → `io.Discard`；`headless + file` → `logging.Writer`；交互 → `os.Stdout`。
  - `activityWriter` 包装输出，readiness 逻辑不变。
  - 仅交互模式（`!Headless && term.IsTerminal(stdin)`）启动 `forwardStdin`、`makeRawIfTerminal`、`watchWinch`。
  - `launchPTY(cmd, &pty.Winsize{Rows: opts.Rows, Cols: opts.Cols})`，headless 不读 stdin。
- `initLogging`：使用 `opts.Workspace` + `opts.LogFile`，不再 `os.Getwd()`；日志加 `id`/`role` 字段。agentd 是独立进程，可以继续 `slog.SetDefault` 指向自己的日志文件，但必须使用显式路径。
- 退出：
  - 单一 `cmd.Wait()` 所有者（`waitAgent`）；其他路径只发信号并等待 `waitDone`，避免重复 Wait。
  - SIGTERM → `syscall.Kill(-pid, SIGTERM)`（agent 是 session leader）→ 等待 grace → `SIGKILL`。
  - 失败在途任务 → `ReportStatus(exited)` → Close。
- `Close` 顺序：stop 任务 → 关 PTY（发送 SIGHUP）→ 等待/强杀 agent → webhook Shutdown → gRPC conn → log close。

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
5. 可选 `serve --no-coordinator`：纯 specialist 任务服务器；此时 `/readyz` 只要求 gRPC 可用，不要求 coordinator。

若产品上只需要后台 specialist、不需要对话 coordinator，则：
- `serve` 可以不启动 coordinator（`--no-coordinator`），直接暴露 `Ask`；
- 文档必须明确 coordinator 角色在 daemon 模式下不可用。

不推荐的做法：

- 直接放开 `Ask` 接受 coordinator：会破坏现有“Ask 只用于 specialist”的不变量和测试语义。
- 让用户 `attach` 到 PTY 再输入：仍依赖终端，违背本次目标（可作为 P3 的调试功能）。

### 3.14 `serve` 启动与失败顺序（实现清单）

1. 解析 flags/env；加载并校验 YAML；应用 `--profile`；`daemon.Resolve()` + `Validate()`。
2. 确认 workspace 绝对路径、HOME、日志目录、run 目录权限；做 agent binary/端口预检。
3. `logging.New()` 初始化 `core.log`；`slog.SetDefault`；后续错误同时写 stderr 与日志。
4. `daemon.AcquirePIDFile()`；失败（`ErrAlreadyRunning`）→ 退出码 3。
5. `term.New(backend)`；`core.New(options)`。
6. 启动 admin HTTP（若配置）；`core.Run(ctx)` 内部 listen gRPC 并回调 `OnListening(addr)`。
7. `OnListening` 中：写 state（pid、gRPC addr、admin addr、workspace、profile、version、started_at）。
8. 启动 coordinator（`serve` 下 supervisor 循环；`run` 下前台等待）。
9. 安装信号处理（SIGTERM/SIGINT/SIGHUP）；阻塞到 ctx 取消。
10. 清理顺序严格反向：stop accepting（关 listener）→ 子进程 SIGTERM → Core GracefulStop/Stop → admin shutdown → backend close → log close → state remove → pid release。

任一步失败：关闭已创建资源、删除 state（若已写）、释放 pid 锁、返回可操作错误；**不得留下半启动的监听或子进程**。

---

## 4. 关键接口设计（供 coder 直接实现）

### 4.1 `pkg/core`

```go
type Options struct {
    Workspace       string
    Config          config.Config
    Addr            string // "" => 127.0.0.1:0
    Backend         term.Backend // nil => term.New(term.Options{Kind:...})
    Logger          *slog.Logger
    ShutdownTimeout time.Duration
    RestartCoordinator bool
    OnListening     func(addr string) error // 写 state 的钩子，失败则终止启动
}

type Core struct { /* ... */ }

func New(opts Options) (*Core, error)
func (c *Core) Run(ctx context.Context) error
func (c *Core) Addr() string
func (c *Core) Reload(cfg config.Config) error
func (c *Core) Snapshot() Status // Status 至少包含 coordinator 与 specialists 的 role/id/state/restarts/last_error
```

- `Core.cfg` 改为 `atomic.Pointer[config.Config]`，`profileRoles/findRole/coordinatorRole` 每次取快照；避免 SIGHUP 与请求处理竞态。
- `Core` 不再创建/关闭日志文件；logger 由 cmd 注入，生命周期归 cmd。
- `Core.Run` 负责关闭传入的 backend（约定：所有权转移给 Core）。
- 保留 `func (c *Core) Start(ctx context.Context) error { return c.Run(ctx) }` 作为过渡，减少一次性改动量。

### 4.2 `pkg/term`

```go
type Backend = driver.Backend
type Options struct {
    Kind          string
    AllowHeadless bool
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
    AllowNonLoopback                          *bool
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

### 5.1 新增

| 文件 | 要点 | 优先级 | 风险 |
|---|---|---|---|
| `pkg/logging/logging.go` | slog 构造、level/format、`Writer.Reopen` | P0 | 中 |
| `pkg/logging/rotate.go` | 大小轮转、保留/清理、并发安全 | P0 | 中（丢日志） |
| `pkg/logging/rotate_test.go` | 轮转/并发/超大单条测试 | P0 | 低 |
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
| `cmd/agentd.go` | 新增 `--workspace/--log-file/--log-level/--headless/--agent-output/--agent-log-file/--cols/--rows/--shutdown-timeout`；`signal.NotifyContext`；调用 `Run(ctx)` | P0 | 中 |
| `cmd/ask.go` | `--addr` 可选：缺省读当前 workspace state；保留显式 addr | P1 | 低 |
| `main.go` | 保持 CLI 错误 stderr；必要时增加已定义退出码（1 启动/运行，3 已运行） | P0 | 低 |
| `pkg/config/config.go` | 新增 `Server/Logging/Runtime` 段与默认值；新增 workspace-key 日志路径/run 路径 helper；`NewFileLogger` 改为兼容 wrapper 或删除 | P0 | 中 |
| `pkg/config/config.yaml` | 增加注释掉的 `server/logging/runtime` 示例，保持旧配置可用 | P1 | 低 |
| `pkg/core/core.go` | `Options/New/Run/Addr/Reload/Snapshot`；可配置监听；graceful shutdown；不再持有日志文件；health service | P0 | 高 |
| `pkg/core/coordinator.go` | headless 子进程（stdio 重定向）；进程组 SIGTERM→SIGKILL；重启循环 + 退避；单一 Wait 所有者 | P0 | 高 |
| `pkg/core/specialist.go` | Spawn 传 `Env/Stdout/Stderr`；日志路径下发；配置快照读取 | P0 | 中 |
| `pkg/core/server.go` | `Ask` 拒绝 stopping；新增 `Chat`（P1）；Register/ReportStatus 用配置快照；状态快照 | P0 | 中 |
| `pkg/core/instance.go` | 暴露 Snapshot（state/role/id/restarts）；不改变任务状态机 | P1 | 中 |
| `pkg/core/registry.go` | `snapshot()` 返回稳定排序的角色状态 | P1 | 低 |
| `pkg/agentd/agentd.go` | Options/Run(ctx)；显式 workspace/log；信号与优雅关闭；按显式路径设置进程内默认 logger | P0 | 高 |
| `pkg/agentd/process.go` | 输出 sink；headless 跳过 stdin/raw/winch；PTY 尺寸显式；agent 进程组优雅 kill | P0 | 高 |
| `pkg/agentd/webhook.go` | 使用注入 logger（可选）；其余不变 | P2 | 低 |
| `pkg/term/term.go` | `New(Options)` 显式选择 + headless 注册；保留 `New()` 兼容 wrapper | P0 | 中 |
| `pkg/term/driver/driver.go` | `Spec` 增加 `Env/Stdout/Stderr` | P0 | 低 |
| `pkg/term/iterm2/iterm2.go` | headless 不调用；`Label` 仅在交互模式；无需大改 | P1 | 低 |
| `README.md` / `README.zh-CN.md` | 新增后台运行章节、命令表、日志路径、安全说明 | P1 | 低 |
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

1. `pkg/logging` + 测试：轮转、并发、Reopen。
2. `pkg/daemon`：Options 解析、PID flock、state 原子读写 + 测试。
3. `pkg/term/headless` + `driver.Spec` 扩展 + 测试。
4. `pkg/config` 新配置段与默认值；保留旧字段兼容。

**验收**：`go test ./pkg/logging ./pkg/daemon ./pkg/term/headless ./pkg/config` 通过；旧 `mecha run` 不受影响。

### Phase 1：Core 生命周期重构 + serve 入口

1. `pkg/core` 引入 `Options/New/Run/Addr/Reload/Snapshot`；`Core.cfg` 原子快照；监听地址可配置；gRPC health。
2. `coordinator.go`：headless spawn、进程组优雅终止、重启循环。
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

### 7.1 功能验收

1. **无 TTY 常驻**
   ```bash
   # Linux
   setsid ./mecha serve --workspace /path/to/project \
     --config ~/.mecha/config.yaml --log-level info \
     </dev/null >/dev/null 2>&1 &
   # macOS（无 setsid 命令时）
   nohup ./mecha serve --workspace /path/to/project \
     --config ~/.mecha/config.yaml --log-level info \
     </dev/null >/tmp/mecha.out 2>&1 &
   disown
   ```
   父 shell 退出后进程仍在；`ps -o tty= -p <pid>` 为空；`/proc/<pid>/fd/0`（Linux）或 `lsof -p <pid>`（macOS）不指向 TTY；无 stdin 读取 goroutine。
2. **任务可用**
   ```bash
   ./mecha ask --addr 127.0.0.1:<port> architect "design X"
   ./mecha chat --addr 127.0.0.1:<port> "what is the current status?"
   ./mecha status --workspace /path/to/project
   ```
   若未实现 `Chat`，`serve` 必须支持 `--no-coordinator` 并在文档中声明只提供 specialist 任务接口。
3. **优雅退出**
   ```bash
   kill -TERM $(cat ~/.mecha/run/<key>.pid)
   ```
   进程在 `shutdown_timeout` 内退出 0；`pgrep -f 'mecha (serve|agentd)'` 为空；pid/state 文件被移除；日志含 `shutdown complete`。
4. **重复实例**
   同 workspace 第二次启动 → 退出码 3，错误包含已运行 pid；不产生第二个监听/子进程。
5. **SIGHUP**
   ```bash
   kill -HUP $(cat ...pid)
   ```
   日志级别/新配置生效；已有任务不中断；无效配置只记录 error，进程不退出。
6. **健康检查**
   - 启动中：`/readyz` 503。
   - coordinator ready：`/readyz` 200，`/healthz` 200，`/status` 含 coordinator/specialists。
   - stopping：`/healthz` 503。
7. **Coordinator 崩溃恢复**
   `kill -TERM <coordinator agentd pid>` → `serve` 不退出；`/readyz` 503；退避后重启并恢复 200；specialist 不受影响。
8. **日志**
   - `core.log` 与 `agentd-*.log` 独立；`stat -f %Lp`/`stat -c %a` 为 600。
   - 写入超过 `max_size_mb` 后生成备份文件；进程持续运行不报错。
   - 不出现 Core 与 agentd 交叉写同一文件。
9. **交互模式回归**
   在 tmux/iTerm2/Ghostty 中 `mecha`：pane 正常创建、键盘输入转发、coordinator 退出后清理 pane，行为与改造前一致。
10. **supervisor 托管**
    - Linux：`systemctl --user start mecha` → `status=active`；`systemctl --user stop mecha` → 30s 内 inactive；`systemctl --user restart mecha` 正常。
    - macOS：`launchctl bootstrap gui/$UID deploy/launchd/com.champly.mecha.plist` → `launchctl print gui/$UID/com.champly.mecha` 为 running；`bootout` 后无残留。
11. **环境预检**
    缺失 `HOME`、agent binary、不可写日志目录、非法监听地址 → 启动失败，退出码 1，错误信息可操作（包含需要的 flag/env）。

### 7.2 质量门禁

- `go test ./...` 全绿。
- `go vet ./...` 通过。
- `gofmt -l .` 为空。
- 新增包测试覆盖：轮转、pid 锁竞争、state 原子性、headless 进程树回收、coordinator 重启、优雅关闭、admin 状态码。
- 手工验收至少覆盖 `serve` + `ask` + SIGTERM + SIGHUP + coordinator 重启 + 日志轮转。

---

## 8. 风险与缓解

| 风险 | 影响 | 缓解 |
|---|---|---|
| headless 下 agent TUI 行为差异 | 任务无法完成/readiness 卡住 | 固定 PTY 80×24/可配置；注入 `TERM=xterm-256color`；对 5 种 agent 做集成验证；保留交互模式作为对照 |
| SIGKILL agentd 导致 agent 孤儿 | 资源泄漏、重复进程 | 正常路径 SIGTERM→等待→SIGKILL；agentd 必须优雅处理 TERM；systemd `KillMode=mixed`/launchd job 最终清理；P3 启动对账 |
| 多实例/端口冲突 | 启动失败或写错 state | workspace-key 的 pid flock；启动前 listen；admin 端口冲突报错并提示改 `--admin-addr` |
| Core/agentd 共享日志轮转踩踏 | 日志损坏/丢失 | 设计上每进程独立文件；Core 通过 writer 承接子进程 stderr |
| SIGHUP 重载竞态 | 请求读到半更新配置 | `atomic.Pointer[Config]`；先 validate 再 swap；失败保留旧值 |
| 30min 任务与 20s shutdown 冲突 | 在途任务被中止 | 文档明确；`Ask` 在 stopping 时返回 Unavailable；supervisor 超时兜底 |
| launchd/systemd 环境缺 HOME/PATH/凭据 | agent 无法认证 | 预检 + 文档；user unit/LaunchAgent；`agents[].binary` 绝对路径；`EnvironmentFile`/plist `EnvironmentVariables` |
| 无鉴权本地端口 | 本机任意进程可驱动 agent | 强制 loopback；非 loopback 需显式开关；文档安全警告 |
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
