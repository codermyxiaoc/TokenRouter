import { describe, expect, it, vi } from 'vitest'

import { formatDateLocalInput, formatDateTimeLocalInput } from '../format'

describe('formatDateLocalInput', () => {
  it('formats the calendar date in local time', () => {
    const localDate = new Date('2026-07-12T16:30:00Z')
    vi.spyOn(localDate, 'getFullYear').mockReturnValue(2026)
    vi.spyOn(localDate, 'getMonth').mockReturnValue(6)
    vi.spyOn(localDate, 'getDate').mockReturnValue(13)

    expect(formatDateLocalInput(localDate)).toBe('2026-07-13')
  })

  it('returns an empty string for an invalid date', () => {
    expect(formatDateLocalInput(new Date('invalid'))).toBe('')
  })
})

describe('formatDateTimeLocalInput', () => {
  it('formats the date and time with local fields', () => {
    vi.spyOn(Date.prototype, 'getFullYear').mockReturnValue(2026)
    vi.spyOn(Date.prototype, 'getMonth').mockReturnValue(6)
    vi.spyOn(Date.prototype, 'getDate').mockReturnValue(13)
    vi.spyOn(Date.prototype, 'getHours').mockReturnValue(0)
    vi.spyOn(Date.prototype, 'getMinutes').mockReturnValue(30)

    expect(formatDateTimeLocalInput(1_783_893_000)).toBe('2026-07-13T00:30')
  })
})
