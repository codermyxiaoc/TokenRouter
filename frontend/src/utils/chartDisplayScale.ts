// 圆环图没有可配置的数值轴，因此先把正值转换为相对最小正值的 log10 距离。
// 加 1 保证最小正值仍有可见扇区；转换结果只用于图形，tooltip 与表格继续使用原始值。
export const toLogarithmicDisplayValues = (values: number[]): number[] => {
  const positives = values.filter((v) => v > 0)
  if (positives.length === 0) return values
  const min = Math.min(...positives)
  const minLog = Math.log10(min)
  return values.map((v) => (v > 0 ? Math.log10(v) - minLog + 1 : 0))
}
