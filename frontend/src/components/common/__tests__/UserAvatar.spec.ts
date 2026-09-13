import { mount } from '@vue/test-utils'
import UserAvatar from '../UserAvatar.vue'

describe('UserAvatar', () => {
  it('有 avatarUrl 时优先展示上传头像', () => {
    const wrapper = mount(UserAvatar, {
      props: {
        avatarUrl: 'https://cdn.example.com/a.png',
        userId: 7,
        alt: 'alice',
      },
    })

    const img = wrapper.get('img')
    expect(img.attributes('src')).toBe('https://cdn.example.com/a.png')
    expect(img.attributes('alt')).toBe('alice')
  })

  it('无 avatarUrl 时按用户 ID 生成 identicon', () => {
    const wrapper = mount(UserAvatar, {
      props: { userId: 7 },
    })

    const src = wrapper.get('img').attributes('src') || ''
    expect(src.startsWith('data:image/svg+xml,')).toBe(true)
  })

  it('相同用户 ID 渲染相同默认头像，不同 ID 不同', () => {
    const first = mount(UserAvatar, { props: { userId: 7 } })
    const second = mount(UserAvatar, { props: { userId: 7 } })
    const other = mount(UserAvatar, { props: { userId: 8 } })

    expect(first.get('img').attributes('src')).toBe(second.get('img').attributes('src'))
    expect(first.get('img').attributes('src')).not.toBe(other.get('img').attributes('src'))
  })

  it('用户 ID 缺失时使用兜底种子', () => {
    const bySeed = mount(UserAvatar, { props: { seed: 'user-42' } })
    const sameSeed = mount(UserAvatar, { props: { seed: 'user-42' } })

    expect(bySeed.get('img').attributes('src')).toBe(sameSeed.get('img').attributes('src'))
  })

  it('透传尺寸 class', () => {
    const wrapper = mount(UserAvatar, {
      props: { userId: 7, sizeClass: 'h-20 w-20 shrink-0' },
    })

    const classes = wrapper.get('img').classes()
    expect(classes).toContain('h-20')
    expect(classes).toContain('w-20')
    expect(classes).toContain('rounded-full')
  })
})
