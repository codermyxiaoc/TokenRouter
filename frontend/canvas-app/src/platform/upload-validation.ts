// 文件选择器的 accept 只是提示；上传前统一检查整批，实际文件签名仍由服务端验证。
const mediaMimes = new Map<string, 'image'|'video'|'audio'>([
  ['image/png','image'],['image/jpeg','image'],['image/webp','image'],
  ['video/mp4','video'],['video/webm','video'],
  ['audio/mpeg','audio'],['audio/wav','audio'],['audio/wave','audio'],['audio/x-wav','audio'],['audio/ogg','audio'],
]);
const extensionMimes:Record<string,string>={png:'image/png',jpg:'image/jpeg',jpeg:'image/jpeg',webp:'image/webp',mp4:'video/mp4',webm:'video/webm',mp3:'audio/mpeg',wav:'audio/wav',ogg:'audio/ogg'};
export const mediaUploadAccept=[...mediaMimes.keys()].join(',');

export function validateUploadFiles(files:readonly File[],maxBytes?:number,intent?:{mode:'reference'|'replace';targetType:string}) {
  for(const [index,file] of files.entries()) {
    const mime=file.type.toLowerCase()||extensionMimes[file.name.split('.').pop()?.toLowerCase()||''];
    const mediaType=mediaMimes.get(mime);
    if(!mediaType)throw new Error('素材格式不支持，请选择 PNG、JPEG、WebP、MP4、WebM、MP3、WAV 或 OGG');
    if(file.size<=0)throw new Error('素材文件为空，请重新选择');
    if(maxBytes&&file.size>maxBytes)throw new Error('素材超过服务器允许的大小');
    if(mediaType==='image'&&file.size>32*1024*1024)throw new Error('图片超过 32 MiB，请更换较小图片');
    if(intent?.mode==='reference'&&mediaType!=='image')throw new Error('参考素材请选择图片');
    if(intent?.mode==='replace'&&index===0&&mediaType!==intent.targetType)throw new Error('上传的素材类型与当前节点不一致');
  }
}
