# 架构设计

## 1. 设计原则

1. **LLM 只是认知模块，不是整个 Agent。** 权重冻结，只改变 prompt 输入与对输出的校验。
2. **Agent 由独立 Runtime 持续驱动**，不依赖用户输入才运行。
3. **无任何权重更新**（无 SFT/DPO/RL/LoRA）。自我迭代 = 持久化经历 + 语言反思 + 经验规则 +
   策略版本 + 技能库 + 世界/自我模型更新。
4. **根目标、安全约束、奖励权重由人类定义**，Agent 不可修改。
5. Agent 可生成候选规则/技能，但必须**经过评测才能激活**。
6. **禁止执行任意代码**：动作只能通过预注册的 Action Registry 执行。
7. 所有重要状态、动作、奖励、版本变化**可审计、可回滚**（`events` / `evaluation_runs` /
   `policy_versions` 表）。

## 2. 模块边界与依赖方向

依赖单向指向 `domain`，无循环依赖：

```
                         ┌────────────┐
                         │   domain   │  核心类型 + 接口 + ActionRegistry（零依赖）
                         └─────▲──────┘
      ┌───────────┬───────────┼───────────┬───────────┬───────────┐
   config       llm        memory       safety     evaluation    skills
      ▲           ▲           ▲            ▲            ▲            ▲
      └───────────┴─────┬─────┴─────┬──────┴─────┬──────┴────┬───────┘
                     cognition   reflection   evolution   embodiment
                        ▲            ▲            ▲            ▲
                        └────────────┴─────┬──────┴────────────┘
                                        kernel  ← 常驻主循环（编排）
                                           ▲
                                        cmd/agent  ← 组装依赖
                                           ▲
                                      examples/gridworld（示例 Environment/BodySource）
```

- `domain` 定义题目要求的六大接口：`Environment` / `Embodiment` / `LLM` / `Memory` /
  `Evaluator` / `Agent`，以及安全关键的 `ActionRegistry`。
- 各具体实现只依赖接口，可整体替换场景、LLM、记忆、评测器。

## 3. 主循环（`internal/kernel`）

`Runtime.Step` 执行一个 Tick：

1. `Embodiment.Proprioception` → 更新 `AgentState.BodyState`
2. `Environment.Observe` → 观察
3. `Agent.UpdateState` → 更新世界/自我模型
4. 若无进行中的 Episode 则开启一个
5. `Agent.SelectGoal`（检索原则/技能）→ `Agent.Plan` → `Agent.SelectAction`
6. **安全门控**：`safety.Guard.Check`，被禁动作直接拒绝并记录 `safety_reject` 事件（不执行）
7. `Environment.Execute` → `Outcome`（含多维 `RewardVector`）
8. 刷新身体状态
9. 构造 `Experience`，`Agent.Learn`（追加 + 在线更新自我模型），加入 Episode 轨迹
10. 若 `episode_done`：保存 Episode → `Reflector.Reflect` → `evolution.ProcessReflection`
11. `SaveAgentState` 落盘

支持：优雅关闭（`context` + SIGINT/SIGTERM）、Tick 超时、单步（`-step`）、
最大 Tick（`max_ticks`）、`Pause()`/`Resume()`、每 Tick 结构化 JSON 日志、重启恢复。

## 4. 状态设计（`domain.AgentState`）

- **Immutable**：根目标、奖励权重、安全约束（来自 config，运行时只读）。
- **Observed**：`WorldState`、`BodyState`（环境/身体适配器提供）。
- **Learned**：`SelfModel.Capabilities`（动作成功率 EMA）、`RiskEstimates`、`ActiveSkills`、
  以及记忆库中的原则/技能。

身体字段**不硬编码**：由 `config.body.fields` 声明，`embodiment.Adapter` 按 schema 暴露。

## 5. 记忆系统（`internal/memory`，SQLite）

八张表：`agents` / `events` / `experiences` / `episodes` / `principles` / `skills` /
`policy_versions` / `evaluation_runs`。

记忆分层：

1. **原始事件日志**（`events`、`experiences`）：只追加。
2. **情景记忆**（`episodes`）：一次任务的完整轨迹。
3. **语义原则**（`principles`）：从多经历提炼的规则。
4. **程序性技能**（`skills`）：受限 JSON DSL，只引用注册动作。
5. **价值记忆**：原则的适用条件、成功率、置信度。

检索为可替换的 `Scorer` 接口，MVP 实现：
`标签匹配 + 关键词匹配 + 时间新鲜度 + 重要性(置信度) + 历史成功率`，并对 `active` 加权、
对 `deprecated` 强降权。后续可无缝替换为 Embedding。

## 6. 反思与持续学习

- 每个 Episode 结束调用 LLM，要求严格 JSON：
  `{success, cause, principle, applicable_conditions, confidence, suggested_skill}`。
- 解析失败重试一次；仍失败则保存原始输出（`reflection_unparsed` 事件）但**不更新原则**。
- 原则生命周期：`candidate → evaluated → active → deprecated`。
  单次经历不能直接激活；需达到 `min_evidence` 且通过 `Evaluator` 阈值。
- 重复原则**合并**证据、成功率、置信度，而非无限新增；技能按名称去重更新。

## 7. 奖励系统

`RewardVector` 六维：`TaskSuccess / Homeostasis / InformationGain / HumanFeedback /
ResourceCost / RiskCost`。权重来自 YAML，Agent 不可改。标量化仅发生在 `config.RewardWeights.Scalarize`
一处（cost 维度作为惩罚）。**安全硬约束优先于奖励**：违反约束的动作即使奖励极高也被拒绝。

## 8. 安全与动作白名单

- `ActionRegistry` 是动作执行的唯一入口：校验注册、必填参数、未知参数、类型。
- `safety.Guard` 在规划与执行前双重检查硬约束（`forbidden_action`/`body_min`/`body_max`），
  均为内置声明式检查器，绝不执行任意表达式。
- 技能展开（`skills.Expander`）逐步再次经过 Registry 校验。

## 9. LLM Provider（`internal/llm`）

- `FakeLLM`：确定性、离线、规则化，解析 `@@CTX@@` 上下文块为各任务产出合法 JSON，
  保证测试与 Demo 无需密钥稳定运行。
- `OpenAIProvider`：标准 `/chat/completions`，密钥仅从 `LLM_BASE_URL`/`LLM_API_KEY`/`LLM_MODEL`
  环境变量读取。
- `Retrying` 包装层：每次调用超时 + 有界重试 + 失败降级（回退启发式）。
- 所有结构化输出都经过：JSON 提取（容忍代码围栏/散文）、schema 校验、动作白名单校验、
  参数类型校验。
