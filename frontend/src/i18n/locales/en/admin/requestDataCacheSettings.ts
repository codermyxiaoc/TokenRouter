// System settings - data cache
export default {
  requestDataCacheSettings: {
    title: 'Data cache',
    description: 'Cache endpoint information for requests in usage records (inbound/upstream endpoint, method, model, status). Normal and error requests are configured separately. Entries expire automatically and can be cleared manually.',
    normalTitle: 'Normal request cache',
    normalHint: 'Cache for successful requests. When enabled, every request writes an extra snapshot, so limit groups and duration as needed.',
    errorTitle: 'Error request cache',
    errorHint: 'Cache for failed requests. Enabled for all groups with 30-day retention by default, same as before.',
    enabled: 'Enable cache',
    ttl: 'Cache duration',
    units: { minutes: 'Minutes', hours: 'Hours', days: 'Days' },
    ttlInvalid: 'Cache duration must be between 1 minute and 365 days',
    scope: 'Cached groups',
    allGroups: 'All groups',
    selectedGroups: 'Selected groups',
    includeUngrouped: 'Also cache requests without a group',
    groupsRequired: 'Select at least one group, or include requests without a group',
    cachedCount: '{count} entries cached',
    clear: 'Clear cache',
    clearAll: 'Clear all caches',
    clearConfirmTitle: 'Clear data cache',
    clearConfirm: 'Clear {target}? Users will no longer see endpoint information for these requests. This cannot be undone.',
    targetNormal: 'the normal request cache',
    targetError: 'the error request cache',
    targetAll: 'all data caches',
    cleared: 'Cleared {count} entries',
    saved: 'Data cache settings saved',
    loadFailed: 'Failed to load data cache settings',
    saveFailed: 'Failed to save data cache settings',
    clearFailed: 'Failed to clear cache'
  }
}
