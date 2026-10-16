import { mount } from '@vue/test-utils'
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import AccountTestVideoPreview from '../AccountTestVideoPreview.vue'

vi.mock('vue-i18n', () => ({ useI18n: () => ({ t: (key: string) => key }) }))

describe('Account test video preview loading', () => {
  beforeEach(() => vi.useFakeTimers())
  afterEach(() => vi.useRealTimers())

  // stalled 只是暂时没收到数据；30 秒提示也不能取消仍在传输的媒体。
  it('waits for metadata, reports a slow connection after 30 seconds, and accepts late metadata', async () => {
    const wrapper = mount(AccountTestVideoPreview, { props: { url: 'https://cdn.example/video.mp4' } })
    const player = wrapper.get('video')
    expect(wrapper.get('[data-testid="account-test-video-preview-loading"]').exists()).toBe(true)
    await player.trigger('stalled')
    await vi.advanceTimersByTimeAsync(29_999)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    await vi.advanceTimersByTimeAsync(1)
    expect(wrapper.get('[role="alert"]').text()).toBe('admin.accounts.videoPreviewTimedOut')
    expect(wrapper.find('[data-testid="account-test-video-preview-loading"]').exists()).toBe(false)
    expect(wrapper.get('video').element).toBe(player.element)
    await player.trigger('loadedmetadata')
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(vi.getTimerCount()).toBe(0)
    wrapper.unmount()
  })

  it('clears the previous timer when the source changes and ignores the old player', async () => {
    const wrapper = mount(AccountTestVideoPreview, { props: { url: 'https://cdn.example/old.mp4' } })
    const oldPlayer = wrapper.get('video')
    await vi.advanceTimersByTimeAsync(20_000)
    await wrapper.setProps({ url: 'https://cdn.example/new.mp4' })
    expect(vi.getTimerCount()).toBe(1)
    await oldPlayer.trigger('loadedmetadata')
    await vi.advanceTimersByTimeAsync(10_000)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    expect(wrapper.find('[data-testid="account-test-video-preview-loading"]').exists()).toBe(true)
    await vi.advanceTimersByTimeAsync(20_000)
    expect(wrapper.get('[role="alert"]').text()).toBe('admin.accounts.videoPreviewTimedOut')
    wrapper.unmount()
  })

  it('clears the timer on error and gives a reloaded player a new timeout window', async () => {
    const wrapper = mount(AccountTestVideoPreview, { props: { url: 'https://cdn.example/video.mp4' } })
    await vi.advanceTimersByTimeAsync(20_000)
    await wrapper.get('video').trigger('error')
    expect(vi.getTimerCount()).toBe(0)
    await wrapper.get('[data-testid="account-test-video-reload"]').trigger('click')
    expect(vi.getTimerCount()).toBe(1)
    await vi.advanceTimersByTimeAsync(10_000)
    expect(wrapper.find('[role="alert"]').exists()).toBe(false)
    await wrapper.get('video').trigger('loadedmetadata')
    expect(vi.getTimerCount()).toBe(0)
    wrapper.unmount()
  })

  it('clears pending timers on unmount and creates none for an invalid media URL', () => {
    const wrapper = mount(AccountTestVideoPreview, { props: { url: 'https://cdn.example/video.mp4' } })
    expect(vi.getTimerCount()).toBe(1)
    wrapper.unmount()
    expect(vi.getTimerCount()).toBe(0)
    const invalid = mount(AccountTestVideoPreview, { props: { url: 'javascript:alert(1)' } })
    expect(vi.getTimerCount()).toBe(0)
    expect(invalid.find('[data-testid="account-test-video-preview-loading"]').exists()).toBe(false)
    invalid.unmount()
  })
})
