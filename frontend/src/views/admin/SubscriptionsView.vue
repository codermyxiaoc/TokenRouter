<template>
  <AppLayout>
    <TablePageLayout>
      <template #filters>
        <!-- Top Toolbar: Left (search + filters) / Right (actions) -->
        <div class="flex flex-col gap-3 sm:flex-row sm:items-start sm:justify-between">
          <!-- Left: Fuzzy user search + filters (wrap to multiple lines) -->
          <div class="flex min-w-0 flex-1 flex-nowrap items-center gap-3">
            <!-- User Search -->
            <div
              class="relative min-w-0 flex-1 sm:flex-none sm:w-64"
              data-filter-user-search
            >
              <Icon
                name="search"
                size="md"
                class="absolute left-3 top-1/2 -translate-y-1/2 text-gray-400"
              />
              <input
                v-model="filterUserKeyword"
                type="text"
                :placeholder="t('admin.users.searchUsers')"
                class="input pl-10 pr-8"
                @input="debounceSearchFilterUsers"
                @focus="showFilterUserDropdown = true"
              />
              <button
                v-if="selectedFilterUser"
                @click="clearFilterUser"
                type="button"
                class="absolute right-2 top-1/2 -translate-y-1/2 text-gray-400 hover:text-gray-600 dark:hover:text-gray-300"
                :title="t('common.clear')"
              >
                <Icon name="x" size="sm" :stroke-width="2" />
              </button>

              <!-- User Dropdown -->
              <div
                v-if="showFilterUserDropdown && (filterUserResults.length > 0 || filterUserKeyword)"
                class="absolute z-50 mt-1 max-h-60 w-full overflow-auto rounded-lg border border-gray-200 bg-white shadow-lg dark:border-gray-700 dark:bg-gray-800"
              >
                <div
                  v-if="filterUserLoading"
                  class="px-4 py-3 text-sm text-gray-500 dark:text-gray-400"
                >
                  {{ t('common.loading') }}
                </div>
                <div
                  v-else-if="filterUserResults.length === 0 && filterUserKeyword"
                  class="px-4 py-3 text-sm text-gray-500 dark:text-gray-400"
                >
                  {{ t('common.noOptionsFound') }}
                </div>
                <button
                  v-for="user in filterUserResults"
                  :key="user.id"
                  type="button"
                  @click="selectFilterUser(user)"
                  class="w-full px-4 py-2 text-left text-sm hover:bg-gray-100 dark:hover:bg-gray-700"
                >
                  <span class="font-medium text-gray-900 dark:text-white">{{ user.email }}</span>
                  <span class="ml-2 text-gray-500 dark:text-gray-400">#{{ user.id }}</span>
                </button>
              </div>
            </div>

            <!-- Filters -->
            <div ref="filterDropdownRef" class="relative shrink-0">
              <button
                ref="filterDropdownButtonRef"
                type="button"
                class="btn btn-secondary relative h-9 w-9 p-0"
                :aria-expanded="showFilterDropdown"
                :aria-label="t('common.filter')"
                :title="t('common.filter')"
                @click="toggleFilterDropdown"
              >
                <Icon name="filter" size="sm" />
                <span
                  v-if="activeFilterCount > 0"
                  class="pointer-events-none absolute -right-1 -top-1 inline-flex h-5 min-w-5 items-center justify-center rounded-full bg-primary-100 px-1.5 text-xs font-semibold text-primary-700 dark:bg-primary-900/40 dark:text-primary-300"
                >
                  {{ activeFilterCount }}
                </span>
              </button>

              <Teleport to="body">
                <div
                  v-if="showFilterDropdown"
                  class="fixed z-[60] max-w-[calc(100vw-2rem)] overflow-y-auto rounded-xl border border-gray-200 bg-white shadow-xl dark:border-dark-600 dark:bg-dark-900"
                  :style="filterDropdownStyle"
                  @click.stop
                >
                <div class="flex items-center justify-between border-b border-gray-100 px-4 py-3 dark:border-dark-700">
                  <div class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('common.filter') }}</div>
                  <button
                    v-if="activeFilterCount > 0"
                    type="button"
                    class="text-xs font-medium text-primary-600 hover:text-primary-700 dark:text-primary-400"
                    @click="resetSubscriptionFilters"
                  >
                    {{ t('common.reset') }}
                  </button>
                </div>
                <div class="grid grid-cols-1 gap-3 p-4 sm:grid-cols-2">
                  <div>
                    <label class="input-label">{{ t('admin.subscriptions.columns.status') }}</label>
                    <Select v-model="filters.status" :options="statusOptions" :placeholder="t('admin.subscriptions.allStatus')" @change="applyFilters" />
                  </div>
                  <div>
                    <label class="input-label">{{ t('admin.subscriptions.form.group') }}</label>
                    <Select v-model="filters.plan_id" :options="planOptions" :placeholder="t('admin.announcements.form.selectPackages')" @change="applyFilters" />
                  </div>
                  <div class="sm:col-span-2">
                    <label class="input-label">{{ t('admin.accounts.columns.platform') }}</label>
                    <Select v-model="filters.platform" :options="platformFilterOptions" :placeholder="t('admin.subscriptions.allPlatforms')" @change="applyFilters" />
                  </div>
                </div>
                </div>
              </Teleport>
            </div>
          </div>

          <!-- Right: Actions -->
          <div class="flex shrink-0 flex-wrap items-center justify-end gap-3">
            <button
              @click="loadSubscriptions"
              :disabled="loading"
              class="btn btn-secondary h-9 w-9 shrink-0 p-0"
              :title="t('common.refresh')"
            >
              <Icon name="refresh" size="md" :class="loading ? 'animate-spin' : ''" />
            </button>
            <!-- Column Settings Dropdown -->
            <div class="relative" ref="columnDropdownRef">
              <button
                @click="showColumnDropdown = !showColumnDropdown"
                class="btn btn-secondary h-9 w-9 shrink-0 p-0"
                :title="t('admin.users.columnSettings')"
              >
                <svg class="h-4 w-4" fill="none" stroke="currentColor" viewBox="0 0 24 24" stroke-width="1.5">
                  <path stroke-linecap="round" stroke-linejoin="round" d="M9 4.5v15m6-15v15m-10.875 0h15.75c.621 0 1.125-.504 1.125-1.125V5.625c0-.621-.504-1.125-1.125-1.125H4.125C3.504 4.5 3 5.004 3 5.625v12.75c0 .621.504 1.125 1.125 1.125z" />
                </svg>
                <span class="hidden">{{ t('admin.users.columnSettings') }}</span>
              </button>
              <!-- Dropdown menu -->
              <div
                v-if="showColumnDropdown"
                class="absolute right-0 z-50 mt-2 w-48 origin-top-right rounded-lg border border-gray-200 bg-white shadow-lg dark:border-gray-700 dark:bg-gray-800"
              >
                <div class="p-2">
                  <!-- User column mode selection -->
                  <div class="mb-2 border-b border-gray-200 pb-2 dark:border-gray-700">
                    <div class="px-3 py-1 text-xs font-medium text-gray-500 dark:text-gray-400">
                      {{ t('admin.subscriptions.columns.user') }}
                    </div>
                    <button
                      @click="setUserColumnMode('email')"
                      class="flex w-full items-center justify-between rounded-md px-3 py-2 text-sm text-gray-700 hover:bg-gray-100 dark:text-gray-200 dark:hover:bg-gray-700"
                    >
                      <span>{{ t('admin.users.columns.email') }}</span>
                      <Icon v-if="userColumnMode === 'email'" name="check" size="sm" class="text-primary-500" />
                    </button>
                    <button
                      @click="setUserColumnMode('username')"
                      class="flex w-full items-center justify-between rounded-md px-3 py-2 text-sm text-gray-700 hover:bg-gray-100 dark:text-gray-200 dark:hover:bg-gray-700"
                    >
                      <span>{{ t('admin.users.columns.username') }}</span>
                      <Icon v-if="userColumnMode === 'username'" name="check" size="sm" class="text-primary-500" />
                    </button>
                  </div>
                  <!-- Other columns toggle -->
                  <button
                    v-for="col in toggleableColumns"
                    :key="col.key"
                    @click="toggleColumn(col.key)"
                    class="flex w-full items-center justify-between rounded-md px-3 py-2 text-sm text-gray-700 hover:bg-gray-100 dark:text-gray-200 dark:hover:bg-gray-700"
                  >
                    <span>{{ col.label }}</span>
                    <Icon v-if="isColumnVisible(col.key)" name="check" size="sm" class="text-primary-500" />
                  </button>
                </div>
              </div>
            </div>
            <button
              @click="showGuideModal = true"
              class="btn btn-secondary h-9 w-9 shrink-0 p-0"
              :title="t('admin.subscriptions.guide.showGuide')"
            >
              <Icon name="questionCircle" size="md" />
            </button>
            <button @click="showAssignModal = true" class="btn btn-primary h-9 whitespace-nowrap">
              <Icon name="plus" size="md" class="mr-2" />
              {{ t('admin.subscriptions.assignSubscription') }}
            </button>
          </div>
        </div>
        <div v-if="bulkRequest && bulkAction === null" class="mt-3 rounded-xl border border-amber-200 bg-amber-50 p-3 dark:border-amber-800 dark:bg-amber-900/20">
          <button type="button" class="btn btn-secondary btn-sm" data-test="resume-bulk-operation" @click="bulkAction = bulkRequest.action">
            {{ t('admin.subscriptions.bulkResume') }}
          </button>
          <p class="mt-2 text-xs text-amber-800 dark:text-amber-200">{{ t('admin.subscriptions.bulkRetryHint') }}</p>
        </div>
        <!-- 批量栏沿用上游布局，操作范围与当前套餐规则保持一致。 -->
        <div
          v-if="selectedSubscriptionIDs.length"
          class="mt-3 space-y-2 rounded-xl border border-primary-200 bg-primary-50 p-3 dark:border-primary-800 dark:bg-primary-900/20"
          data-test="subscription-bulk-actions"
        >
          <div class="flex flex-wrap items-center gap-2">
            <span class="mr-2 text-sm font-medium text-primary-800 dark:text-primary-200">
              {{ t('admin.subscriptions.bulkSelected', { count: selectedSubscriptionIDs.length }) }}
            </span>
            <button
              v-for="action in bulkActions"
              :key="action"
              type="button"
              :class="action === 'revoke' ? 'btn btn-danger btn-sm' : 'btn btn-secondary btn-sm'"
              :data-test="`bulk-${action}`"
              :disabled="loading || bulkSubmitting || bulkTargets[action].length === 0"
              @click="openBulkDialog(action)"
            >
              {{ t(bulkActionLabels[action]) }} ({{ bulkTargets[action].length }})
            </button>
            <button type="button" class="btn btn-secondary btn-sm" :disabled="bulkSubmitting" @click="selectedSubscriptionIDs = []">
              {{ t('common.clearSelection') }}
            </button>
          </div>
          <p class="text-xs text-gray-600 dark:text-gray-400">{{ t('admin.subscriptions.bulkSelectionHint') }}</p>
        </div>
      </template>

      <!-- Subscriptions Table -->
      <template #table>
        <DataTable
          :columns="columns"
          :data="subscriptions"
          :loading="loading"
          :selectable="!bulkSubmitting"
          :selected-keys="selectedSubscriptionIDs"
          :selection-label="t('admin.subscriptions.title')"
          row-key="id"
          :server-side-sort="true"
          default-sort-key="created_at"
          default-sort-order="desc"
          @sort="handleSort"
          @update:selected-keys="updateSelectedSubscriptions"
        >
          <template #cell-user="{ row }">
            <div class="flex items-center gap-2">
              <UserAvatar
                :avatar-url="row.user?.avatar_url || ''"
                :user-id="row.user_id"
                :alt="row.user?.email || ''"
                size-class="h-8 w-8"
              />
              <span class="font-medium text-gray-900 dark:text-white">
                {{ userColumnMode === 'email'
                  ? (row.user?.email || t('admin.redeem.userPrefix', { id: row.user_id }))
                  : (row.user?.username || '-')
                }}
              </span>
            </div>
          </template>

          <template #cell-group="{ row }">
            <span v-if="row.plan" class="text-sm font-medium text-gray-900 dark:text-white">
              {{ row.plan.name }}
            </span>
            <span v-else-if="row.plan_id" class="text-sm font-medium text-gray-900 dark:text-white">
              {{ `Plan #${row.plan_id}` }}
            </span>
            <span v-else class="text-sm text-gray-400 dark:text-dark-500">-</span>
          </template>

          <template #cell-usage="{ row }">
            <div class="min-w-[280px] space-y-2">
              <!-- Daily Usage -->
              <div v-if="row.daily_limit_usd" class="usage-row">
                <div class="flex items-center gap-2">
                  <span class="usage-label">{{ t('admin.subscriptions.daily') }}</span>
                  <div class="h-1.5 flex-1 rounded-full bg-gray-200 dark:bg-dark-600">
                    <div
                      class="h-1.5 rounded-full transition-all"
                      :class="getProgressClass(row.daily_usage_usd, row.daily_limit_usd)"
                      :style="{
                        width: getProgressWidth(row.daily_usage_usd, row.daily_limit_usd)
                      }"
                    ></div>
                  </div>
                  <span class="usage-amount">
                    {{ formatSubscriptionBalance(row.daily_usage_usd) }}
                    <span class="text-gray-400">/</span>
                    {{ formatSubscriptionBalance(row.daily_limit_usd) }}
                  </span>
                </div>
                <!-- 计数由服务端按符合规则的到点窗口返回，不在前端按用量或天数推算。 -->
                <div class="flex flex-wrap items-center gap-x-2 gap-y-1 pl-12">
                  <span
                    data-testid="admin-quota-reset-count-daily"
                    class="reset-count"
                    :title="t('userSubscriptions.resetCountHint')"
                    tabindex="0"
                  >{{ t('userSubscriptions.resetCount', { count: row.daily_reset_count ?? 0 }) }}</span>
                  <div class="reset-info" v-if="row.daily_window_start">
                    <svg
                      class="h-3 w-3"
                      fill="none"
                      viewBox="0 0 24 24"
                      stroke="currentColor"
                      stroke-width="2"
                    >
                      <path
                        stroke-linecap="round"
                        stroke-linejoin="round"
                        d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z"
                      />
                    </svg>
                    <span>{{ formatDailyUsageWindow(row) }}</span>
                  </div>
                </div>
              </div>

              <!-- Weekly Usage -->
              <div v-if="row.weekly_limit_usd" class="usage-row">
                <div class="flex items-center gap-2">
                  <span class="usage-label">{{ t('admin.subscriptions.weekly') }}</span>
                  <div class="h-1.5 flex-1 rounded-full bg-gray-200 dark:bg-dark-600">
                    <div
                      class="h-1.5 rounded-full transition-all"
                      :class="getProgressClass(row.weekly_usage_usd, row.weekly_limit_usd)"
                      :style="{
                        width: getProgressWidth(row.weekly_usage_usd, row.weekly_limit_usd)
                      }"
                    ></div>
                  </div>
                  <span class="usage-amount">
                    {{ formatSubscriptionBalance(row.weekly_usage_usd) }}
                    <span class="text-gray-400">/</span>
                    {{ formatSubscriptionBalance(row.weekly_limit_usd) }}
                  </span>
                </div>
                <div class="flex flex-wrap items-center gap-x-2 gap-y-1 pl-12">
                  <span
                    data-testid="admin-quota-reset-count-weekly"
                    class="reset-count"
                    :title="t('userSubscriptions.resetCountHint')"
                    tabindex="0"
                  >{{ t('userSubscriptions.resetCount', { count: row.weekly_reset_count ?? 0 }) }}</span>
                  <div class="reset-info" v-if="row.weekly_window_start">
                    <svg
                      class="h-3 w-3"
                      fill="none"
                      viewBox="0 0 24 24"
                      stroke="currentColor"
                      stroke-width="2"
                    >
                      <path
                        stroke-linecap="round"
                        stroke-linejoin="round"
                        d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z"
                      />
                    </svg>
                    <span>{{ formatUsageWindow(row, row.weekly_window_start, 'weekly') }}</span>
                  </div>
                </div>
              </div>

              <!-- Monthly Usage -->
              <div v-if="row.monthly_limit_usd" class="usage-row">
                <div class="flex items-center gap-2">
                  <span class="usage-label">{{ t('admin.subscriptions.monthly') }}</span>
                  <div class="h-1.5 flex-1 rounded-full bg-gray-200 dark:bg-dark-600">
                    <div
                      class="h-1.5 rounded-full transition-all"
                      :class="getProgressClass(row.monthly_usage_usd, row.monthly_limit_usd)"
                      :style="{
                        width: getProgressWidth(row.monthly_usage_usd, row.monthly_limit_usd)
                      }"
                    ></div>
                  </div>
                  <span class="usage-amount">
                    {{ formatSubscriptionBalance(row.monthly_usage_usd) }}
                    <span class="text-gray-400">/</span>
                    {{ formatSubscriptionBalance(row.monthly_limit_usd) }}
                  </span>
                </div>
                <div class="flex flex-wrap items-center gap-x-2 gap-y-1 pl-12">
                  <span
                    data-testid="admin-quota-reset-count-monthly"
                    class="reset-count"
                    :title="t('userSubscriptions.resetCountHint')"
                    tabindex="0"
                  >{{ t('userSubscriptions.resetCount', { count: row.monthly_reset_count ?? 0 }) }}</span>
                  <div class="reset-info" v-if="row.monthly_window_start">
                    <svg
                      class="h-3 w-3"
                      fill="none"
                      viewBox="0 0 24 24"
                      stroke="currentColor"
                      stroke-width="2"
                    >
                      <path
                        stroke-linecap="round"
                        stroke-linejoin="round"
                        d="M12 8v4l3 3m6-3a9 9 0 11-18 0 9 9 0 0118 0z"
                      />
                    </svg>
                    <span>{{ formatUsageWindow(row, row.monthly_window_start, 'monthly') }}</span>
                  </div>
                </div>
              </div>

              <!-- No Limits - Unlimited badge -->
              <div
                v-if="
                  !row.daily_limit_usd &&
                  !row.weekly_limit_usd &&
                  !row.monthly_limit_usd
                "
                class="flex items-center gap-2 rounded-lg bg-gradient-to-r from-emerald-50 to-teal-50 px-3 py-2 dark:from-emerald-900/20 dark:to-teal-900/20"
              >
                <span class="text-lg text-emerald-600 dark:text-emerald-400">∞</span>
                <span class="text-xs font-medium text-emerald-700 dark:text-emerald-300">
                  {{ t('admin.subscriptions.unlimited') }}
                </span>
              </div>
            </div>
          </template>

          <template #cell-expires_at="{ value }">
            <div v-if="value">
              <span
                class="text-sm"
                :class="
                  isExpiringSoon(value)
                    ? 'text-orange-600 dark:text-orange-400'
                    : 'text-gray-700 dark:text-gray-300'
                "
              >
                {{ formatDateTimeToMinute(value) }}
              </span>
              <template
                v-for="remainingExpiry in [formatRemainingExpiry(value)]"
                :key="remainingExpiry ?? 'expired'"
              >
                <div v-if="remainingExpiry" class="text-xs text-gray-500">
                  {{ remainingExpiry }}
                </div>
              </template>
            </div>
            <span v-else class="text-sm text-gray-500">{{
              t('admin.subscriptions.noExpiration')
            }}</span>
          </template>

          <template #cell-status="{ value }">
            <span
              :class="[
                'badge',
                value === 'active'
                  ? 'badge-success'
                  : value === 'pending'
                    ? 'badge-primary'
                  : value === 'expired'
                    ? 'badge-warning'
                    : value === 'suspended'
                      ? 'badge-danger'
                      : 'badge-gray'
              ]"
            >
              {{ t(`admin.subscriptions.status.${value}`) }}
            </span>
          </template>

          <template #cell-actions="{ row }">
            <div class="flex items-center gap-1">
              <button
                v-if="row.status === 'active' || row.status === 'expired'"
                @click="handleExtend(row)"
                class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-blue-50 hover:text-blue-600 dark:hover:bg-blue-900/20 dark:hover:text-blue-400"
              >
                <Icon name="calendar" size="sm" />
                <span class="text-xs">{{ t('admin.subscriptions.adjust') }}</span>
              </button>
              <button
                v-if="row.status === 'active'"
                @click="handleResetQuota(row)"
                :disabled="resettingQuota && resettingSubscription?.id === row.id"
                class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-orange-50 hover:text-orange-600 dark:hover:bg-orange-900/20 dark:hover:text-orange-400 disabled:cursor-not-allowed disabled:opacity-50"
              >
                <Icon name="refresh" size="sm" />
                <span class="text-xs">{{ t('admin.subscriptions.resetQuota') }}</span>
              </button>
              <button
                v-if="row.status === 'active' || row.status === 'pending'"
                @click="handleRevoke(row)"
                class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-red-50 hover:text-red-600 dark:hover:bg-red-900/20 dark:hover:text-red-400"
              >
                <Icon name="ban" size="sm" />
                <span class="text-xs">{{ revokeActionText(row) }}</span>
              </button>
              <button
                v-if="row.status === 'revoked'"
                @click="handleRestore(row)"
                class="flex flex-col items-center gap-0.5 rounded-lg p-1.5 text-gray-500 transition-colors hover:bg-green-50 hover:text-green-600 dark:hover:bg-green-900/20 dark:hover:text-green-400"
              >
                <Icon name="refresh" size="sm" />
                <span class="text-xs">{{ t('admin.subscriptions.restore') }}</span>
              </button>
            </div>
          </template>

          <template #empty>
            <EmptyState
              :title="t('admin.subscriptions.noSubscriptionsYet')"
              :description="t('admin.subscriptions.assignFirstSubscription')"
              :action-text="t('admin.subscriptions.assignSubscription')"
              @action="showAssignModal = true"
            />
          </template>
        </DataTable>
      </template>

      <!-- Pagination -->
      <template #pagination>
      <Pagination
        v-if="pagination.total > 0"
        :page="pagination.page"
        :total="pagination.total"
        :page-size="pagination.page_size"
        @update:page="handlePageChange"
        @update:pageSize="handlePageSizeChange"
      />
      </template>
    </TablePageLayout>

    <!-- 保持组件挂载，关闭后再次打开仍能确认上次未完成的分配请求。 -->
    <AssignSubscriptionDialog
      :show="showAssignModal"
      :plans="plans"
      @close="showAssignModal = false"
      @assigned="loadSubscriptions"
    />

    <!-- Adjust Subscription Modal -->
    <BaseDialog
      :show="showExtendModal"
      :title="t('admin.subscriptions.adjustSubscription')"
      width="narrow"
      @close="closeExtendModal"
    >
      <form
        v-if="extendingSubscription"
        id="extend-subscription-form"
        @submit.prevent="handleExtendSubscription"
        class="space-y-5"
      >
        <div class="rounded-lg bg-gray-50 p-4 dark:bg-dark-700">
          <p class="text-sm text-gray-600 dark:text-gray-400">
            {{ t('admin.subscriptions.adjustingFor') }}
            <span class="font-medium text-gray-900 dark:text-white">{{
              extendingSubscription.user?.email
            }}</span>
          </p>
          <p class="mt-1 text-sm text-gray-600 dark:text-gray-400">
            {{ t('admin.subscriptions.currentExpiration') }}:
            <span class="font-medium text-gray-900 dark:text-white">
              {{
                extendingSubscription.expires_at
                  ? formatDateTimeToMinute(extendingSubscription.expires_at)
                  : t('admin.subscriptions.noExpiration')
              }}
            </span>
          </p>
          <p v-if="extendingSubscription.expires_at" class="mt-1 text-sm text-gray-600 dark:text-gray-400">
            {{ t('admin.subscriptions.remainingDays') }}:
            <span class="font-medium text-gray-900 dark:text-white">
              {{ getDaysRemaining(extendingSubscription.expires_at) ?? 0 }}
            </span>
          </p>
        </div>
        <div>
          <label class="input-label">{{ t('admin.subscriptions.form.targetValidityDays') }}</label>
          <div class="flex items-center gap-2">
            <input
              v-model.number="extendForm.days"
              type="number"
              min="1"
              required
              class="input text-center"
              :placeholder="t('admin.subscriptions.adjustDaysPlaceholder')"
            />
          </div>
          <p class="input-hint">{{ t('admin.subscriptions.adjustHint') }}</p>
        </div>
      </form>
      <template #footer>
        <div v-if="extendingSubscription" class="flex justify-end gap-3">
          <button @click="closeExtendModal" type="button" class="btn btn-secondary">
            {{ t('common.cancel') }}
          </button>
          <button
            type="submit"
            form="extend-subscription-form"
            :disabled="submitting"
            class="btn btn-primary"
          >
            {{ submitting ? t('admin.subscriptions.adjusting') : t('admin.subscriptions.adjust') }}
          </button>
        </div>
      </template>
    </BaseDialog>

    <!-- Revoke Confirmation Dialog -->
    <ConfirmDialog
      :show="showRevokeDialog"
      :title="revokeDialogTitle"
      :message="revokeDialogMessage"
      :confirm-text="revokeDialogConfirmText"
      :cancel-text="t('common.cancel')"
      :danger="true"
      @confirm="confirmRevoke"
      @cancel="showRevokeDialog = false"
    />

    <!-- 恢复确认弹窗 -->
    <ConfirmDialog
      :show="showRestoreDialog"
      :title="t('admin.subscriptions.restoreSubscription')"
      :message="t('admin.subscriptions.restoreConfirm', { user: restoringSubscription?.user?.email })"
      :confirm-text="t('admin.subscriptions.restore')"
      :cancel-text="t('common.cancel')"
      @confirm="confirmRestore"
      @cancel="showRestoreDialog = false"
    />

    <!-- Reset Quota Confirmation Dialog -->
    <ConfirmDialog
      :show="showResetQuotaConfirm"
      :title="t('admin.subscriptions.resetQuotaTitle')"
      :message="t('admin.subscriptions.resetQuotaConfirm', { user: resettingSubscription?.user?.email })"
      :confirm-text="t('admin.subscriptions.resetQuota')"
      :cancel-text="t('common.cancel')"
      @confirm="confirmResetQuota"
      @cancel="showResetQuotaConfirm = false"
    />
    <!-- 确认清单保留本次请求快照，网络失败后沿用同一幂等键重试。 -->
    <BaseDialog :show="bulkAction !== null" :title="bulkAction ? t(bulkActionLabels[bulkAction]) : ''" width="normal" :close-on-escape="!bulkSubmitting" @close="closeBulkDialog">
      <form id="bulk-subscription-form" class="space-y-4" @submit.prevent="submitBulkAction">
        <p class="text-sm text-gray-600 dark:text-gray-300">{{ t('admin.subscriptions.bulkConfirmTargets', { count: bulkSubscriptionIDs.length }) }}</p>
        <ul class="max-h-48 divide-y divide-gray-100 overflow-y-auto rounded-lg border border-gray-200 dark:divide-dark-700 dark:border-dark-600" data-test="bulk-targets">
          <li v-for="subscription in bulkTargetSnapshot" :key="subscription.id" class="flex flex-wrap items-center justify-between gap-x-3 gap-y-1 px-3 py-2 text-sm">
            <span class="min-w-0 break-all font-medium text-gray-900 dark:text-gray-100">{{ subscription.user?.email || `#${subscription.user_id}` }}</span>
            <span class="text-xs text-gray-500 dark:text-gray-400">{{ subscription.plan?.name || '-' }} · #{{ subscription.id }}</span>
          </li>
        </ul>
        <div v-if="bulkAction === 'extend'">
          <label for="bulk-subscription-days" class="input-label">{{ t('admin.subscriptions.bulkExtendDays') }}</label>
          <input id="bulk-subscription-days" v-model.number="bulkDays" type="number" min="1" max="36500" step="1" required class="input" :disabled="bulkSubmitting || bulkRequest !== null" />
          <p class="input-hint">{{ t('admin.subscriptions.bulkExtendHint') }}</p>
        </div>
        <fieldset v-else-if="bulkAction === 'reset'" class="space-y-2" :disabled="bulkSubmitting || bulkRequest !== null">
          <legend class="input-label">{{ t('admin.subscriptions.bulkResetWindows') }}</legend>
          <div class="flex flex-wrap gap-5">
            <label v-for="period in (['daily', 'weekly', 'monthly'] as const)" :key="period" class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
              <input v-model="bulkResetWindows[period]" type="checkbox" class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500 dark:border-dark-600" :data-bulk-window="period" />
              {{ t(`admin.subscriptions.${period}`) }}
            </label>
          </div>
          <p class="input-hint">{{ t('admin.subscriptions.bulkResetHint') }}</p>
        </fieldset>
        <p v-else class="rounded-lg bg-amber-50 p-3 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-200">
          {{ t(bulkAction === 'revoke' ? 'admin.subscriptions.bulkRevokeHint' : 'admin.subscriptions.bulkRestoreHint') }}
        </p>
        <div v-if="bulkError" class="rounded-lg bg-red-50 p-3 dark:bg-red-900/20">
          <p role="alert" class="text-sm text-red-600 dark:text-red-400">{{ bulkError }}</p>
          <p v-if="bulkRequest" class="mt-1 text-xs text-gray-600 dark:text-gray-400">{{ t('admin.subscriptions.bulkRetryHint') }}</p>
        </div>
      </form>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" class="btn btn-secondary" :disabled="bulkSubmitting" @click="closeBulkDialog">{{ t('common.cancel') }}</button>
          <button type="submit" form="bulk-subscription-form" :class="bulkAction === 'revoke' ? 'btn btn-danger' : 'btn btn-primary'" :disabled="bulkSubmitting">
            {{ bulkSubmitting ? t('common.processing') : t('common.confirm') }}
          </button>
        </div>
      </template>
    </BaseDialog>
    <!-- Subscription Guide Modal -->
    <teleport to="body">
      <transition name="modal">
        <div v-if="showGuideModal" class="fixed inset-0 z-50 flex items-center justify-center p-4" @mousedown.self="showGuideModal = false">
          <div class="fixed inset-0 bg-black/50" @click="showGuideModal = false"></div>
          <div class="relative max-h-[85vh] w-full max-w-2xl overflow-y-auto rounded-surface bg-white p-6 shadow-2xl dark:bg-dark-800 sm:rounded-dialog">
            <button type="button" class="absolute right-4 top-4 text-gray-400 hover:text-gray-600 dark:hover:text-gray-200" @click="showGuideModal = false">
              <svg class="h-5 w-5" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2"><path stroke-linecap="round" stroke-linejoin="round" d="M6 18L18 6M6 6l12 12" /></svg>
            </button>

            <h2 class="mb-4 text-lg font-bold text-gray-900 dark:text-white">{{ t('admin.subscriptions.guide.title') }}</h2>
            <p class="mb-5 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.subscriptions.guide.subtitle') }}</p>

            <!-- Step 1 -->
            <div class="mb-5">
              <h3 class="mb-2 flex items-center gap-2 text-sm font-semibold text-gray-900 dark:text-white">
                <span class="flex h-6 w-6 items-center justify-center rounded-full bg-primary-100 text-xs font-bold text-primary-700 dark:bg-primary-900/40 dark:text-primary-300">1</span>
                {{ t('admin.subscriptions.guide.step1.title') }}
              </h3>
              <ol class="ml-8 list-decimal space-y-1 text-sm text-gray-600 dark:text-gray-300">
                <li>{{ t('admin.subscriptions.guide.step1.line1') }}</li>
                <li>{{ t('admin.subscriptions.guide.step1.line2') }}</li>
                <li>{{ t('admin.subscriptions.guide.step1.line3') }}</li>
              </ol>
              <div class="ml-8 mt-2">
                <router-link
                  to="/admin/groups"
                  @click="showGuideModal = false"
                  class="inline-flex items-center gap-1 text-sm font-medium text-primary-600 hover:text-primary-700 dark:text-primary-400 dark:hover:text-primary-300"
                >
                  {{ t('admin.subscriptions.guide.step1.link') }}
                  <Icon name="arrowRight" size="xs" />
                </router-link>
              </div>
            </div>

            <!-- Step 2 -->
            <div class="mb-5">
              <h3 class="mb-2 flex items-center gap-2 text-sm font-semibold text-gray-900 dark:text-white">
                <span class="flex h-6 w-6 items-center justify-center rounded-full bg-primary-100 text-xs font-bold text-primary-700 dark:bg-primary-900/40 dark:text-primary-300">2</span>
                {{ t('admin.subscriptions.guide.step2.title') }}
              </h3>
              <ol class="ml-8 list-decimal space-y-1 text-sm text-gray-600 dark:text-gray-300">
                <li>{{ t('admin.subscriptions.guide.step2.line1') }}</li>
                <li>{{ t('admin.subscriptions.guide.step2.line2') }}</li>
                <li>{{ t('admin.subscriptions.guide.step2.line3') }}</li>
              </ol>
            </div>

            <!-- Step 3 -->
            <div class="mb-5">
              <h3 class="mb-2 flex items-center gap-2 text-sm font-semibold text-gray-900 dark:text-white">
                <span class="flex h-6 w-6 items-center justify-center rounded-full bg-primary-100 text-xs font-bold text-primary-700 dark:bg-primary-900/40 dark:text-primary-300">3</span>
                {{ t('admin.subscriptions.guide.step3.title') }}
              </h3>
              <div class="ml-8 overflow-hidden rounded-lg border border-gray-200 dark:border-dark-600">
                <table class="w-full text-sm">
                  <tbody>
                    <tr v-for="(row, i) in guideActionRows" :key="i" class="border-b border-gray-100 dark:border-dark-700 last:border-0">
                      <td class="whitespace-nowrap bg-gray-50 px-3 py-2 font-medium text-gray-700 dark:bg-dark-700 dark:text-gray-300">{{ row.action }}</td>
                      <td class="px-3 py-2 text-gray-600 dark:text-gray-400">{{ row.desc }}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>

            <!-- Tip -->
            <div class="rounded-lg bg-blue-50 p-3 text-xs text-blue-700 dark:bg-blue-900/20 dark:text-blue-300">
              {{ t('admin.subscriptions.guide.tip') }}
            </div>

            <div class="mt-4 text-right">
              <button type="button" class="btn btn-primary btn-sm" @click="showGuideModal = false">{{ t('common.close') }}</button>
            </div>
          </div>
        </div>
      </transition>
    </teleport>
  </AppLayout>
