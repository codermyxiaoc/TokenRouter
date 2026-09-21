import { beforeEach, describe, expect, it, vi } from 'vitest'
import { readPendingSubscriptionOperation, writePendingSubscriptionOperation } from '../subscriptionOperationStorage'

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  vi.restoreAllMocks()
})

describe('订阅待确认操作存储', () => {
  it('按管理员和操作类型隔离，不跨用户读取或删除请求', () => {
    localStorage.setItem('auth_user', JSON.stringify({ id: 1 }))
    writePendingSubscriptionOperation('assign', { key: 'assign-1' })
    writePendingSubscriptionOperation('lifecycle', { key: 'extend-1' })
    localStorage.setItem('auth_user', JSON.stringify({ id: 2 }))
    expect(readPendingSubscriptionOperation('assign')).toBeNull()
    writePendingSubscriptionOperation('assign', null)
    localStorage.setItem('auth_user', JSON.stringify({ id: 1 }))
    expect(readPendingSubscriptionOperation('assign')).toEqual({ key: 'assign-1' })
    expect(readPendingSubscriptionOperation('lifecycle')).toEqual({ key: 'extend-1' })
    writePendingSubscriptionOperation('assign', null)
    expect(readPendingSubscriptionOperation('assign')).toBeNull()
  })

  it.each([null, {}, { id: '1' }, { id: 0 }, { id: -1 }])('无效身份 %j 不持久保存', identity => {
    localStorage.setItem('auth_user', JSON.stringify(identity))
    writePendingSubscriptionOperation('assign', { key: 'pending' })
    expect(sessionStorage.length).toBe(0)
    expect(readPendingSubscriptionOperation('assign')).toBeNull()
  })

  it('存储禁止访问或数据损坏时保持调用安全', () => {
    localStorage.setItem('auth_user', '{invalid')
    expect(readPendingSubscriptionOperation('assign')).toBeNull()
    expect(() => writePendingSubscriptionOperation('assign', {})).not.toThrow()
    localStorage.setItem('auth_user', JSON.stringify({ id: 1 }))
    sessionStorage.setItem('subscription-operation:1:assign', '{invalid')
    expect(readPendingSubscriptionOperation('assign')).toBeNull()
    vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => { throw new Error('Storage denied') })
    expect(() => writePendingSubscriptionOperation('assign', {})).not.toThrow()
  })
})
