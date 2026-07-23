# live-agent — 基于冻结 LLM 的持续学习 Agent Runtime

一个用 Go 实现的常驻 Agent 运行时。它**不训练、不微调**任何模型：LLM 只是一个**冻结的认知模块**。
Agent 通过长期运行、环境交互、持久化记忆、语言反思、经验规则、策略版本与技能复用，
在固定权重之上表现出跨任务的持续成长。

- 无 SFT / DPO / RL / LoRA / 任何权重更新
- 自我迭代仅包括：持久化经历、语言反思、经验规则、策略版本、技能库、世界/自我模型更新
- 根目标、安全约束、奖励权重由**人类**定义，Agent 不可修改
- 动作只能通过预注册的 **Action Registry** 执行，禁止执行 LLM 生成的任意 Shell/SQL/代码
- 无需 API Key，`FakeLLM` 让全部测试与 GridWorld Demo 完全离线运行

---

## 快速开始

```bash
# 运行全部测试（完全离线）
go test ./...

# 运行 GridWorld Demo（默认使用 FakeLLM，无需任何密钥）
go run ./cmd/agent -config configs/gridworld.yaml

# 单步运行一个 Tick
go run ./cmd/agent -config configs/gridworld.yaml -step

# 覆盖最大 Tick 数
go run ./cmd/agent -config configs/gridworld.yaml -max-ticks 100
```

进程重启后会自动从 SQLite 恢复同一 `AgentID`、年龄、记忆和已激活的原则/技能，年龄继续累加。

Demo 会打印结构化 JSON 日志，可以观察到：探索 → 能量耗尽 → 学会休息恢复 → 稳态振荡 →
失败产生候选原则 → 累积证据后原则激活 → 技能 `recover_energy` 被检索并复用。

---

## 架构

```
Observe → 更新世界/自我模型 → 检索经验/原则/技能 → 选择目标 → 生成计划
→ 校验计划与权限 → 安全门控 → 执行动作 → 接收环境结果与奖励向量
→ 保存完整轨迹 → 反思成败 → 生成候选原则/技能 → 评测通过后激活 → 下一 Tick
```

分层（详见 [`docs/architecture.md`](docs/architecture.md)）：

| 包 | 职责 |
|----|------|
| `internal/domain` | 无依赖的核心类型与接口（`Environment`/`Embodiment`/`LLM`/`Memory`/`Evaluator`/`Agent`）、`ActionRegistry` |
| `internal/config` | 人类定义的 YAML 配置（根目标、奖励权重、安全约束、身体 schema、循环参数） |
| `internal/llm` | `FakeLLM` + OpenAI-compatible Provider，超时/重试/降级、严格 JSON 提取 |
| `internal/memory` | SQLite 存储（8 张表）、可替换的 Retrieval Scorer |
| `internal/safety` | 硬约束门控，**优先于奖励最大化** |
| `internal/cognition` | `Agent` 实现：目标/计划/动作/在线自我模型更新，每步都有确定性降级 |
| `internal/reflection` | Episode 结束后调用 LLM，产出严格 JSON 反思（校验 + 重试一次） |
| `internal/evolution` | 原则/技能生命周期：`candidate → evaluated → active → deprecated`，去重合并证据 |
| `internal/evaluation` | 原则/技能激活的门控（证据/置信度阈值、技能只引用注册动作） |
| `internal/skills` | 受限 JSON DSL 技能展开为已注册动作序列 |
| `internal/embodiment` | 由 schema 驱动的身体适配器（字段不写死在代码里） |
| `internal/kernel` | 常驻主循环：优雅关闭、Tick 超时、单步、max-tick、pause/resume、状态落盘、重启恢复 |
| `examples/gridworld` | 可离线运行的示例环境（2D 地图、资源、障碍、energy/health/age） |
| `cmd/agent` | 入口，负责组装依赖 |

状态变量分为三类：

- **Immutable**：人类定义（根目标、奖励权重、安全约束），Agent 不可修改
- **Observed**：由环境和身体适配器提供（位置、能量、健康）
- **Learned**：由 Agent 从经验更新（`SelfModel` 的能力置信度、风险预测；原则/技能）

---

## 配置说明

见 [`configs/gridworld.yaml`](configs/gridworld.yaml)。关键段落：

- `agent`：名称与 SQLite 路径（`name` 用于重启时恢复同一身份）
- `runtime`：`max_ticks` / `tick_timeout` / `tick_interval` / `step_mode`
- `llm`：`provider`（`fake` 或 `openai`）、`model`、`temperature`、`timeout`、`max_retries`
- `reward.weights`：多维奖励向量的标量化权重（`resource_cost`/`risk_cost` 作为惩罚）
- `safety.constraints`：硬约束（`forbidden_action` / `body_min` / `body_max`）
- `evolution`：`min_evidence` / `min_confidence` / `min_success_rate`
- `goals`：人类定义的**根目标**（Agent 只能在其中选择）
- `body.fields`：身体状态 schema（字段在此声明，而非硬编码）
- `environment`：环境类型与参数

