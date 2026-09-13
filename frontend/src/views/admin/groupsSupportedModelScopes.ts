export const normalizeSupportedModelScopesForPlatform = (
  platform: string,
  scopes: string[] | undefined,
): string[] => {
  // 非 Antigravity 分组不支持模型系列，提交时主动清空隐藏表单值。
  if (platform !== "antigravity") return [];
  return scopes ?? [];
};
