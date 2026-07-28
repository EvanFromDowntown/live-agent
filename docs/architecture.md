# 架构设计（v2）

## 1. 设计原则

1. **LLM 只是冻结的认知模块**：不训练、不微调，只改变 prompt 输入与对输出的校验/执行。
2. **反应式、原生 tool_use**：每步向模型要一个 tool_call（训练分布内），执行后把真实结果反馈，闭环自我调试。
3. **混合循环**：模型用 `update_plan` 维护 TODO，底层按单步 tool_call 反应式推进。
4. **动作白名单**：动作只能是预注册、schema 校验过的工具；硬约束优先于模型意图。
5. **环境运行时注入**：不假设操作系统；OS/shell/解释器/cwd 启动时探测并注入上下文。
6. **总会终止**：`max_steps` + 重复检测 + 连续错误检测 + `finish`。
7. **可审计**：每个 episode 与其中每个 event（工具调用 + 结果）落 SQLite。

> v2 暂不含持续学习（原则/技能/反思/进化）；先做扎实「能干活」的核心，之后再加带验证的学习。

## 2. 模块边界与依赖方向

依赖单向指向 `domain`，无循环依赖：

```
                    ┌────────────┐
                    │   domain   │  核心类型 + ActionRegistry（零依赖）
                    └─────▲──────┘
        ┌───────────┬─────┼──────┬───────────┐
      config       llm   tool  safety      store
        ▲           ▲     ▲      ▲            ▲
        └───────────┴──┬──┴──────┴─────┬──────┘
                     agent  ← 反应式 tool_use 循环 + prompt/上下文管理
                       ▲
                    cmd/agent  ← 装配 + stdin/-task 输入
```

## 3. 单步流程

```
探测环境（首步一次）
 → system prompt（内置操作常识，OS 无关）
 → 动态上下文块（task / 探测到的 environment / 当前 plan）——每步重建，永不被压缩
 → 滚动 transcript（assistant: 工具调用；user: 真实结果）
 → 逼近预算(默认 75%)则把较早轮次压成一条摘要，保留最近 keep_recent 轮
 → 请求模型（携带工具定义，tool_choice=auto）
 → 取第一个原生 tool_call；若无，则从正文文本兜底解析已注册工具调用
 → 安全门控（禁用动作 / 危险命令模式）
 → schema 校验（必填/未知参数/类型）
 → 执行（宿主 os/exec，工作目录内，带 step_timeout）
 → 记录 event；结果写回 transcript
 → finish → 结束；或 达 max_steps / 同一调用重复 max_repeats / 连续错误 max_consecutive_errors → 停止
```

## 4. 上下文管理

- 短期工作记忆 = 单次任务的 transcript，运行结束不跨任务保留。
- 长期记忆 = SQLite（`episodes`/`events`/`notes`）；`notes` 为后续「带验证的学习」预留。
- pin 不压缩项：system prompt、task、探测环境、当前 plan（都在每步重建的 ask 里）。
- 压缩：超过 `max_chars * compact_at_pct` 时，把 keep_recent 之前的轮次交给 LLM 概括为一条摘要。

## 5. 已知取舍

- **无沙箱**：工具直接在宿主执行（显式决策）。`forbidden_shell_patterns` 只拦明显危险命令，非强隔离。
- **transcript 文本表示**会诱导部分模型把工具调用写成正文；已用「文本兜底解析」回收这类步骤。
- **`finish` 的 success 由模型自述**，v2 不做客观校验（等带验证的学习阶段补上）。