---

## 如何接入 OpenAI-compatible API

密钥**只**从环境变量读取，绝不写入代码或配置：

```bash
export LLM_BASE_URL="https://api.openai.com/v1"   # 任意兼容端点
export LLM_API_KEY="sk-..."                        # 不要提交到仓库
export LLM_MODEL="gpt-4o-mini"

# 将 configs/gridworld.yaml 中 llm.provider 改为 openai，然后：
go run ./cmd/agent -config configs/gridworld.yaml
```

Provider 使用标准 `/chat/completions`，对目标/计划/反思启用 `response_format: json_object`，
并由 `internal/llm` 的重试/超时层做错误降级；任一 LLM 决策失败都会回退到确定性启发式，Agent 不会卡死。

---

## 如何接入新 Environment

实现 `domain.Environment` 接口：

```go
type Environment interface {
    Observe(ctx context.Context) (Observation, error)
    AvailableActions(ctx context.Context) []ActionSchema   // 这些会成为 Action Registry 白名单
    Execute(ctx context.Context, action Action) (Outcome, error)
    Tick() int64
}
```

在 `Outcome.Info` 中设置 `episode_done` / `episode_success` / `death` 以驱动 Episode 边界。
在 `cmd/agent/main.go` 的 `buildEnvironment` 中注册新的 `environment.type`。
参考 `examples/gridworld`。

## 如何接入新 Embodiment

在 `body.fields` 中声明身体字段（不改代码），并让环境实现 `embodiment.BodySource`：

```go
type BodySource interface { BodyState() map[string]any }
```

`embodiment.Adapter` 会按 schema 过滤/暴露 `Proprioception` 与 `Health`。
新增字段（如 `temperature`、`battery`）只需改 YAML。

## 如何注册 Action

动作通过 `ActionRegistry` 白名单化。默认由 `Environment.AvailableActions` 填充：

```go
registry := domain.NewActionRegistry()
registry.RegisterAll(env.AvailableActions(ctx))
```

`registry.Validate(action)` 校验：动作已注册、必填参数存在、无未知参数、参数类型正确。
技能（`internal/skills`）在展开时会再次校验每一步——**技能只能调用已注册动作，绝不执行任意代码**。

---

## 验收标准对应

| # | 验收项 | 覆盖测试 |
|---|--------|----------|
| 1 | 状态正确持久化 | `kernel.TestRunPersistAndRecover` |
| 2 | 重启恢复同一 AgentID/年龄/记忆 | `kernel.TestRunPersistAndRecover` / `memory.TestPersistenceAndRecoveryAcrossRestart` |
| 3 | 非法动作被 Registry 拒绝 | `domain.TestRegistry*` |
| 4 | 违反硬约束的高奖励动作被拒 | `safety.TestForbiddenActionRejectedRegardlessOfReward` |
| 5 | 失败 Episode 产生候选原则 | `evolution.TestFailedEpisodeProducesCandidatePrinciple` / `kernel.TestLoopProducesPrinciples` |
| 6 | 未通过评测的原则不激活 | `evolution.TestPrincipleActivatesOnlyAfterEnoughEvidence` |
| 7 | 相似场景检索到已激活原则 | `memory.TestRetrieveActivePrincipleForMatchingTags` |
| 8 | 技能只能调用注册动作 | `evaluation.TestEvaluateSkillRejectsUnregisteredAction` |
| 9 | FakeLLM 离线运行 | `llm.TestFakeLLM*`（全部测试均离线） |
| 10 | `go test ./...` 通过 | 全绿 |
| 11 | `go run ./cmd/agent -config configs/gridworld.yaml` 可启动 | Demo 可运行 |

---

## 当前限制与下一阶段建议

**限制**
- 检索为标签 + 关键词 + 新鲜度 + 重要性 + 成功率的启发式，尚无向量/Embedding。
- 计划为“每 Tick 重规划、执行首个动作”的反应式策略，多步技能靠逐 Tick 重选复用，非严格顺序执行。
- 原则冲突仅按文本归一化合并，缺少语义级冲突消解与反例衰减。
- GridWorld 为演示环境，奖励与地图较简单；成功/失败以探索比例判定。
- 评测尚未包含“模拟环境回放”，目前主要依据证据数量与置信度阈值。

**下一阶段**
- 实现 Embedding 版 `Scorer`（接口已就绪，可直接替换）。
- 引入基于历史轨迹回放的原则/技能离线评测（`evaluation_runs` 表已预留）。
- 计划缓存与多步技能的顺序执行 + 前置条件校验。
- 原则的反例统计与自动 `deprecated` 降级、置信度衰减。
- 更丰富的世界模型与人类反馈（`HumanFeedback` 维度）接入。

> 本项目**不包含**任何模型微调、自动后训练或让 Agent 修改根目标/奖励/安全约束的功能，且不会加入。
