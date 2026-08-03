# live-agent — 通用 Agent 框架（v2）

一个用 Go 实现的通用 Agent 框架。LLM 是一个**冻结的认知模块**（不训练、不微调），
框架把它接进一个**反应式、原生 tool_use 的执行循环**：每一步向模型要工具调用，
在宿主机上真实执行，把真实结果反馈回去，直到模型 `finish`、`reply` 或触发限制。
既能像助手一样**多轮对话**，也能作为 agent **真刀真枪地干活**。

设计要点（均为可配置决策）：

- **原生 function-calling**：动作 = 模型训练分布内的 tool_call；由 `ActionRegistry` 做白名单 + schema 校验。无原生 tool_call 时从正文兜底解析。
- **对话 / 任务双模**：模型用 `reply` 直接对话（单步返回），或进入任务模式（`update_plan` → 执行 → 验证 → `finish`）。会话长期保持，一个任务完成后可继续下一个。
- **混合循环 + 有限并行**：模型维护一份 TODO 计划，反应式推进；一步内可**并发**多个只读查询（`read_file`/`list_dir`/`glob`/`grep`/`http_fetch`），改状态与终止类调用则串行单发。
- **多轮上下文**：滚动 transcript（动作 + 真实结果）+ 预算内 pruning/LLM 压缩（默认 75% 触发），让模型读到自己上一步的报错并自我调试。当前目标锚定**本轮请求**，避免长会话漂回旧任务。
- **运行时环境注入**：OS / shell / 可用解释器 / 工作目录在启动时**探测**并注入上下文块——不假设特定操作系统。
- **直接宿主执行**：内置工具用 `os/exec` 直接在会话工作目录执行（无沙箱）；文件类工具**禁闭在工作目录内**（jail）。
- **客观成功验证**：`finish(success=true)` 由一个独立、严格的 **LLM-as-judge** 依据任务、摘要、真实产出文件与执行证据复核；不达标则打回继续。
- **带验证的自我学习闭环**：从**已验证成功**的运行中蒸馏可迁移经验存入 `notes`；新任务按相关度（语义 embedding，缺省降级为词面重叠）+ 质量先验召回；近重复经验做强化（wins++），长期无用经验淘汰。
- **安全硬约束**：内置永远生效的灾难命令拦截（`rm -rf`、fork bomb、`mkfs`、`dd of=/dev/…`、pipe-to-shell 等）+ 用户可配置的禁用动作/命令模式，优先于模型意图。
- **总会终止**：`max_steps` + 连续重复检测 + 连续错误检测 + 停滞（无进展）提醒 + `finish`/`reply`。
- **成本可观测**：每轮累计 prompt/completion/total tokens、LLM 调用次数、耗时，实时上报 UI。
- **持久化**：SQLite 记录每次任务（episode）、其中每个工具调用（event）与学习经验（note）。

---

## 快速开始

### CLI

```bash
go test ./...
go build -o bin/agent ./cmd/agent

# 凭据只从环境变量读取，绝不写入代码或配置
export LLM_BASE_URL="https://your-gateway/v1"
export LLM_API_KEY="..."
export LLM_MODEL="your-model"

# 任务从 stdin 或 -task 传入
echo "把 example.com 首页标题抓下来写到 title.txt" | ./bin/agent -config configs/agent.yaml
./bin/agent -config configs/agent.yaml -task "统计当前目录下 .go 文件行数并写入 report.md"
```

### Web UI

```bash
go build -o bin/server ./cmd/server
./bin/server -config configs/agent.yaml   # 默认 http://localhost:8787
```

浏览器打开后是一个三栏式（会话侧栏 / 流式对话 / 运行轨道）界面：

- **流式**：token 级展示模型思考与正文，shell 输出实时滚动。
- **对话/任务双模**：模型自行判断闲聊还是进入任务；会话长期保持。
- **文件/图片收发**：可上传图片给视觉模型、上传文件给 agent；agent 用 `send_file` 回传产物。
- **模型配置**：设置里可配多个 URL 源、每源多模型、独立 embedding 端点；对话框下方可切换模型。
- **可观测面板**：token/成本/耗时、后台服务列表（可停止）、会话标题由模型自动整理。

### 离线 A/B 评测

```bash
go build -o bin/eval ./cmd/eval
./bin/eval -config configs/agent.yaml            # 内置任务集，对比「有/无经验召回」
```

蒸馏在评测期关闭以防污染，报告已验证成功率、步数、token 成本等指标。

---

## 循环（每一步）

