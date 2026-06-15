# mecha

多 agent 编排系统。（[English](README.md)）Lead 拆解需求后通过 `mecha ask` 分派给 specialist，任务状态由 Hook 事件驱动。

## 运行原理

```
用户
 │
 ▼
┌─────────────────────────────────────────────────────────┐
│  Lead（当前终端）                                        │
│                                                         │
│  接收需求 → 拆解 → mecha ask <role> "<task>"            │
│                                                         │
│  ┌─────────────────────────────────────────────────┐   │
│  │          mecha（HTTP Server）                    │   │
│  │                                                 │   │
│  │   POST /ask  ◄────────  POST /webhook/:id       │   │
│  │   （同步阻塞）             （事件回调）            │   │
│  └──────┬──────────────────────▲───────────────────┘   │
│         │ Spawn                │ Hook                   │
│         ▼                      │                       │
│  ┌──────────┐  ┌──────────┐  ┌──────────┐             │
│  │architect │  │  coder   │  │ tester   │  ...        │
│  │（pane）   │  │（pane）   │  │（pane）   │             │
│  └──────────┘  └──────────┘  └──────────┘             │
│                                                         │
│  任务完成 → Lead 汇总 → 返回用户                         │
└─────────────────────────────────────────────────────────┘
```

- **Lead**：接收需求、拆解任务、派发，不亲自执行
- **Specialist**：在独立 pane 中执行具体任务（architect / coder / tester / reviewer）
- **Hook 事件**：`SessionStart`（启动完成）、`Stop`（任务成功）、`StopFailure`（任务失败）驱动状态流转

## 快速开始

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
