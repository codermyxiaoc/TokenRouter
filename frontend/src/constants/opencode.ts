// OpenCode 平台的内置模型快照；实际可用目录仍由账号同步结果决定。
export const OPENCODE_DEFAULT_MODEL = 'gpt-5.6-luna'
export const OPENCODE_MODELS = [
  'grok-4.6', 'gpt-5.6-luna',
  // Zen System One 结构化决策模型；GO 账号由后端能力校验决定是否可用。
  'jev-1.13', 'jev-1.13-free',
  'glm-5.3-flash', 'glm-5.3', 'glm-5.2', 'glm-5.1',
  'kimi-k3', 'kimi-k2.7-code', 'kimi-k2.6',
  'longcat-2.0',
  'deepseek-v4-pro', 'deepseek-v4-flash', 'deepseek-v4-flash-vision-exp',
  'mimo-v2.5', 'mimo-v2.5-pro',
  'minimax-m3', 'minimax-m2.7', 'minimax-m2.5',
  'muse-spark-1.3-contributor', 'muse-spark-1.2-contributor',
  'qwen3.8-max', 'qwen3.8-flash', 'qwen3.7-max', 'qwen3.7-plus', 'qwen3.6-plus',
  'hy4-preview', 'hy3', 'omen-alpha'
] as const
