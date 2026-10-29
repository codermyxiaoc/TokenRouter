<template>
  <!-- API 文档 Markdown 正文：渲染后净化，站内链接走前端路由，二级标题按顺序编号供大纲跳转 -->
  <div ref="root" class="api-doc-markdown" data-testid="api-doc-markdown" @click="handleClick" v-html="html"></div>
</template>

<script setup lang="ts">
import { computed, nextTick, ref, watch } from 'vue'
import { useRouter } from 'vue-router'
import { Marked } from 'marked'
import DOMPurify from 'dompurify'

const props = defineProps<{ markdown: string }>()
const router = useRouter()
const root = ref<HTMLElement | null>(null)

// 使用独立实例，避免修改全局 marked 配置影响其它页面。
const parser = new Marked({ gfm: true, breaks: false })

// 净化后再把表格包进可横向滚动的容器，保持表格自身的列布局。
const html = computed(() => DOMPurify.sanitize(parser.parse(props.markdown || '') as string)
  .replace(/<table>/g, '<div class="api-doc-table-wrapper"><table>')
  .replace(/<\/table>/g, '</table></div>'))

// 二级标题编号为 doc-heading-0…，与 apiDocOutline 的顺序一致。
watch(html, async () => {
  await nextTick()
  root.value?.querySelectorAll('h2').forEach((heading, index) => { heading.id = `doc-heading-${index}` })
}, { immediate: true })

// 站内相对链接（如 /docs/xxx）交给前端路由，避免整页刷新；外部链接在新窗口打开。
function handleClick(event: MouseEvent) {
  const anchor = (event.target as HTMLElement | null)?.closest('a')
  const href = anchor?.getAttribute('href') || ''
  if (!anchor || !href || event.metaKey || event.ctrlKey || event.shiftKey || event.button !== 0) return
  if (href.startsWith('/') && !href.startsWith('//')) {
    event.preventDefault()
    void router.push(href)
  } else if (/^https?:\/\//i.test(href)) {
    anchor.setAttribute('target', '_blank')
    anchor.setAttribute('rel', 'noopener noreferrer')
  }
}
</script>

<style scoped>
.api-doc-markdown { @apply min-w-0 break-words text-sm leading-7 text-gray-600 dark:text-dark-300; }
.api-doc-markdown :deep(h1) { @apply mb-4 text-2xl font-bold tracking-tight text-gray-900 dark:text-white; }
.api-doc-markdown :deep(h2) { @apply mb-3 mt-8 scroll-mt-20 text-base font-semibold text-gray-900 dark:text-white; }
.api-doc-markdown :deep(h3) { @apply mb-2 mt-5 text-sm font-semibold text-gray-900 dark:text-white; }
.api-doc-markdown :deep(p) { @apply my-3; }
.api-doc-markdown :deep(ul) { @apply my-3 list-disc space-y-2 pl-5; }
.api-doc-markdown :deep(ol) { @apply my-3 list-decimal space-y-2 pl-5; }
.api-doc-markdown :deep(a) { @apply text-primary-600 hover:underline dark:text-primary-300; }
.api-doc-markdown :deep(strong) { @apply font-semibold text-gray-900 dark:text-white; }
.api-doc-markdown :deep(:not(pre) > code) { @apply rounded bg-gray-100 px-1.5 py-0.5 font-mono text-xs text-primary-700 dark:bg-dark-700 dark:text-primary-300; }
.api-doc-markdown :deep(pre) { @apply my-3 max-w-full overflow-x-auto rounded-xl bg-gray-900 p-4 text-xs leading-6 text-gray-100 dark:bg-dark-900; }
.api-doc-markdown :deep(pre code) { @apply whitespace-pre font-mono; }
.api-doc-markdown :deep(table) { @apply my-3 w-full border-collapse text-left text-xs; }
.api-doc-markdown :deep(th) { @apply whitespace-nowrap border border-gray-200 bg-gray-50 px-3 py-2 font-semibold text-gray-500 dark:border-dark-600 dark:bg-dark-800 dark:text-dark-300; }
.api-doc-markdown :deep(td) { @apply border border-gray-200 px-3 py-2 align-top leading-6 dark:border-dark-600; overflow-wrap: anywhere; }
.api-doc-markdown :deep(.api-doc-table-wrapper) { @apply my-3 max-w-full overflow-x-auto rounded-xl border border-gray-200 dark:border-dark-600; }
.api-doc-markdown :deep(.api-doc-table-wrapper table) { @apply my-0 min-w-[560px]; }
.api-doc-markdown :deep(.api-doc-table-wrapper th),
.api-doc-markdown :deep(.api-doc-table-wrapper td) { @apply border-x-0 border-t-0; }
.api-doc-markdown :deep(blockquote) { @apply my-3 border-l-4 border-primary-300 pl-4 text-gray-500 dark:border-primary-700; }
.api-doc-markdown :deep(hr) { @apply my-6 border-gray-200 dark:border-dark-600; }
</style>
