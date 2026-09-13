// 判断分组是否支持 OpenAI Fast 强制策略。
export function supportsGroupOpenAIFast(platform: string): boolean {
  return platform === "openai" || platform === "composite";
}

// 仅在支持的平台上保留开关值，避免前端提交无效配置。
export function normalizeGroupOpenAIFast(
  platform: string,
  enabled: boolean,
): boolean {
  return supportsGroupOpenAIFast(platform) && enabled;
}
