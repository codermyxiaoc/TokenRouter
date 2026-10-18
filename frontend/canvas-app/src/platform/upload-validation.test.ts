// @vitest-environment jsdom
import {describe,expect,it} from 'vitest';
import {validateUploadFiles} from './upload-validation';

describe('上传前整批参数校验',()=>{
  it.each(['image/png','image/jpeg','image/webp','video/mp4','video/webm','audio/mpeg','audio/wav','audio/ogg'])('接受服务端支持的 %s 素材',type=>{
    expect(()=>validateUploadFiles([new File(['media'],'asset',{type})],1024)).not.toThrow();
  });
  it('浏览器未提供 MIME 时只接受已知扩展名，不接受脚本或 SVG',()=>{
    expect(()=>validateUploadFiles([new File(['media'],'PHOTO.JPEG')],1024)).not.toThrow();
    for(const file of [new File(['x'],'picture.svg',{type:'image/svg+xml'}),new File(['x'],'script.js'),new File(['x'],'photo.png',{type:'text/html'})])expect(()=>validateUploadFiles([file],1024)).toThrow('素材格式不支持');
  });
  it('空文件、整批大小越界及图片独立大小上限先拒绝',()=>{
    const good=new File(['valid'],'a.png',{type:'image/png'});
    expect(()=>validateUploadFiles([good,new File([],'empty.png',{type:'image/png'})],1024)).toThrow('为空');
    expect(()=>validateUploadFiles([good,new File(['too large'],'b.png',{type:'image/png'})],5)).toThrow('服务器允许的大小');
    const large=new File(['placeholder'],'large.png',{type:'image/png'});Object.defineProperty(large,'size',{value:32*1024*1024+1});
    expect(()=>validateUploadFiles([large],64*1024*1024)).toThrow('32 MiB');
  });
  it('参考图和替换素材只接受目标允许的类型',()=>{
    const image=new File(['image'],'a.png',{type:'image/png'}),video=new File(['video'],'b.mp4',{type:'video/mp4'});
    expect(()=>validateUploadFiles([image,video],1024,{mode:'reference',targetType:'video'})).toThrow('参考素材请选择图片');
    expect(()=>validateUploadFiles([video],1024,{mode:'replace',targetType:'image'})).toThrow('当前节点不一致');
    expect(()=>validateUploadFiles([video],1024,{mode:'replace',targetType:'video'})).not.toThrow();
  });
});
