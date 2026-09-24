import { ref } from 'vue'

// 上传仅保存在当前弹窗内；切换模式或关闭弹窗会使未完成的读取失效。
export function useAccountTestUploads(t: (key: string) => string, onError: (message: string) => void) {
  const imageFileInput = ref<HTMLInputElement | null>(null)
  const audioFileInput = ref<HTMLInputElement | null>(null)
  const uploadImageDataURL = ref('')
  const uploadImageName = ref('')
  const uploadAudioDataURL = ref('')
  const uploadAudioName = ref('')
  const loadingUpload = ref(false)
  let generation = 0

  const clearMediaUploads = () => {
    generation++
    loadingUpload.value = false
    uploadImageDataURL.value = ''
    uploadImageName.value = ''
    uploadAudioDataURL.value = ''
    uploadAudioName.value = ''
    if (imageFileInput.value) imageFileInput.value.value = ''
    if (audioFileInput.value) audioFileInput.value.value = ''
  }
  const readUpload = async (event: Event, kind: 'image' | 'audio') => {
    const input = event.target as HTMLInputElement
    const file = input.files?.[0]
    const current = ++generation
    loadingUpload.value = false
    if (kind === 'image') {
      uploadImageDataURL.value = ''
      uploadImageName.value = ''
    } else {
      uploadAudioDataURL.value = ''
      uploadAudioName.value = ''
    }
    if (!file) return
    const extension = file.name.split('.').pop()?.toLowerCase() || ''
    const audioMimes: Record<string, string> = { wav: 'audio/wav', mp3: 'audio/mpeg', m4a: 'audio/mp4', ogg: 'audio/ogg', webm: 'audio/webm' }
    const inferredAudioMime = ['', 'application/octet-stream', 'video/webm'].includes(file.type) ? audioMimes[extension] : undefined
    const valid = kind === 'image'
      ? ['image/png', 'image/jpeg', 'image/gif', 'image/webp'].includes(file.type)
      : file.type.startsWith('audio/') || Boolean(inferredAudioMime)
    if (!valid || file.size > (kind === 'image' ? 4 : 6) * 1024 * 1024) {
      input.value = ''
      onError(t(valid ? 'admin.accounts.grok.mediaTooLarge' : 'admin.accounts.grok.mediaInvalidType'))
      return
    }
    loadingUpload.value = true
    try {
      const dataURL = await new Promise<string>((resolve, reject) => {
        const reader = new FileReader()
        reader.onload = () => resolve(String(reader.result || ''))
        reader.onerror = () => reject(new Error('file read failed'))
        // 浏览器无法识别常见音频时补安全 MIME，后端只接收 audio/* data URL。
        const payload = kind === 'audio' && inferredAudioMime
          ? new File([file], file.name, { type: inferredAudioMime, lastModified: file.lastModified })
          : file
        reader.readAsDataURL(payload)
      })
      if (current !== generation) return
      if (kind === 'image') {
        uploadImageDataURL.value = dataURL
        uploadImageName.value = file.name
      } else {
        uploadAudioDataURL.value = dataURL
        uploadAudioName.value = file.name
      }
    } catch {
      if (current === generation) onError(t('admin.accounts.grok.fileReadFailed'))
    } finally {
      if (current === generation) loadingUpload.value = false
    }
  }
  return {
    imageFileInput, audioFileInput, uploadImageDataURL, uploadImageName, uploadAudioDataURL,
    uploadAudioName, loadingUpload, clearMediaUploads,
    onImageFileChange: (event: Event) => readUpload(event, 'image'),
    onAudioFileChange: (event: Event) => readUpload(event, 'audio')
  }
}
