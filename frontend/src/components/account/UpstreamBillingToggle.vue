<template>
  <div class="flex items-center justify-between gap-4">
    <div class="min-w-0">
      <label class="input-label mb-0" :for="id">{{ label }}</label>
      <p v-if="hint" class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ hint }}</p>
    </div>
    <button
      :id="id"
      type="button"
      role="switch"
      :aria-label="label"
      :aria-checked="modelValue"
      :disabled="disabled"
      :data-testid="id"
      :class="['relative inline-flex h-6 w-11 flex-shrink-0 rounded-full border-2 border-transparent transition-colors focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2 disabled:opacity-50', modelValue ? 'bg-primary-600' : 'bg-gray-200 dark:bg-dark-600']"
      @click="$emit('update:modelValue', !modelValue)"
    >
      <span :class="['pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow transition', modelValue ? 'translate-x-5' : 'translate-x-0']" />
    </button>
  </div>
</template>

<script setup lang="ts">
// 账号表单与全局设置共用开关外观；联动规则由父级统一处理。
defineProps<{ id: string; modelValue: boolean; label: string; hint?: string; disabled?: boolean }>()
defineEmits<{ 'update:modelValue': [value: boolean] }>()
</script>
