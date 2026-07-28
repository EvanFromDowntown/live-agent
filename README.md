# live-agent — 通用 Agent 框架（v2）

一个用 Go 实现的通用 Agent 框架。LLM 是一个**冻结的认知模块**（不训练、不微调），
框架把它接进一个**反应式、原生 tool_use 的执行循环**：每一步向模型要**一个**工具调用，
在宿主机上真实执行，把真实结果反馈回去，直到模型 `finish` 或触发限制。

设计要点（均为可配置决策）：

- **原生 function-calling**：动作 = 模型训练分布内的 tool_call；由 `ActionRegistry` 做白名单 + schema 校验。
- **混合循环**：模型用 `update_plan` 维护一份 TODO，每 tick 只发一个 tool_call，反应式推进。
- **多轮上下文**：滚动 transcript（动作 + 真实结果）+ 预算内 pruning/compaction（默认 75% 触发），让模型读到自己上一步的报错并自我调试。
- **运行时环境注入**：OS / shell / 可用解释器 / 工作目录在启动时**探测**并注入上下文块——system prompt 不假设特定操作系统。
- **直接宿主执行**：内置 `run_shell` / `run_python` / `read_file` / `write_file` / `http_fetch` / `update_plan` / `finish`，用 `os/exec` 直接在工作目录执行（无沙箱）。
- **安全硬约束**：白名单 + schema 校验 + 禁用动作/危险命令模式，优先于模型意图。
- **总会终止**：`max_steps` + 连续重复检测 + 连续错误检测 + `finish`。
- **持久化**：SQLite 记录每次任务（episode）与其中每个工具调用（event），并预留 notes 表供后续「带验证的学习」使用。

> v2 **暂不包含**上一代的持续学习机制（原则/技能/反思/进化）。当前目标是先把「能干活」的核心做扎实；
> 之后再加**带 A/B 验证**的学习。同样不包含任何权重更新。

---

## 快速开始

```bash
go test ./...
go build -o bin/agent ./cmd/agent

# 凭据只从环境变量读取，绝不写入代码或配置
export LLM_BASE_URL="https://your-gateway/v1"
export LLM_API_KEY="..."
export LLM_MODEL="your-model"

# 任务从 stdin 传入
echo "把 example.com 首页标题抓下来写到 title.txt" | ./bin/agent -config configs/agent.yaml

# 或用 -task 参数
./bin/agent -config configs/agent.yaml -task "统计当前目录下 .go 文件行数并写入 report.md"
```

运行结束会打印 episode、步数、停止原因、是否 finish/success、摘要与工作目录。

---

## 循环（每一步）

```
探测环境（首步）
 → 组装分层 prompt：system + 动态上下文块(task/env/plan) + 滚动 transcript + 本轮 ask
 → 逼近预算则压缩较早 transcript 轮次为摘要
 → 请求模型（携带 tool 定义），取第一个 tool_call
   （无原生 tool_call 时，从正文文本兜底解析出已注册的工具调用）
 → 安全门控 → schema 校验 → 执行 → 记录 event
 → 把真实结果写回 transcript
 → finish? 达步数/重复/连续错误上限? 否则继续
```

## 包结构

| 包 | 职责 |
|----|------|
| `internal/domain` | 无依赖核心类型（`Message`/`ToolDef`/`ToolCall`/`LLMRequest`/`Action`/`LLM`）与 `ActionRegistry`（白名单 + schema 校验） |
| `internal/config` | 精简 YAML 配置（llm / limits / context / safety / agent）；**任务本身不在配置里**，运行时经 stdin 传入 |
| `internal/llm` | OpenAI-compatible Provider（原生 tool_use）、超时/重试降级、宽容 JSON 提取 |
| `internal/store` | SQLite：`episodes` / `events` / `notes` |
| `internal/tool` | `Tool` 接口 + 注册表 + 内置宿主工具；由 schema 生成 tool 定义 |
| `internal/safety` | 硬约束门控（禁用动作 / 危险命令模式），优先于模型意图 |
| `internal/agent` | 反应式 tool_use 循环、分层 prompt 组装 + 环境探测、transcript 上下文管理与压缩 |
| `cmd/agent` | 入口：装配依赖，从 stdin/`-task` 读任务并运行 |

---

## 配置

见 [`configs/agent.yaml`](configs/agent.yaml)：

- `agent`：`name`（重启恢复身份）、`db_path`、`workspace`（宿主执行目录）、可选 `system_prompt`（**追加**到内置操作 prompt）
- `llm`：`provider`（`openai`）、`model`、`temperature`、`max_tokens`、`timeout`、`max_retries`、`disable_response_format`、`disable_thinking`、`reasoning_effort`
- `limits`：`max_steps` / `max_repeats` / `max_consecutive_errors` / `step_timeout`
- `context`：`max_chars` / `keep_recent` / `compact_at_pct`
- `safety`：`forbidden_actions` / `forbidden_shell_patterns`

凭据（`LLM_BASE_URL` / `LLM_API_KEY` / `LLM_MODEL`）**只**从环境变量读取。

> 网关兼容开关 `llm.disable_response_format`：某些网关在 `response_format: json_object` 下会污染正文；
> 工具调用模式下本就不请求 JSON 模式，该项保持 `true` 更稳。

---

## 内置工具

| 工具 | 说明 |
|------|------|
| `run_shell` | 在工作目录 `sh -c` 执行命令，返回合并的 stdout+stderr 与退出状态 |
| `run_python` | 将代码写入文件并用 Python3 执行；缺第三方库时应先用 `run_shell` 安装 |
| `read_file` / `write_file` | 读写 UTF-8 文本（相对路径落在工作目录） |
| `http_fetch` | 对 URL 发 GET，返回状态码与截断正文；更复杂场景用 `run_python` |
| `update_plan` | 设置/更新 TODO 计划（数组或 JSON 字符串均可） |
| `finish` | 结束运行，给出摘要与 `success` |

> 直接在宿主机执行是本仓库的显式选择（无沙箱）。`safety.forbidden_shell_patterns` 拦截明显危险命令，
> 但这不是强隔离；请在你能接受的环境里运行。

---

## 接入新工具

实现 `tool.Tool` 并注册即可（schema 会自动转成 function-calling 定义、并纳入白名单校验）：

```go
type Tool interface {
    Spec() domain.ActionSchema
    Execute(ctx context.Context, args map[string]any) tool.Result
}
```

---

> 本项目不包含任何模型微调、后训练，或让 Agent 修改安全约束的能力。
