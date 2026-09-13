<template>
  <button
    type="button"
    @click="toggle"
    class="toggle-control relative inline-flex flex-shrink-0 cursor-pointer rounded-full border-0 p-0 transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2 dark:focus:ring-offset-dark-800"
    :class="[
      props.modelValue ? 'toggle-active' : 'bg-gray-300 dark:bg-dark-600',
      props.size === 'sm' ? 'h-5 w-9' : 'h-6 w-11',
      props.disabled && 'cursor-not-allowed opacity-50'
    ]"
    role="switch"
    :aria-checked="props.modelValue"
    :aria-disabled="props.disabled"
    :disabled="props.disabled"
  >
    <!-- 普通规格采用 16px 滑块和 4px 留白；小规格保留现有的 2px 留白。 -->
    <span
      class="pointer-events-none absolute h-4 w-4 transform rounded-full bg-white shadow ring-0 transition-transform duration-200 ease-in-out"
      :class="[
        props.size === 'sm' ? 'left-0.5 top-0.5' : 'left-1 top-1',
        props.modelValue ? (props.size === 'sm' ? 'translate-x-4' : 'translate-x-5') : 'translate-x-0'
      ]"
    />
  </button>
</template>

<script setup lang="ts">
const props = withDefaults(defineProps<{
  modelValue: boolean
  size?: 'sm' | 'md'
  disabled?: boolean
}>(), {
  size: 'md',
  disabled: false
})

const emit = defineEmits<{
  (e: 'update:modelValue', value: boolean): void
}>()

// 开关保持受控，异步保存或确认期间由调用方决定何时更新值。
function toggle() {
  if (props.disabled) return
  emit('update:modelValue', !props.modelValue)
}
</script>