```
探测环境（首步）
 → 组装分层 prompt：system + 动态上下文块(current_request/env/plan/召回经验) + 滚动 transcript + 本轮 ask
 → 逼近预算则压缩较早 transcript 轮次为摘要
 → 请求模型（携带 tool 定义，流式），取本步全部 tool_call
   （无原生 tool_call 时，从正文文本兜底解析）
 → 分类批处理：只读查询并发执行，改状态/终止类按序单发
 → 安全门控 → schema 校验 → 执行 → 记录 event → 结果写回 transcript
 → reply?（对话收尾）finish?（验证成功声明，不达标打回）
 → 达步数/重复/连续错误上限? 否则继续
 → 任务已验证成功 → 蒸馏经验入库
```

## 包结构

| 包 | 职责 |
|----|------|
| `internal/domain` | 无依赖核心类型（`Message`/`ToolDef`/`ToolCall`/`LLMRequest`/`LLMResponse`/`Action`/`LLM`）与 `ActionRegistry`（白名单 + schema 校验） |
| `internal/config` | 精简 YAML 配置（llm / limits / context / safety / agent）；任务不在配置里，运行时传入 |
| `internal/llm` | OpenAI-compatible Provider（原生 tool_use、流式、多模态）、embedding、按状态码区分可重试的 `APIError`、超时/重试降级、宽容 JSON 提取 |
| `internal/store` | SQLite：`episodes` / `events` / `notes`；启动时清理上次中断的 running 会话 |
| `internal/tool` | `Tool` 接口 + 注册表 + 内置宿主工具（含后台服务，meta 持久化，重启后可管理） |
| `internal/safety` | 硬约束门控：内置灾难命令拦截 + 用户配置模式，优先于模型意图 |
| `internal/agent` | 反应式 tool_use 循环、分层 prompt + 环境探测、transcript 压缩、成功验证（`verify`）、学习闭环、成本统计 |
| `cmd/agent` | CLI 入口：从 stdin/`-task` 读任务并运行 |
| `cmd/server` | Web UI：SSE 流式、多轮会话、运行时模型配置、文件收发、服务/成本面板 |
| `cmd/eval` | 离线 A/B 评测：度量学习闭环（经验召回）对成功率/成本的影响 |

---

## 配置

见 [`configs/agent.yaml`](configs/agent.yaml)：

- `agent`：`name`（重启恢复身份）、`db_path`、`workspace`（宿主执行目录）、可选 `system_prompt`（**追加**到内置操作 prompt）
- `llm`：`provider`（`openai`）、`model`、`temperature`、`max_tokens`、`timeout`、`max_retries`、`disable_response_format`、`disable_thinking`、`reasoning_effort`
- `limits`：`max_steps` / `max_repeats` / `max_consecutive_errors` / `step_timeout` / `stall_nudge`
- `context`：`max_chars` / `keep_recent` / `compact_at_pct`
- `safety`：`forbidden_actions` / `forbidden_shell_patterns`（叠加在内置灾难命令拦截之上）

凭据（`LLM_BASE_URL` / `LLM_API_KEY` / `LLM_MODEL`，以及可选 `LLM_EMBED_*`）**只**从环境变量读取；
Web UI 里配置的 API key 仅驻留内存（回显时打码），不落盘。

> 网关兼容开关 `llm.disable_response_format`：工具调用模式下本就不请求 JSON 模式，保持 `true` 更稳。

---

## 内置工具

| 工具 | 说明 |
|------|------|
| `run_shell` | 在工作目录 `sh -c` 执行命令，输出可实时流式上报 UI |
| `run_python` | 将代码写入文件并用 Python3 执行；缺库时先用 `run_shell` 安装 |
| `read_file` / `write_file` / `edit_file` | 读写/精确替换 UTF-8 文本（禁闭在工作目录内） |
| `list_dir` / `glob` / `grep` | 目录列举 / 模式找文件 / 内容检索（只读，可并发） |
| `http_fetch` | 对 URL 发 GET，返回状态码与截断正文 |
| `start_service` / `list_services` / `stop_service` | 以后台进程运行长驻命令（如起服务），meta 持久化，重启后仍可管理 |
| `send_file` | 把工作目录里的产物（图片/文件）回传给用户 |
| `set_title` | 由模型给当前会话起一个简短标题 |
| `update_plan` | 设置/更新 TODO 计划（每轮开始自动重置，避免跨任务污染） |
| `reply` | 直接对话作答，单步结束本轮（不进入任务机制） |
| `finish` | 结束任务，给出摘要与 `success`（受独立验证复核） |

> 直接在宿主机执行是本仓库的显式选择（无沙箱）。文件类工具 jail 在工作目录内，
> 且内置灾难命令拦截始终生效；但 shell 并非强隔离，请在你能接受的环境里运行。

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
> 学习只发生在框架层（外化的经验记忆 + 反思），LLM 权重始终冻结。
