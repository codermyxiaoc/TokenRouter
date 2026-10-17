// MJ 前缀统一识别为图片模型，同时保留已有 Midjourney 数字版本名称的兼容。
// 接口仍保留用户配置的大小写，不在展示识别时改写真实请求模型。
export function isMidjourneyImageModel(model: string): boolean {
  const normalized = model.trim().toLowerCase()
  return normalized.startsWith('mj') || /^midjourney[-_]v\d+(?:\.\d+)*$/.test(normalized)
}
