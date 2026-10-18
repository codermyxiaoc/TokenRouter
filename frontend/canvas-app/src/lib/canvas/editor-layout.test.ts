import {describe,it,expect} from 'vitest';
import {composerFootprint,focusNodeLayout} from './editor-layout';

describe('画布聚焦布局',()=>{
  for(const size of [{width:1400,height:900},{width:1086,height:712},{width:800,height:544},{width:600,height:500}]){
    it(`${size.width}×${size.height} 保留节点工具条与底部操作空间`,()=>{
      const node={position:{x:1900,y:-300},width:420,height:236};
      const below=composerFootprint(size.height,84);
      const v=focusNodeLayout(node,size,below);
      expect(v.y+node.position.y*v.k-62).toBeGreaterThanOrEqual(0);
      expect(v.y+(node.position.y+node.height)*v.k+below).toBeLessThanOrEqual(size.height-100);
      expect(v.x+(node.position.x+node.width/2)*v.k).toBe(size.width/2);
    });
  }
});
