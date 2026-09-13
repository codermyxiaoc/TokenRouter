# Project Doc 建库备忘录

> 本文件只在首次建库未完成时存在。每完成一个工作单元立即更新；所有完成条件满足后删除本文件。

## 当前状态

- 仓库：<code>{{repository_root}}</code>
- 文档根：<code>{{doc_root}}</code>
- 主语言：<code>{{primary_language}}</code>
- 总体状态：<code>{{planned|in_progress|blocked}}</code>
- 当前项目：<code>{{current_item_or_none}}</code>
- 下一项唯一动作：{{next_action}}
- 最后更新：<code>{{timestamp_with_timezone}}</code>

## 结构决策

- {{decision_and_reason}}

## 目录计划

| 路径 | 分类 | 范围与读取时机 | 权威来源 | 遗留处置 | 状态 | 下一步 |
| --- | --- | --- | --- | --- | --- | --- |
| <code>{{document_path}}</code> | <code>{{category}}</code> | {{scope_and_read_when}} | <code>{{source_paths}}</code> | <code>{{adopt|update|merge|split|new}}</code> | <code>{{planned|in_progress|verified|blocked}}</code> | {{item_next_action}} |

## 锚点计划

| 文档章节 | 稳定 ID | 代码语义入口 | 状态 |
| --- | --- | --- | --- |
| <code>{{document_path}}#{{section_id}}</code> | <code>{{section_id}}</code> | <code>{{code_path_or_symbol}}</code> | <code>{{planned|in_progress|verified|blocked}}</code> |

## 遗留文档

| 原路径 | 处置 | 目标路径或理由 | 引用修复状态 |
| --- | --- | --- | --- |
| <code>{{legacy_path}}</code> | <code>{{adopt|update|merge|split|exclude}}</code> | {{target_or_reason}} | <code>{{planned|in_progress|verified|blocked}}</code> |

## 技能门禁

| Agent 入口 | 操作 | 状态 |
| --- | --- | --- |
| <code>{{instruction_path}}</code> | <code>{{create|update|unchanged}}</code> | <code>{{planned|in_progress|verified|blocked}}</code> |

## 阻塞项与未决问题

- <code>{{blocked_or_none}}</code>

## 验证记录

| 时间 | 范围 | 检查 | 结果 |
| --- | --- | --- | --- |
| <code>{{timestamp}}</code> | <code>{{scope}}</code> | {{validation}} | {{result}} |

## 交接说明

- 已核实：{{verified_summary}}
- 不要重复：{{completed_work_to_preserve}}
- 恢复入口：{{exact_file_symbol_or_command_to_inspect_next}}
- 当前风险：{{known_risk_or_none}}
