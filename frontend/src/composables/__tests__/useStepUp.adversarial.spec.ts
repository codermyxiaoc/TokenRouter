import { describe, expect, it, vi } from 'vitest'
import { isStepUpBlocked, isStepUpRequired, StepUpCancelledError, useStepUp } from '../useStepUp'

// 并发测试只替换内存 action，绝不请求真实 TOTP 或管理员接口。
describe('敏感操作并发与错误输入', () => {
  it.each([null, undefined, '', 403, {}, { code: 403 }, { reason: '<script>STEP_UP_REQUIRED</script>' }])('未知错误 %j 不触发授权重试', (error) => {
    expect(isStepUpRequired(error)).toBe(false)
    expect(isStepUpBlocked(error)).toBe(false)
  })

  it.each([true, false])('八个并发操作在验证结果 %s 后全部结算', async (verified) => {
    const controller = useStepUp()
    const actions = Array.from({ length: 8 }, (_, i) => vi.fn()
      .mockRejectedValueOnce({ code: 'STEP_UP_REQUIRED' }).mockResolvedValue(i))
    const results = Promise.allSettled(actions.map(action => controller.run(action)))
    await Promise.resolve()
    expect(controller.visible.value).toBe(true)
    if (verified) controller.onVerified()
    else controller.onCancel()
    const settled = await results
    expect(settled).toHaveLength(8)
    settled.forEach((result, i) => {
      if (verified) expect(result).toEqual({ status: 'fulfilled', value: i })
      else {
        expect(result.status).toBe('rejected')
        if (result.status === 'rejected') expect(result.reason).toBeInstanceOf(StepUpCancelledError)
      }
      expect(actions[i]).toHaveBeenCalledTimes(verified ? 2 : 1)
    })
  })

  it('重试再次要求验证时向调用者报错，不循环执行敏感动作', async () => {
    const controller = useStepUp()
    const error = { code: 'STEP_UP_REQUIRED' }
    const action = vi.fn().mockRejectedValue(error)
    const result = controller.run(action)
    await Promise.resolve()
    controller.onVerified()
    await expect(result).rejects.toBe(error)
    expect(action).toHaveBeenCalledTimes(2)
    expect(controller.visible.value).toBe(false)
  })

  it('一个重试失败不改变其他并发动作的结算结果', async () => {
    const controller = useStepUp()
    const first = controller.run(vi.fn().mockRejectedValueOnce({ code: 'STEP_UP_REQUIRED' }).mockRejectedValue('failed'))
    const second = controller.run(vi.fn().mockRejectedValueOnce({ reason: 'STEP_UP_REQUIRED' }).mockResolvedValue('ok'))
    const settled = Promise.allSettled([first, second])
    await Promise.resolve()
    controller.onVerified()
    expect(await settled).toEqual([{ status: 'rejected', reason: 'failed' }, { status: 'fulfilled', value: 'ok' }])
  })
})
