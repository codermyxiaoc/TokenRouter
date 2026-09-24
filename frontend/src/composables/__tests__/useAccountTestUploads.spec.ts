import { afterEach, describe, expect, it, vi } from 'vitest'
import { useAccountTestUploads } from '../useAccountTestUploads'

function uploadEvent(file: File) {
  return { target: { files: [file], value: 'selected' } } as unknown as Event
}

// 文件读取与切换账号可以交错完成，旧文件不得流入下一次请求。
describe('useAccountTestUploads', () => {
  afterEach(() => vi.unstubAllGlobals())

  it('rejects oversized images, audio and unsupported image formats before reading', async () => {
    const onError = vi.fn()
    const read = vi.fn()
    vi.stubGlobal('FileReader', class { readAsDataURL = read })
    const uploads = useAccountTestUploads((key) => key, onError)
    await uploads.onImageFileChange(uploadEvent(new File([new Uint8Array(4 * 1024 * 1024 + 1)], 'image.png', { type: 'image/png' })))
    await uploads.onAudioFileChange(uploadEvent(new File([new Uint8Array(6 * 1024 * 1024 + 1)], 'audio.wav', { type: 'audio/wav' })))
    await uploads.onImageFileChange(uploadEvent(new File(['<svg/>'], 'image.svg', { type: 'image/svg+xml' })))
    expect(onError).toHaveBeenCalledTimes(3)
    expect(read).not.toHaveBeenCalled()
    expect(uploads.uploadImageDataURL.value).toBe('')
    expect(uploads.uploadAudioDataURL.value).toBe('')
  })

  it('drops an in-flight read when the modal is closed or the mode changes', async () => {
    let finishRead = () => {}
    vi.stubGlobal('FileReader', class {
      result = 'data:image/png;base64,AAAA'
      onload = () => {}
      readAsDataURL() { finishRead = () => this.onload() }
    })
    const uploads = useAccountTestUploads((key) => key, vi.fn())
    const pending = uploads.onImageFileChange(uploadEvent(new File(['png'], 'image.png', { type: 'image/png' })))
    expect(uploads.loadingUpload.value).toBe(true)
    uploads.clearMediaUploads()
    finishRead()
    await pending
    expect(uploads.loadingUpload.value).toBe(false)
    expect(uploads.uploadImageDataURL.value).toBe('')
    expect(uploads.uploadImageName.value).toBe('')
  })

  it('keeps only the most recent selected file', async () => {
    const readers: { result: string; onload: () => void }[] = []
    vi.stubGlobal('FileReader', class {
      result = ''
      onload = () => {}
      readAsDataURL(file: File) {
        this.result = `data:audio/wav;base64,${file.name}`
        readers.push(this)
      }
    })
    const uploads = useAccountTestUploads((key) => key, vi.fn())
    const first = uploads.onAudioFileChange(uploadEvent(new File(['wav'], 'first.wav', { type: 'audio/wav' })))
    const second = uploads.onAudioFileChange(uploadEvent(new File(['wav'], 'second.wav', { type: 'audio/wav' })))
    readers[1].onload()
    await second
    readers[0].onload()
    await first
    expect(uploads.uploadAudioDataURL.value).toBe('data:audio/wav;base64,second.wav')
    expect(uploads.uploadAudioName.value).toBe('second.wav')
    expect(uploads.loadingUpload.value).toBe(false)
  })
  it.each([
    ['test.wav', '', 'audio/wav'],
    ['test.mp3', 'application/octet-stream', 'audio/mpeg'],
    ['test.webm', 'video/webm', 'audio/webm']
  ])('normalizes an unknown audio MIME for %s', async (name, mime, expected) => {
    vi.stubGlobal('FileReader', class {
      result = ''
      onload = () => {}
      readAsDataURL(file: File) {
        this.result = `data:${file.type};base64,AAAA`
        this.onload()
      }
    })
    const uploads = useAccountTestUploads((key) => key, vi.fn())
    await uploads.onAudioFileChange(uploadEvent(new File(['audio'], name, { type: mime })))
    expect(uploads.uploadAudioDataURL.value).toBe(`data:${expected};base64,AAAA`)
  })

})
