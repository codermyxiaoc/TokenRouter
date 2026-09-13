/** 表格从该宽度开始使用桌面布局；更窄视口统一使用逐行卡片布局。 */
export const TABLE_DESKTOP_MIN_WIDTH = 1024

/** 表格相关组件共用同一媒体查询，避免外框与内容落入不同响应式模式。 */
export const TABLE_DESKTOP_MEDIA_QUERY = `(min-width: ${TABLE_DESKTOP_MIN_WIDTH}px)`
