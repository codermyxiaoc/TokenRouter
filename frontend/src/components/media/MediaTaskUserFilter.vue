<template>
  <div ref="container" class="relative min-w-0">
    <input
      :id="id" v-model="keyword" type="text" enterkeyhint="search" class="input w-full pr-8" maxlength="100"
      role="combobox" autocomplete="off" :aria-expanded="open" :aria-controls="`${id}-results`"
      :aria-activedescendant="open && activeIndex >= 0 ? `${id}-option-${activeIndex}` : undefined"
      :placeholder="t('mediaTasks.userSearchPlaceholder')" @input="search" @focus="open = true"
      @keydown.down.prevent="move(1)" @keydown.up.prevent="move(-1)"
      @keydown.enter="chooseActive" @keydown.esc.stop="open = false"
    />
    <button v-if="keyword || modelValue" type="button" class="absolute right-2 top-2 text-gray-400 hover:text-gray-600"
      :aria-label="t('common.reset')" @click="clear"><Icon name="x" size="sm" /></button>
    <div v-if="open && keyword.trim()" :id="`${id}-results`" role="listbox"
      class="absolute z-50 mt-1 max-h-64 w-full overflow-y-auto rounded-xl border border-gray-200 bg-white p-1 shadow-lg dark:border-dark-600 dark:bg-dark-800">
      <p v-if="loading" role="status" class="px-3 py-3 text-sm text-gray-500">{{ t('common.loading') }}</p>
      <p v-else-if="failed" role="alert" class="px-3 py-3 text-sm text-red-600">{{ t('mediaTasks.userSearchFailed') }}</p>
      <p v-else-if="!results.length" class="px-3 py-3 text-sm text-gray-500">{{ t('common.noOptionsFound') }}</p>
      <button v-for="(user, index) in results" :id="`${id}-option-${index}`" :key="user.id" type="button" role="option"
        :aria-selected="modelValue?.id === user.id" class="block w-full min-w-0 rounded-lg px-3 py-2 text-left text-sm hover:bg-gray-100 dark:hover:bg-dark-700"
        :class="activeIndex === index ? 'bg-gray-100 dark:bg-dark-700' : ''" @mousedown.prevent @click="choose(user)">
        <span class="flex min-w-0 items-center gap-1"><span class="truncate font-medium">{{ displayName(user) }}</span><span class="shrink-0 text-xs text-gray-400">#{{ user.id }}</span></span>
        <span class="block truncate text-xs text-gray-500">{{ user.email }}<span v-if="user.deleted"> · {{ t('admin.usage.userDeletedBadge') }}</span></span>
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import { adminUsageAPI, type SimpleUser } from '@/api/admin/usage'

const props = defineProps<{ id: string; modelValue: SimpleUser | null }>()
const emit = defineEmits<{ 'update:modelValue': [value: SimpleUser | null]; change: [] }>()
const { t } = useI18n()
const container = ref<HTMLElement | null>(null)
const keyword = ref('')
const results = ref<SimpleUser[]>([])
const open = ref(false)
const loading = ref(false)
const failed = ref(false)
const activeIndex = ref(-1)
let sequence = 0
let timer: ReturnType<typeof setTimeout> | undefined
let editing = false
const displayName = (user: SimpleUser) => user.username?.trim() || user.email

// 桌面与手机共用选中对象；自己编辑导致的清空不能抹去刚输入的搜索词。
watch(() => props.modelValue, user => {
  if (user) {
    invalidate()
    editing = false
    keyword.value = displayName(user)
    open.value = false
  } else if (!editing) {
    invalidate()
    keyword.value = ''
    results.value = []
  }
}, { immediate: true })

function invalidate() {
  ++sequence
  if (timer) clearTimeout(timer)
  timer = undefined
  loading.value = false
}
function search() {
  invalidate()
  editing = true
  open.value = true
  results.value = []
  activeIndex.value = -1
  failed.value = false
  // 输入新名字后立即取消旧用户过滤，避免界面与实际用户 ID 不一致。
  if (props.modelValue) { emit('update:modelValue', null); emit('change') }
  const query = keyword.value.trim()
  if (!query) return
  const request = sequence
  loading.value = true
  timer = setTimeout(async () => {
    timer = undefined
    try {
      const users = await adminUsageAPI.searchUsers(query)
      if (request === sequence) results.value = users.sort((a, b) => Number(a.deleted) - Number(b.deleted))
    } catch {
      if (request === sequence) failed.value = true
    } finally {
      if (request === sequence) loading.value = false
    }
  }, 300)
}
function choose(user: SimpleUser) {
  invalidate()
  editing = false
  keyword.value = displayName(user)
  open.value = false
  emit('update:modelValue', user)
  emit('change')
}
function clear() {
  invalidate()
  editing = false
  keyword.value = ''
  results.value = []
  open.value = false
  emit('update:modelValue', null)
  emit('change')
}
function move(delta: number) {
  open.value = true
  if (results.value.length) {
    activeIndex.value = activeIndex.value < 0
      ? (delta > 0 ? 0 : results.value.length - 1)
      : (activeIndex.value + delta + results.value.length) % results.value.length
  }
}
function chooseActive(event: KeyboardEvent) {
  if (open.value && activeIndex.value >= 0 && results.value[activeIndex.value]) {
    event.preventDefault()
    choose(results.value[activeIndex.value])
  }
}
function outside(event: MouseEvent) {
  if (!container.value?.contains(event.target as Node)) open.value = false
}
onMounted(() => document.addEventListener('click', outside))
onBeforeUnmount(() => { invalidate(); document.removeEventListener('click', outside) })
</script>
