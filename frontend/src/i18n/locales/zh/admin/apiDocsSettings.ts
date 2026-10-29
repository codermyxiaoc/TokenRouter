export default {
  // 系统设置：API 文档 Markdown 编辑
  apiDocsSettings: {
    title: 'API 文档',
    description: '文档页的每一篇（快速开始、认证、各接口）都以 Markdown 渲染。在此改写某一篇后，公开文档页立即显示新内容；未改写的文档跟随系统内置版本更新。正文中的 {placeholder} 会替换为当前站点的网关地址。',
    openPage: '打开文档页',
    document: '文档',
    customized: '已自定义',
    builtin: '内置',
    reset: '恢复默认',
    resetConfirm: '恢复该文档的内置内容？当前自定义内容将被删除。',
    markdown: 'Markdown',
    preview: '预览',
    unsaved: '有未保存的修改',
    discardConfirm: '当前文档有未保存的修改，确定放弃并切换？',
    loadFailed: '加载 API 文档失败',
    saveFailed: '保存 API 文档失败',
    conflict: '该文档在你编辑期间已被其他管理员修改，已刷新最新保存版本；你的草稿仍保留，确认后再次保存将覆盖对方的修改。',
    saved: 'API 文档已保存',
  },
}
