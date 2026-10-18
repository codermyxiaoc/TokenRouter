// 只公开内置节点颜色；不加载远程插件或可执行扩展。
export function getNodeDefinition(type: string) {
  return {minimapColor: ({image:'#82bca3',video:'#9b91c9',text:'#c4b58b',audio:'#b695a6',config:'#a9acb0',group:'#777'} as Record<string,string>)[type]};
}
