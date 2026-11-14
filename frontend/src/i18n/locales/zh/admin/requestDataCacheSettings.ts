// 系统设置 - 数据缓存
export default {
  requestDataCacheSettings: {
    title: '数据缓存',
    description: '缓存使用记录中请求的端点信息（入站/上游端点、方法、模型、状态等），普通请求与错误请求分别设置。到期自动清理，也可手动清除。',
    normalTitle: '普通请求缓存',
    normalHint: '成功请求的数据缓存。开启后每条请求都会额外写入一份快照，请按需限定分组与时长。',
    errorTitle: '错误请求缓存',
    errorHint: '失败请求的数据缓存，默认对全部分组开启、保留 30 天，与原有行为一致。',
    enabled: '启用缓存',
    ttl: '缓存时间',
    units: { minutes: '分钟', hours: '小时', days: '天' },
    ttlInvalid: '缓存时间需在 1 分钟到 365 天之间',
    scope: '缓存分组',
    allGroups: '全部分组',
    selectedGroups: '指定分组',
    includeUngrouped: '同时缓存未绑定分组的请求',
    groupsRequired: '请至少选择一个分组，或勾选“未绑定分组的请求”',
    cachedCount: '当前缓存 {count} 条',
    clear: '清除缓存',
    clearAll: '清除全部缓存',
    clearConfirmTitle: '清除数据缓存',
    clearConfirm: '确定清除{target}吗？清除后用户将无法再查看这些请求的端点信息，此操作不可恢复。',
    targetNormal: '普通请求缓存',
    targetError: '错误请求缓存',
    targetAll: '全部数据缓存',
    cleared: '已清除 {count} 条缓存',
    saved: '数据缓存设置已保存',
    loadFailed: '加载数据缓存设置失败',
    saveFailed: '保存数据缓存设置失败',
    clearFailed: '清除缓存失败'
  }
}