</template>

<script setup lang="ts">
import { ref, reactive, computed, nextTick, onMounted, onUnmounted, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import { useBalanceDisplay } from '@/composables/useBalanceDisplay'
import type { UserSubscription } from '@/types'
import type { SubscriptionPlan } from '@/types/payment'
import type { SimpleUser } from '@/api/admin/usage'
import type { Column } from '@/components/common/types'
import { formatDateTimeToMinute } from '@/utils/format'
import { getPersistedPageSize } from '@/composables/usePersistedPageSize'
import AppLayout from '@/components/layout/AppLayout.vue'
import TablePageLayout from '@/components/layout/TablePageLayout.vue'
import DataTable from '@/components/common/DataTable.vue'
import Pagination from '@/components/common/Pagination.vue'
import BaseDialog from '@/components/common/BaseDialog.vue'
import AssignSubscriptionDialog from '@/components/admin/subscription/AssignSubscriptionDialog.vue'
import ConfirmDialog from '@/components/common/ConfirmDialog.vue'
import EmptyState from '@/components/common/EmptyState.vue'
import UserAvatar from '@/components/common/UserAvatar.vue'
import Select from '@/components/common/Select.vue'
import Icon from '@/components/icons/Icon.vue'
import { GROUP_PLATFORM_OPTIONS } from '@/constants/platforms'
import { getFloatingPanelPosition } from '@/utils/floatingPanel'
import { readPendingSubscriptionOperation, writePendingSubscriptionOperation } from '@/utils/subscriptionOperationStorage'
import {
  getRemainingDurationParts,
  getRemainingExpiryDuration,
  getSubscriptionQuotaResetTime,
  isOneTimeDailyQuota,
  isQuotaWindowEndingAtSubscriptionExpiry,
  type RemainingDurationParts
} from '@/utils/subscriptionQuota'

const { t } = useI18n()
const appStore = useAppStore()
const { formatBalanceAmount } = useBalanceDisplay()

// Guide modal state
const showGuideModal = ref(false)

const guideActionRows = computed(() => [
  { action: t('admin.subscriptions.guide.actions.adjust'), desc: t('admin.subscriptions.guide.actions.adjustDesc') },
  { action: t('admin.subscriptions.guide.actions.resetQuota'), desc: t('admin.subscriptions.guide.actions.resetQuotaDesc') },
  { action: t('admin.subscriptions.guide.actions.revoke'), desc: t('admin.subscriptions.guide.actions.revokeDesc') }
])

const formatSubscriptionBalance = (value: number | null | undefined): string =>
  formatBalanceAmount(value, { fractionDigits: 2 })

const isPendingSubscription = (subscription: UserSubscription | null): boolean =>
  subscription?.status === 'pending'

const revokeActionText = (subscription: UserSubscription): string =>
  isPendingSubscription(subscription)
    ? t('admin.subscriptions.cancelPending')
    : t('admin.subscriptions.revoke')

const revokeDialogTitle = computed(() =>
  isPendingSubscription(revokingSubscription.value)
    ? t('admin.subscriptions.cancelPendingSubscription')
    : t('admin.subscriptions.revokeSubscription')
)

const revokeDialogMessage = computed(() => {
  const key = isPendingSubscription(revokingSubscription.value)
    ? 'admin.subscriptions.cancelPendingConfirm'
    : 'admin.subscriptions.revokeConfirm'
  return t(key, { user: revokingSubscription.value?.user?.email })
})

const revokeDialogConfirmText = computed(() =>
  isPendingSubscription(revokingSubscription.value)
    ? t('admin.subscriptions.cancelPending')
    : t('admin.subscriptions.revoke')
)

// User column display mode: 'email' or 'username'
const userColumnMode = ref<'email' | 'username'>('email')
const USER_COLUMN_MODE_KEY = 'subscription-user-column-mode'

const loadUserColumnMode = () => {
  try {
    const saved = localStorage.getItem(USER_COLUMN_MODE_KEY)
    if (saved === 'email' || saved === 'username') {
      userColumnMode.value = saved
    }
  } catch (e) {
    console.error('Failed to load user column mode:', e)
  }
}

const saveUserColumnMode = () => {
  try {
    localStorage.setItem(USER_COLUMN_MODE_KEY, userColumnMode.value)
  } catch (e) {
    console.error('Failed to save user column mode:', e)
  }
}

const setUserColumnMode = (mode: 'email' | 'username') => {
  userColumnMode.value = mode
  saveUserColumnMode()
}

// All available columns
const allColumns = computed<Column[]>(() => [
  {
    key: 'user',
    label: userColumnMode.value === 'email'
      ? t('admin.subscriptions.columns.user')
      : t('admin.users.columns.username'),
    sortable: false
  },
  { key: 'group', label: t('payment.admin.planName'), sortable: false },
  { key: 'usage', label: t('admin.subscriptions.columns.usage'), sortable: false },
  { key: 'expires_at', label: t('admin.subscriptions.columns.expires'), sortable: true },
  { key: 'status', label: t('admin.subscriptions.columns.status'), sortable: true },
  { key: 'actions', label: t('admin.subscriptions.columns.actions'), sortable: false }
])

// Columns that can be toggled (exclude user and actions which are always visible)
const toggleableColumns = computed(() =>
  allColumns.value.filter(col => col.key !== 'user' && col.key !== 'actions')
)

// Hidden columns set
const hiddenColumns = reactive<Set<string>>(new Set())

// Default hidden columns
const DEFAULT_HIDDEN_COLUMNS: string[] = []

// localStorage key
const HIDDEN_COLUMNS_KEY = 'subscription-hidden-columns'

// Load saved column settings
const loadSavedColumns = () => {
  try {
    const saved = localStorage.getItem(HIDDEN_COLUMNS_KEY)
    if (saved) {
      const parsed = JSON.parse(saved) as string[]
      parsed.forEach(key => hiddenColumns.add(key))
    } else {
      DEFAULT_HIDDEN_COLUMNS.forEach(key => hiddenColumns.add(key))
    }
  } catch (e) {
    console.error('Failed to load saved columns:', e)
    DEFAULT_HIDDEN_COLUMNS.forEach(key => hiddenColumns.add(key))
  }
}

// Save column settings to localStorage
const saveColumnsToStorage = () => {
  try {
    localStorage.setItem(HIDDEN_COLUMNS_KEY, JSON.stringify([...hiddenColumns]))
  } catch (e) {
    console.error('Failed to save columns:', e)
  }
}

// Toggle column visibility
const toggleColumn = (key: string) => {
  if (hiddenColumns.has(key)) {
    hiddenColumns.delete(key)
  } else {
    hiddenColumns.add(key)
  }
  saveColumnsToStorage()
}

// Check if column is visible
const isColumnVisible = (key: string) => !hiddenColumns.has(key)

// Filtered columns for display
const columns = computed<Column[]>(() =>
  allColumns.value.filter(col =>
    col.key === 'user' || col.key === 'actions' || !hiddenColumns.has(col.key)
  )
)

// Column dropdown state
const showColumnDropdown = ref(false)
const columnDropdownRef = ref<HTMLElement | null>(null)
const showFilterDropdown = ref(false)
const filterDropdownRef = ref<HTMLElement | null>(null)
const filterDropdownButtonRef = ref<HTMLElement | null>(null)
const filterDropdownPosition = reactive({
  top: null as number | null,
  bottom: null as number | null,
  left: 16,
  width: 512,
  maxHeight: 0
})
const filterDropdownStyle = computed(() => ({
  top: filterDropdownPosition.top == null ? 'auto' : `${filterDropdownPosition.top}px`,
  bottom: filterDropdownPosition.bottom == null ? 'auto' : `${filterDropdownPosition.bottom}px`,
  left: `${filterDropdownPosition.left}px`,
  width: `${filterDropdownPosition.width}px`,
  maxHeight: `${filterDropdownPosition.maxHeight}px`
}))

// 过滤面板挂载到 body 后按触发按钮重新定位，确保桌面端和窄屏都不越界。
const updateFilterDropdownPosition = () => {
  if (!showFilterDropdown.value) return
  const trigger = filterDropdownButtonRef.value
  if (!trigger) return
  Object.assign(
    filterDropdownPosition,
    getFloatingPanelPosition(
      trigger.getBoundingClientRect(),
      document.documentElement.clientWidth || window.innerWidth,
      window.innerHeight,
      { maxWidth: 512, mobileBreakpoint: 768 }
    )
  )
}

const toggleFilterDropdown = async () => {
  showFilterDropdown.value = !showFilterDropdown.value
  if (showFilterDropdown.value) {
    await nextTick()
    updateFilterDropdownPosition()
  }
}

// Filter options
const statusOptions = computed(() => [
  { value: '', label: t('admin.subscriptions.allStatus') },
  { value: 'active', label: t('admin.subscriptions.status.active') },
  { value: 'pending', label: t('admin.subscriptions.status.pending') },
  { value: 'expired', label: t('admin.subscriptions.status.expired') },
  { value: 'suspended', label: t('admin.subscriptions.status.suspended') },
  { value: 'revoked', label: t('admin.subscriptions.status.revoked') }
])

const subscriptions = ref<UserSubscription[]>([])
const selectedSubscriptionIDs = ref<number[]>([])
type BulkAction = 'extend' | 'reset' | 'revoke' | 'restore'
const bulkActions: BulkAction[] = ['extend', 'reset', 'revoke', 'restore']
const bulkActionLabels: Record<BulkAction, string> = {
  extend: 'admin.subscriptions.bulkExtend', reset: 'admin.subscriptions.bulkReset',
  revoke: 'admin.subscriptions.bulkRevoke', restore: 'admin.subscriptions.bulkRestore'
}
const bulkAction = ref<BulkAction | null>(null)
const bulkTargetSnapshot = ref<UserSubscription[]>([])
const bulkTargets = computed(() => {
  const selected = subscriptions.value.filter(sub => selectedSubscriptionIDs.value.includes(sub.id))
  return {
    // 延期、重置仍校验完整选择；撤销与恢复分别列出其可操作记录。
    extend: selected, reset: selected,
    revoke: selected.filter(sub => sub.status !== 'revoked'),
    restore: selected.filter(sub => sub.status === 'revoked')
  }
})
const bulkSubscriptionIDs = ref<number[]>([])
const bulkDays = ref(1)
const bulkResetWindows = reactive({ daily: true, weekly: true, monthly: true })
const bulkSubmitting = ref(false)
const bulkError = ref('')
interface BulkRequest {
  key: string
  action: BulkAction
  days: number
  windows: { daily: boolean; weekly: boolean; monthly: boolean }
  unknownResult: boolean
}
const bulkRequest = ref<BulkRequest | null>(null)
// 只保存未确认操作，按管理员隔离；刷新或离开页面后仍沿用原参数与幂等键。
const savedBulk = readPendingSubscriptionOperation<{ request: BulkRequest; targets: UserSubscription[] }>('lifecycle')
if (savedBulk?.request && bulkActions.includes(savedBulk.request.action)
  && typeof savedBulk.request.key === 'string' && savedBulk.request.key.startsWith('subscription-bulk-')
  && Number.isInteger(savedBulk.request.days) && savedBulk.request.days >= 1 && savedBulk.request.days <= 36500
  && ['daily', 'weekly', 'monthly'].every(period => typeof savedBulk.request.windows?.[period as keyof BulkRequest['windows']] === 'boolean')
  && Array.isArray(savedBulk.targets) && savedBulk.targets.length > 0 && savedBulk.targets.length <= 100
  && savedBulk.targets.every(sub => Number.isSafeInteger(sub.id) && sub.id > 0)) {
  bulkRequest.value = { ...savedBulk.request, unknownResult: true }
  bulkTargetSnapshot.value = savedBulk.targets
  bulkSubscriptionIDs.value = savedBulk.targets.map(sub => sub.id)
  bulkDays.value = savedBulk.request.days
  Object.assign(bulkResetWindows, savedBulk.request.windows)
}
watch(bulkRequest, request => {
  writePendingSubscriptionOperation('lifecycle', request ? {
    request,
    // 持久化确认清单所需的最少信息，不保留用量、用户资料等完整对象。
    targets: bulkTargetSnapshot.value.map(sub => ({ id: sub.id, user_id: sub.user_id, user: sub.user ? { email: sub.user.email } : undefined, plan: sub.plan ? { name: sub.plan.name } : undefined }))
  } : null)
}, { deep: true, flush: 'sync' })

const updateSelectedSubscriptions = (keys: Array<string | number>) => {
  if (bulkSubmitting.value) return
  const visibleIDs = new Set(subscriptions.value.map(sub => sub.id))
  selectedSubscriptionIDs.value = [...new Set(keys.map(Number).filter(id => visibleIDs.has(id)))]
}

const openBulkDialog = (action: BulkAction) => {
  // 未确认的请求优先恢复原确认清单，避免关闭后换键重复延长。
  if (bulkRequest.value) {
    bulkAction.value = bulkRequest.value.action
    return
  }
  const selected = bulkTargets.value[action]
  if (!selected.length || selected.length > 100) {
    appStore.showError(t('admin.subscriptions.bulkSelectionLimit'))
    return
  }
  if ((action === 'extend' || action === 'reset') && selected.some(sub => !['active', 'pending', 'expired'].includes(sub.status))) {
    appStore.showError(t('admin.subscriptions.bulkInvalidStatus'))
    return
  }
  bulkSubscriptionIDs.value = selected.map(sub => sub.id)
  bulkTargetSnapshot.value = [...selected]
  bulkAction.value = action
  bulkDays.value = 1
  Object.assign(bulkResetWindows, { daily: true, weekly: true, monthly: true })
  bulkRequest.value = null
  bulkError.value = ''
}

const closeBulkDialog = () => {
  if (bulkSubmitting.value) return
  bulkAction.value = null
  if (!bulkRequest.value) bulkError.value = ''
}

const submitBulkAction = async () => {
  if (!bulkAction.value || bulkSubmitting.value) return
  if (bulkAction.value === 'extend' && (!Number.isInteger(bulkDays.value) || bulkDays.value < 1 || bulkDays.value > 36500)) {
    bulkError.value = t('admin.subscriptions.bulkInvalidDays')
    return
  }
  if (bulkAction.value === 'reset' && !Object.values(bulkResetWindows).some(Boolean)) {
    bulkError.value = t('admin.subscriptions.bulkSelectWindow')
    return
  }
  // 固定确认时的参数，失败后点击确认只重试同一批次。
  bulkRequest.value ??= {
    action: bulkAction.value,
    unknownResult: false,
    key: `subscription-bulk-${globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`}`,
    days: bulkDays.value,
    windows: { ...bulkResetWindows }
  }
  bulkSubmitting.value = true
  bulkError.value = ''
  try {
    const request = bulkRequest.value
    const result = bulkAction.value === 'extend'
      ? await adminAPI.subscriptions.bulkExtend(bulkSubscriptionIDs.value, request.days, request.key)
      : bulkAction.value === 'reset'
        ? await adminAPI.subscriptions.bulkResetQuota(bulkSubscriptionIDs.value, request.windows, request.key)
        : bulkAction.value === 'revoke'
          ? await adminAPI.subscriptions.bulkRevoke(bulkSubscriptionIDs.value, request.key)
          : await adminAPI.subscriptions.bulkRestore(bulkSubscriptionIDs.value, request.key)
    appStore.showSuccess(t('admin.subscriptions.bulkSuccess', { count: result.updated_count }))
    selectedSubscriptionIDs.value = []
    bulkAction.value = null
    bulkRequest.value = null
    await loadSubscriptions()
  } catch (error: any) {
    // apiClient 返回顶层结构，兼容测试与旧调用方的 Axios 原始错误。
    const failure = error.response?.data ?? error
    const status = error.response?.status ?? error.status
    const rejectedBeforeCommit = [400, 404, 409].includes(status)
      && (failure?.reason?.startsWith('SUBSCRIPTION_') || failure?.reason === 'INVALID_INPUT')
    if (rejectedBeforeCommit && !bulkRequest.value?.unknownResult) bulkRequest.value = null
    else if (bulkRequest.value) bulkRequest.value.unknownResult = true
    bulkError.value = failure?.reason === 'SUBSCRIPTION_BULK_EXPIRED_HAS_SUCCESSOR'
      ? t('admin.subscriptions.bulkExpiredHasSuccessor', { id: failure.metadata?.subscription_id ?? '-' })
      : failure?.message || t('admin.subscriptions.bulkFailed')
  } finally {
    bulkSubmitting.value = false
  }
}
const plans = ref<SubscriptionPlan[]>([])
const loading = ref(false)
let abortController: AbortController | null = null

// Toolbar user filter (fuzzy search -> select user_id)
const filterUserKeyword = ref('')
const filterUserResults = ref<SimpleUser[]>([])
const filterUserLoading = ref(false)
const showFilterUserDropdown = ref(false)
const selectedFilterUser = ref<SimpleUser | null>(null)
let filterUserSearchTimeout: ReturnType<typeof setTimeout> | null = null

const filters = reactive({
  status: 'active',
  plan_id: '',
  platform: '',
  user_id: null as number | null
})

const activeFilterCount = computed(() => [filters.status, filters.plan_id, filters.platform].filter(Boolean).length)

const resetSubscriptionFilters = () => {
  filters.status = ''
  filters.plan_id = ''
  filters.platform = ''
  applyFilters()
}

// Sorting state
const sortState = reactive({
  sort_by: 'created_at',
  sort_order: 'desc' as 'asc' | 'desc'
})

const pagination = reactive({
  page: 1,
  page_size: getPersistedPageSize(),
  total: 0,
  pages: 0
})

const showAssignModal = ref(false)
const showExtendModal = ref(false)
const showRevokeDialog = ref(false)
const showRestoreDialog = ref(false)
const showResetQuotaConfirm = ref(false)
const submitting = ref(false)
const resettingSubscription = ref<UserSubscription | null>(null)
const resettingQuota = ref(false)
const extendingSubscription = ref<UserSubscription | null>(null)
const revokingSubscription = ref<UserSubscription | null>(null)
const restoringSubscription = ref<UserSubscription | null>(null)

const extendForm = reactive({
  days: 30
})

const planOptions = computed(() => [
  { value: '', label: t('admin.announcements.form.selectPackages') },
  ...plans.value.map((plan) => ({ value: plan.id.toString(), label: plan.name }))
])

const platformFilterOptions = computed(() => [
  { value: '', label: t('admin.subscriptions.allPlatforms') },
  ...GROUP_PLATFORM_OPTIONS
])

const applyFilters = () => {
  selectedSubscriptionIDs.value = []
  pagination.page = 1
  loadSubscriptions()
}

const loadSubscriptions = async () => {
  if (abortController) {
    abortController.abort()
  }
  const requestController = new AbortController()
  abortController = requestController
  const { signal } = requestController

  loading.value = true
  try {
    const response = await adminAPI.subscriptions.list(
      pagination.page,
      pagination.page_size,
      {
        status: (filters.status as any) || undefined,
        plan_id: filters.plan_id ? parseInt(filters.plan_id) : undefined,
        platform: filters.platform || undefined,
        user_id: filters.user_id || undefined,
        sort_by: sortState.sort_by,
        sort_order: sortState.sort_order
      },
      {
        signal
      }
    )
    if (signal.aborted || abortController !== requestController) return
    subscriptions.value = response.items
    selectedSubscriptionIDs.value = selectedSubscriptionIDs.value.filter(id => response.items.some(sub => sub.id === id))
    pagination.total = response.total
    pagination.pages = response.pages
  } catch (error: any) {
    if (signal.aborted || error?.name === 'AbortError' || error?.code === 'ERR_CANCELED') {
      return
    }
    appStore.showError(t('admin.subscriptions.failedToLoad'))
    console.error('Error loading subscriptions:', error)
  } finally {
    if (abortController === requestController) {
      loading.value = false
      abortController = null
    }
  }
}

const loadPlans = async () => {
  try {
    const response = await adminAPI.payment.getPlans()
    plans.value = response.data || []
  } catch (error) {
    console.error('Error loading plans:', error)
  }
}

// Toolbar user filter search with debounce
const debounceSearchFilterUsers = () => {
  if (filterUserSearchTimeout) {
    clearTimeout(filterUserSearchTimeout)
  }
  filterUserSearchTimeout = setTimeout(searchFilterUsers, 300)
}

const searchFilterUsers = async () => {
  const keyword = filterUserKeyword.value.trim()

  // Clear active user filter if user modified the search keyword
  if (selectedFilterUser.value && keyword !== selectedFilterUser.value.email) {
    selectedFilterUser.value = null
    filters.user_id = null
    applyFilters()
  }

  if (!keyword) {
    filterUserResults.value = []
    return
  }

  filterUserLoading.value = true
  try {
    filterUserResults.value = await adminAPI.usage.searchUsers(keyword)
  } catch (error) {
    console.error('Failed to search users:', error)
    filterUserResults.value = []
  } finally {
    filterUserLoading.value = false
  }
}

const selectFilterUser = (user: SimpleUser) => {
  selectedFilterUser.value = user
  filterUserKeyword.value = user.email
  showFilterUserDropdown.value = false
  filters.user_id = user.id
  applyFilters()
}

const clearFilterUser = () => {
  selectedFilterUser.value = null
  filterUserKeyword.value = ''
  filterUserResults.value = []
  showFilterUserDropdown.value = false
  filters.user_id = null
  applyFilters()
}

const handlePageChange = (page: number) => {
  selectedSubscriptionIDs.value = []
  pagination.page = page
  loadSubscriptions()
}

const handlePageSizeChange = (pageSize: number) => {
  selectedSubscriptionIDs.value = []
  pagination.page_size = pageSize
  pagination.page = 1
  loadSubscriptions()
}

const handleSort = (key: string, order: 'asc' | 'desc') => {
  selectedSubscriptionIDs.value = []
  sortState.sort_by = key
  sortState.sort_order = order
  pagination.page = 1
  loadSubscriptions()
}

const handleExtend = (subscription: UserSubscription) => {
  extendingSubscription.value = subscription
  extendForm.days = getTargetValidityDays(subscription)
  showExtendModal.value = true
}

const closeExtendModal = () => {
  showExtendModal.value = false
  extendingSubscription.value = null
}

const handleExtendSubscription = async () => {
  if (!extendingSubscription.value) return

  if (!Number.isFinite(extendForm.days) || extendForm.days < 1) {
    appStore.showError(t('admin.subscriptions.validityDaysRequired'))
    return
  }

  submitting.value = true
  try {
    await adminAPI.subscriptions.extend(extendingSubscription.value.id, {
      days: extendForm.days
    })
    appStore.showSuccess(t('admin.subscriptions.subscriptionAdjusted'))
    closeExtendModal()
    loadSubscriptions()
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.subscriptions.failedToAdjust'))
    console.error('Error adjusting subscription:', error)
  } finally {
    submitting.value = false
  }
}

const handleRevoke = (subscription: UserSubscription) => {
  revokingSubscription.value = subscription
  showRevokeDialog.value = true
}

const confirmRevoke = async () => {
  if (!revokingSubscription.value) return

  const pending = isPendingSubscription(revokingSubscription.value)
  try {
    await adminAPI.subscriptions.revoke(revokingSubscription.value.id)
    appStore.showSuccess(
      pending
        ? t('admin.subscriptions.pendingSubscriptionCancelled')
        : t('admin.subscriptions.subscriptionRevoked')
    )
    showRevokeDialog.value = false
    revokingSubscription.value = null
    loadSubscriptions()
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.subscriptions.failedToRevoke'))
    console.error('Error revoking subscription:', error)
  }
}

const handleRestore = (subscription: UserSubscription) => {
  restoringSubscription.value = subscription
  showRestoreDialog.value = true
}

const confirmRestore = async () => {
  if (!restoringSubscription.value) return

  try {
    await adminAPI.subscriptions.restore(restoringSubscription.value.id)
    appStore.showSuccess(t('admin.subscriptions.subscriptionRestored'))
    showRestoreDialog.value = false
    restoringSubscription.value = null
    loadSubscriptions()
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.subscriptions.failedToRestore'))
    console.error('Error restoring subscription:', error)
  }
}

const handleResetQuota = (subscription: UserSubscription) => {
  resettingSubscription.value = subscription
  showResetQuotaConfirm.value = true
}

const confirmResetQuota = async () => {
  if (!resettingSubscription.value) return
  if (resettingQuota.value) return
  resettingQuota.value = true
  try {
    await adminAPI.subscriptions.resetQuota(resettingSubscription.value.id, { daily: true, weekly: true, monthly: true })
    appStore.showSuccess(t('admin.subscriptions.quotaResetSuccess'))
    showResetQuotaConfirm.value = false
    resettingSubscription.value = null
    await loadSubscriptions()
  } catch (error: any) {
    appStore.showError(error.response?.data?.detail || t('admin.subscriptions.failedToResetQuota'))
    console.error('Error resetting quota:', error)
  } finally {
    resettingQuota.value = false
  }
}

// Helper functions
const getDaysRemaining = (expiresAt: string): number | null => {
  const now = new Date()
  const expires = new Date(expiresAt)
  const diff = expires.getTime() - now.getTime()
  if (diff < 0) return null
  return Math.ceil(diff / (1000 * 60 * 60 * 24))
}

// formatRemainingExpiry 根据实际剩余时长选择天或小时/分钟文案。
const formatRemainingExpiry = (expiresAt: string): string | null => {
  const duration = getRemainingExpiryDuration(expiresAt)
  if (!duration) return null
  if (duration.unit === 'days') {
    return t('admin.subscriptions.daysRemaining', { days: duration.days })
  }
  if (duration.hours) {
    return t('admin.subscriptions.hoursMinutesRemaining', {
      hours: duration.hours,
      minutes: duration.minutes
    })
  }
  return t('admin.subscriptions.minutesRemaining', { minutes: duration.minutes })
}

const getTargetValidityDays = (subscription: UserSubscription): number => {
  const startsAt = new Date(subscription.starts_at)
  const expiresAt = new Date(subscription.expires_at)
  const now = new Date()
  // 待生效订阅按它自己的开始时间计算有效期，生效中订阅按当前剩余天数计算。
  const anchor = now < startsAt ? startsAt : now
  const diff = expiresAt.getTime() - anchor.getTime()
  return Math.max(1, Math.ceil(diff / (1000 * 60 * 60 * 24)))
}

const isExpiringSoon = (expiresAt: string): boolean => {
  const days = getDaysRemaining(expiresAt)
  return days !== null && days <= 7
}

const getProgressWidth = (used: number | null | undefined, limit: number | null): string => {
  if (!limit || limit === 0) return '0%'
  const usedValue = used ?? 0
  const percentage = Math.min((usedValue / limit) * 100, 100)
  return `${percentage}%`
}

const getProgressClass = (used: number | null | undefined, limit: number | null): string => {
  if (!limit || limit === 0) return 'bg-gray-400'
  const usedValue = used ?? 0
  const percentage = (usedValue / limit) * 100
  if (percentage >= 90) return 'bg-red-500'
  if (percentage >= 70) return 'bg-orange-500'
  return 'bg-green-500'
}

const formatResetDuration = (parts: RemainingDurationParts): string => {
  if (parts.days > 0) {
    return t('admin.subscriptions.resetInDaysHours', { days: parts.days, hours: parts.hours })
  }
  if (parts.hours > 0) {
    return t('admin.subscriptions.resetInHoursMinutes', { hours: parts.hours, minutes: parts.minutes })
  }
  return t('admin.subscriptions.resetInMinutes', { minutes: parts.minutes })
}

const formatQuotaEndDuration = (parts: RemainingDurationParts): string => {
  if (parts.days > 0) {
    return t('admin.subscriptions.quotaEndsInDaysHours', { days: parts.days, hours: parts.hours })
  }
  if (parts.hours > 0) {
    return t('admin.subscriptions.quotaEndsInHoursMinutes', { hours: parts.hours, minutes: parts.minutes })
  }
  return t('admin.subscriptions.quotaEndsInMinutes', { minutes: parts.minutes })
}

const formatDailyUsageWindow = (subscription: UserSubscription): string => {
  if (isOneTimeDailyQuota(subscription)) {
    const parts = getRemainingDurationParts(subscription.expires_at)
    return parts ? formatQuotaEndDuration(parts) : t('admin.subscriptions.windowNotActive')
  }
  return formatUsageWindow(subscription, subscription.daily_window_start, 'daily')
}

const formatUsageWindow = (
  subscription: UserSubscription,
  windowStart: string | null,
  period: 'daily' | 'weekly' | 'monthly'
): string => {
  if (isQuotaWindowEndingAtSubscriptionExpiry(subscription, windowStart, period)) {
    const parts = getRemainingDurationParts(subscription.expires_at)
    return parts ? formatQuotaEndDuration(parts) : t('admin.subscriptions.windowNotActive')
  }
  return formatResetTime(subscription, windowStart, period)
}

const formatResetTime = (
  subscription: UserSubscription,
  windowStart: string | null,
  period: 'daily' | 'weekly' | 'monthly'
): string => {
  const resetTime = getSubscriptionQuotaResetTime(subscription, windowStart, period)
  if (!resetTime) return t('admin.subscriptions.windowNotActive')
  const parts = getRemainingDurationParts(resetTime)
  return parts ? formatResetDuration(parts) : t('admin.subscriptions.windowNotActive')
}

// Handle click outside to close dropdowns
const handleClickOutside = (event: MouseEvent) => {
  const target = event.target as HTMLElement
  if (!target.closest('[data-filter-user-search]')) showFilterUserDropdown.value = false
  if (columnDropdownRef.value && !columnDropdownRef.value.contains(target)) {
    showColumnDropdown.value = false
  }
  if (filterDropdownRef.value && !filterDropdownRef.value.contains(target)) {
    showFilterDropdown.value = false
  }
}

onMounted(() => {
  loadUserColumnMode()
  loadSavedColumns()
  loadSubscriptions()
  loadPlans()
  document.addEventListener('click', handleClickOutside)
  window.addEventListener('resize', updateFilterDropdownPosition)
  window.addEventListener('scroll', updateFilterDropdownPosition, true)
})

onUnmounted(() => {
  document.removeEventListener('click', handleClickOutside)
  window.removeEventListener('resize', updateFilterDropdownPosition)
  window.removeEventListener('scroll', updateFilterDropdownPosition, true)
  if (filterUserSearchTimeout) {
    clearTimeout(filterUserSearchTimeout)
  }
})
</script>

<style scoped>
.usage-row {
  @apply space-y-1;
}

.usage-label {
  @apply w-10 flex-shrink-0 text-xs font-medium text-gray-500 dark:text-gray-400;
}

.usage-amount {
  @apply whitespace-nowrap text-xs tabular-nums text-gray-600 dark:text-gray-300;
}

.reset-info {
  @apply flex items-center gap-1 text-[10px] text-blue-600 dark:text-blue-400;
}

.reset-count {
  @apply cursor-help whitespace-nowrap rounded bg-gray-100 px-1.5 py-0.5 text-[10px] text-gray-500 dark:bg-dark-700 dark:text-dark-400;
}
</style>
