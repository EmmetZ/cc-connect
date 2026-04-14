# Codex Observe 事件对齐说明

本文档说明 `agent/codex/observe*.go` 与 `agent/codex/session.go` 的职责边界，以及 `/observe` 与正常对话在消息发送链路上的一致性约束。

## 设计原则

- `agent/codex/session.go` 负责把 Codex 交互会话输出转换成 `core.Event`
- `agent/codex/observe*.go` 负责把 rollout 日志转换成同一套 `core.Event`
- 真正的渲染与发送不在 agent 层完成，而是统一由 `core` 层处理

对应代码入口：

- 交互会话事件接口：`core/interfaces.go`
- 正常对话渲染发送：`core/engine.go`
- observe 渲染发送：`core/observe.go`

这意味着：

1. 对话模式不是单独一套渲染逻辑，本质上也是消费 `core.Event`
2. 不同 Code CLI 适配器的主要职责是“转 Event”，而不是自己拼接最终展示文本
3. `/observe` 必须尽量复用正常对话的事件语义，避免 agent 层再次实现发送格式

## Observe 的对齐范围

`observe` 只转换与 `agent/codex/session.go` 中以下两处逻辑等价的类型：

- `handleItemStarted`
- `handleItemCompleted`

也就是说，`observe` 只负责补齐“正常对话时本来就会发出去的内容”，不额外转换 rollout 中的扩展/调试信息。

## 当前事件映射

### 1. 思考类消息

- rollout `event_msg.agent_reasoning` -> `core.EventThinking`
- rollout `response_item.reasoning` -> `core.EventThinking`
- rollout `event_msg.agent_message` 且 `phase=commentary` -> `core.EventThinking`
- rollout `response_item.message` 且 `role=assistant` 且 `phase=commentary` -> `core.EventThinking`

这对应 `session.go` 里：

- `reasoning` 完成后直接发 `EventThinking`
- 暂存中的 agent 消息在后续出现工具调用时会被冲刷成 `EventThinking`

### 2. 文本类消息

- rollout `event_msg.agent_message` 且非 `commentary` -> `core.EventText`
- rollout `response_item.message` 且 `role=assistant` 且非 `commentary` -> `core.EventText`

这对应 `session.go` 里：

- `agent_message` / `message` 先进入 `pendingMsgs`
- 在 `turn.completed` 时由 `flushPendingAsText()` 统一发成 `EventText`

rollout 中已经显式提供了 `phase`，所以 observe 直接按 `phase` 落到 `Thinking` / `Text`，达到与交互会话相同的用户可见语义。

### 3. 工具调用

- rollout `response_item.function_call` -> `core.EventToolUse`
  - `name=exec_command` 时格式化为 `ToolName=Bash`，`ToolInput=cmd`
  - 其他函数调用保持 `ToolName=name`，`ToolInput=arguments`
- rollout `response_item.web_search_call` -> `core.EventToolUse`
- rollout `response_item.file_search_call` -> `core.EventToolUse`
- rollout `response_item.code_interpreter_call` -> `core.EventToolUse`
- rollout `response_item.computer_use_call` -> `core.EventToolUse`
- rollout `response_item.mcp_tool_call` -> `core.EventToolUse`

这对应 `session.go` 里：

- `command_execution` / `function_call` 的 started 事件
- `web_search` / `file_search` / `code_interpreter` / `computer_use` / `mcp_tool` 在 completed 阶段补发的工具使用事件

### 4. 工具结果

- rollout `event_msg.exec_command_end` -> `core.EventToolResult`
  - `ToolName=Bash`
  - 带 `ToolResult` / `ToolStatus` / `ToolExitCode` / `ToolSuccess`

这对应 `session.go` 里 `command_execution` completed 后的 `EventToolResult`。

### 5. 会话结束

- rollout `event_msg.task_complete` -> `core.EventResult`

这对应 `session.go` 里：

- `turn.completed`
- `flushPendingAsText()`
- 再发送 `EventResult`

## 明确不转换的 rollout 内容

以下内容不属于 `session.go` 当前会发送给核心层的事件，因此 observe 也不转换：

- `response_item.function_call_output`
- `response_item.custom_tool_call`
- `response_item.custom_tool_call_output`
- `response_item.message` 中非 assistant 角色消息
- `event_msg.user_message`
- `event_msg.token_count`
- `event_msg.task_started`
- `event_msg.thread_rolled_back`
- `event_msg.turn_aborted`
- `event_msg.context_compacted`

这样做的原因很简单：这些内容要么是输入/元信息，要么并不是当前交互会话对外发送的消息类型。observe 如果私自把它们转成额外事件，就会和正常对话的输出语义产生偏差。

## 一致性检查结论

基于当前 Codex rollout 结构，正常对话中真正会被用户看到的主要内容形式包括：

- 思考文本
- assistant 正文/最终答复
- 工具调用
- Bash 工具结果
- 会话最终完成结果

这些内容现在都已经在 observe 中转换为与交互会话一致的 `core.Event`。

换句话说：

- observe 和正常对话共享同一套事件模型
- 渲染与发送都由 `core` 层统一负责
- agent/codex 只保留最小必要的协议适配职责
