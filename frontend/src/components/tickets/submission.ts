// 网络结果未知时，相同草稿复用键；修改正文或文件则视为新提交。
export function ticketSubmission() {
  let previousPayload = ''
  let previousFiles: File[] = []
  let key = ''
  return {
    key(payload: unknown, files: File[]): string {
      const serialized = JSON.stringify(payload)
      if (!key || serialized !== previousPayload || files.length !== previousFiles.length || files.some((file, index) => file !== previousFiles[index])) {
        key = Array.from(crypto.getRandomValues(new Uint8Array(16)), byte => byte.toString(16).padStart(2, '0')).join('')
        previousPayload = serialized
        previousFiles = [...files]
      }
      return key
    },
    reset() { key = ''; previousPayload = ''; previousFiles = [] },
  }
}
