<template>
  <!-- 已上传头像优先；否则用用户 ID（或兜底种子）生成 GitHub 风格 identicon -->
  <img
    v-if="normalizedAvatarUrl"
    :src="normalizedAvatarUrl"
    :alt="alt"
    :class="[sizeClass, 'rounded-full object-cover']"
  />
  <img
    v-else
    :src="defaultAvatarUrl"
    :alt="alt"
    :class="[sizeClass, 'rounded-full']"
  />
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { identiconDataUri } from '@/utils/identicon'

interface Props {
  /** 用户已上传/同步的头像地址，非空时优先展示 */
  avatarUrl?: string
  /** 用户 ID，作为 identicon 的首选种子（与 GitHub 一致按用户 ID 生成，全站各处图案一致） */
  userId?: number
  /** 用户 ID 不可用场景的兜底种子，需调用方保证稳定 */
  seed?: string
  alt?: string
  /** 尺寸相关 Tailwind class，如 'h-9 w-9'；各调用点尺寸不一，直接透传 */
  sizeClass?: string
}

const props = withDefaults(defineProps<Props>(), {
  avatarUrl: '',
  userId: undefined,
  seed: '',
  alt: '',
  sizeClass: 'h-9 w-9'
})

const normalizedAvatarUrl = computed(() => props.avatarUrl.trim())

// 用户 ID 优先，其次兜底种子；均为空时由 identiconDataUri 内部归一化兜底
const defaultAvatarUrl = computed(() =>
  identiconDataUri(props.userId != null ? `user-${props.userId}` : props.seed)
)
</script>
