# 后续架构路线

> 状态：部分实施  
> 本文只保留尚未完成或仍需演进的方案；当前实际行为见 [DESIGN.md](DESIGN.md)。

## 1. Daemon / Serve

### 1.1 已落地

- `mecha run`：交互模式，终端后端为 tmux/iTerm2/Ghostty。
- `mecha serve`：无 TTY 常驻模式，默认 headless backend。
- `pkg/term/headless`：直接启动 agentd 和 agent，保留 PTY，不依赖终端 pane。
- SIGINT/SIGTERM 优雅退出；先终止 agent 进程组，再关闭 Core 连接。
- `serve` 日志写 stdout，由 systemd/launchd/supervisor 重定向；`run` 继续写工作区日志。
- agentd 支持 `--dir`，不依赖 shell 的 `cd &&`。

### 1.2 目标进程模型

```text
systemd / launchd / supervisor
  └─ mecha serve
      ├─ Core gRPC + optional admin HTTP
      ├─ coordinator agentd
      │    └─ agent CLI (PTY)
      └─ specialist agentd
           └─ agent CLI (PTY)
```

约束：

- 不实现传统 double-fork daemonize；由 supervisor 托管。
- headless 子进程使用独立 session/process group；agentd 必须先回收 agent，Core 不得只依赖 SIGKILL。
- `run` 与 `serve` 复用同一 Core 生命周期，通过 options 区分终端后端、日志和 coordinator 策略。
- 单实例、pid/state 文件、动态端口发现、健康检查和 SIGHUP 仍未完整落地。

### 1.3 后续阶段

| 阶段 | 内容 | 验收 |
|---|---|---|
| P1 | pid/state 文件、flock 单实例、固定 loopback 地址、Core health | 无 TTY 常驻；重复启动 fail closed；父 shell 退出后仍可 `ask` |
| P2 | `/healthz`、`/readyz`、`/status`、`mecha status/stop` | coordinator 重启时 readiness 503→200；SIGTERM 无残留进程 |
| P3 | SIGHUP 配置重载、日志 Reopen/轮转、deploy 模板 | supervisor 启停稳定；重载不破坏在途任务 |
| P4（可选） | `serve --detach`、`attach`、PTY ring buffer、`/metrics` | 后台启动/观测体验完整 |

### 1.4 关键风险

- headless 下 TUI 固定尺寸、TERM 和交互查询行为需按五类 agent 实测。
- 独立 session 的 agent 无法由 Core 直接跨组回收，agentd 优雅退出是硬依赖。
- coordinator 在无 TTY 模式下的输入来源需要独立定义；当前 `serve` 不作为交互聊天入口。
- 多进程日志、pid/state 文件和异常重启对账不做，会导致运维可观测性不足。

## 2. IM 接入与定时任务

### 2.1 结论

先实现独立 `mecha hub` sidecar，不改 Core：

```text
飞书 / 企业微信
      ↕
mecha hub（channel + router + scheduler + SQLite + outbox）
      ↕ gRPC loopback
mecha serve / Core
      ↕
agentd → agent CLI
```

hub 不碰 PTY、不抓屏，只通过 Core API 提交任务并接收结构化最终结果。平台断线、限流、重试和 SQLite 故障与 agent 生命周期隔离。

### 2.2 平台范围

| 平台 | Phase 1 | 后续 |
|---|---|---|
| 飞书 | WebSocket 事件、文本/卡片/文件、3s ACK、event_id 去重 | CardKit 增量更新、更多消息类型 |
| 企业微信 | 群机器人 text/markdown/file | Phase 2 应用消息和应用文件 |
| 公众号/个人微信 | 不做 | 仍排除，避免非官方协议和封号风险 |

### 2.3 领域模型

核心表：

- `tasks`：IM、`/run`、cron 统一任务记录；
- `jobs` / `job_targets`：定时配置和目标；
- `runs`：job 的一次 occurrence，`(job_id, scheduled_at_ms)` 唯一；
- `inbound_dedup`：平台事件去重状态；
- `outbox`：状态卡、最终结果、失败通知和重试；
- `lease` / `audit_log`：单机单主与审计。

所有外部输出来自 outbox；`Stop/StopFailure` 的最终结果作为正文，不把 PTY 抓屏当聊天结果。

### 2.4 调度与可靠性

- `gocron/v2`，支持秒级、时区、可注入 clock。
- 默认 `max_parallel_per_role=1`，同 role 串行，跨 role 并行。
- overlap 策略默认跳过重叠 occurrence。
- 任务投递采用 at-least-once；`tasks.idempotency_key` 防重复创建。
- outbox 重试：`1m/5m/15m/1h/6h`，6 次后 dead。
- 飞书发送复用 uuid；企微群机器人无平台幂等，必须接受潜在重复。
- hub 先获取 lease 再连 WebSocket/claim；lease 丢失立即停止发送并退出。
- dispatcher 定期刷新 task/run 心跳；崩溃后 reaper 将 stale running 标记失败并补失败 outbox。

### 2.5 命令与隔离

Phase 1 命令：

- `/new`：切换新会话；
- `/run`：人工执行 job；
- `/schedule`、`/jobs`：管理定时任务；
- `/stop`：Phase 1 只停止 hub 等待，agent 可能继续运行；
- 未知 `/xxx` 默认作为普通 prompt，`//` 强制字面量。

SessionKey 默认由 `platform + tenant + chat + threadOrRoot` 组成；仅私聊或显式配置时加入 sender。Phase 1 同 role 串行，Phase 2 才做真正 session 隔离和取消。

### 2.6 后续阶段

| 阶段 | 内容 | 关键验收 |
|---|---|---|
| Phase 1 | sidecar hub、飞书/企微群机器人、SQLite、cron、outbox、lease、reaper、审计 | 3s ACK；event_id 去重；崩溃恢复；outbox 重试；不抓 PTY |
| Phase 2 | Core `session_id`、`Submit/TaskEvents/Cancel/Reset`、结构化增量、企微应用消息 | 两 session 不串上下文；Cancel 5s 内生效；后端事件带单调 `seq` |
| Phase 3 | `job_targets` 多目标 fanout、DLQ 运维、对象存储/落地页、指标报表 | fanout 部分失败可独立重试；DLQ 可重放且幂等 |

### 2.7 关键风险

- 30min 任务超时与 IM 交互冲突：hub 客户端至少 31min，超时发送失败卡。
- Phase 1 无法真正取消 agent：`/stop` 只停止等待，不能宣称任务已取消。
- 飞书卡片和企业微信都有严格频控，需要合并状态更新、节流和退避。
- 长文案必须按平台 UTF-8 字节上限截断，超限降级为文件或签名链接。
- SQLite 单点故障和 lease 双主需要单写连接、busy timeout、WAL 和 fail closed。
- 个人微信和非官方协议不纳入，避免封号和合规风险。

## 3. 优先级建议

1. 完成 daemon 单实例、state 发现、health/ready 和运维闭环。
2. 在稳定 `serve` 基础上实现 IM Phase 1 sidecar。
3. 再推进 Core session/cancel/structured events，避免在 agent 生命周期未稳定时扩展平台层。
4. 多目标 fanout、对象存储和复杂运维功能最后做。

