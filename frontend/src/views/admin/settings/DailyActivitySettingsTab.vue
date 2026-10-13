<template>
  <section class="space-y-6">
    <div class="card overflow-hidden">
      <div
        class="flex flex-wrap items-start justify-between gap-3 border-b border-gray-100 p-6 dark:border-dark-700"
      >
        <div>
          <h2 class="text-lg font-semibold text-gray-900 dark:text-white">
            {{ t('dailyActivities.title') }}
          </h2>
          <p class="mt-2 text-sm text-gray-500">
            {{ t('dailyActivities.settings.description') }}
          </p>
        </div>
        <button
          type="button"
          class="btn btn-secondary btn-sm"
          :disabled="loading || saving"
          @click="load"
        >
          {{ t('common.refresh') }}
        </button>
      </div>
      <p v-if="loading" class="p-8 text-center text-gray-500">
        {{ t('common.loading') }}
      </p>
      <div v-else class="space-y-6 p-6">
        <p v-if="error" role="alert" class="text-sm text-red-600">
          {{ error }}
        </p>
        <template v-if="form">
          <div class="flex items-center justify-between gap-4">
            <div>
              <label for="daily-activity-enabled" class="input-label">{{
                t('dailyActivities.settings.enabled')
              }}</label>
              <p class="mt-2 text-sm text-gray-500">
                {{ t('dailyActivities.settings.enabledHint') }}
              </p>
            </div>
            <Toggle
              id="daily-activity-enabled"
              v-model="form.enabled"
              :disabled="saving"
              :aria-label="t('dailyActivities.settings.enabled')"
            />
          </div>
          <p
            class="rounded-xl bg-primary-50 p-4 text-sm text-primary-700 dark:bg-primary-900/20 dark:text-primary-300"
          >
            {{
              form.started_at
                ? t('dailyActivities.startedAt', {
                    time: formatTime(form.started_at),
                  })
                : t('dailyActivities.settings.startHint')
            }}
          </p>
        </template>
      </div>
    </div>
    <template v-if="form && !loading">
      <div class="space-y-2 text-sm leading-relaxed text-gray-500">
        <p>{{ t('dailyActivities.eligibilityHint') }}</p>
        <p>{{ t('dailyActivities.settings.changeHint') }}</p>
      </div>
      <div v-for="type in dailyActivityTypes" :key="type" class="card p-6">
        <h3 class="text-base font-semibold text-gray-900 dark:text-white">
          {{ t(`dailyActivities.types.${type}`) }}
        </h3>
        <p class="mt-2 text-sm text-gray-500">
          {{
            t(
              type === 'daily_gift'
                ? 'dailyActivities.dailyReset'
                : 'dailyActivities.persistent',
            )
          }}
        </p>
        <div class="mt-5 grid gap-5 sm:grid-cols-2">
          <div>
            <label class="input-label" :for="`${type}-threshold`">{{
              t(
                type === 'daily_gift'
                  ? 'dailyActivities.settings.dailyThreshold'
                  : 'dailyActivities.settings.threshold',
                { unit: balanceUnitName },
              )
            }}</label
            ><input
              :id="`${type}-threshold`"
              v-model.number="form[type].threshold"
              type="number"
              inputmode="decimal"
              min="0.01"
              max="1000000"
              step="0.01"
              class="input w-full"
              :disabled="saving"
            />
          </div>
          <div>
            <label class="input-label" :for="`${type}-reward-max`">{{
              t('dailyActivities.settings.rewardMax', { unit: balanceUnitName })
            }}</label
            ><input
              :id="`${type}-reward-max`"
              v-model.number="form[type].reward_max"
              type="number"
              inputmode="decimal"
              min="0.1"
              max="1000000"
              step="0.01"
              class="input w-full"
              :disabled="saving"
            />
            <p class="mt-2 text-xs text-gray-500">
              {{ t('dailyActivities.settings.rewardHint') }}
            </p>
          </div>
        </div>
      </div>
      <p v-if="!valid" role="alert" class="text-sm text-red-600">
        {{ t('dailyActivities.settings.validation') }}
      </p>
      <div class="flex justify-end">
        <button
          type="button"
          class="btn btn-primary"
          :disabled="saving || !valid"
          data-testid="save-daily-activities"
          @click="save"
        >
          {{
            t(saving ? 'common.processing' : 'dailyActivities.settings.save')
          }}
        </button>
      </div>
    </template>
  </section>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import {
  dailyActivitiesAPI,
  dailyActivityTypes,
  type DailyActivityConfig,
} from '@/api/dailyActivities'
import { useAppStore } from '@/stores/app'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import { extractApiErrorMessage } from '@/utils/apiError'

const { t, locale } = useI18n()
const app = useAppStore()
const { balanceUnitName } = useBalanceDisplay()
const form = ref<DailyActivityConfig>()
const loading = ref(false)
const saving = ref(false)
const error = ref('')
const formatTime = (value: string) =>
  new Date(value).toLocaleString(locale.value, {
    timeZone: 'Asia/Shanghai',
    hour12: false,
  })
const validAmount = (value: number, min: number) =>
  typeof value === 'number' &&
  Number.isFinite(value) &&
  value >= min &&
  value <= 1000000 &&
  Math.abs(value * 100 - Math.round(value * 100)) < 1e-7
const valid = computed(
  () =>
    !!form.value &&
    dailyActivityTypes.every(
      (type) =>
        validAmount(form.value![type].threshold, 0.01) &&
        validAmount(form.value![type].reward_max, 0.1),
    ),
)
async function load() {
  loading.value = true
  error.value = ''
  try {
    form.value = await dailyActivitiesAPI.config()
  } catch (err) {
    form.value = undefined
    error.value = extractApiErrorMessage(err, t('dailyActivities.loadFailed'))
  } finally {
    loading.value = false
  }
}
async function save() {
  if (!form.value || !valid.value || saving.value) return
  saving.value = true
  error.value = ''
  try {
    // 首次开启时间由服务端固定，不随开关或前端提交改变。
    const { enabled, daily_gift, recharge, consumption } = form.value
    form.value = await dailyActivitiesAPI.saveConfig({
      enabled,
      daily_gift,
      recharge,
      consumption,
    })
    app.showSuccess(t('dailyActivities.settings.saved'))
    // 独立配置保存后同步公共开关，避免旧 GET 覆盖已确认的值。
    await app.syncDailyActivityEnabled(form.value.enabled)
  } catch (err) {
    error.value = extractApiErrorMessage(
      err,
      t('dailyActivities.settings.saveFailed'),
    )
  } finally {
    saving.value = false
  }
}
onMounted(load)
</script>
