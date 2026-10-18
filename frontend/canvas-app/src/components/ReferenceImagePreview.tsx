import { useEffect, useState } from 'react';
import { Modal } from 'antd';
import { LoaderCircle } from 'lucide-react';
import { ApiError, assetBlob, isAssetExpired } from '@/platform/api';
import { getEpoch, onSessionClear } from '@/platform/session';
import { AssetView } from './AssetView';
import './reference-image-preview.css';

type Props = { assetId?: string; title: string; index: number; expiresAt?: string };
type Preview = { assetId: string; url?: string; error?: string };

// 预览仅读取已有私有素材，不改变参考连接、提交意图或计费状态。
export function ReferenceImagePreview({ assetId, title, index, expiresAt }: Props) {
  const [openedId, setOpenedId] = useState<string>();
  const [preview, setPreview] = useState<Preview>();
  const open = !!assetId && openedId === assetId;
  const close = () => setOpenedId(undefined);
  const expired = isAssetExpired({ expires_at: expiresAt || '' });

  useEffect(() => { setOpenedId(undefined); }, [assetId]);
  useEffect(() => onSessionClear(() => setOpenedId(undefined)), []);
  useEffect(() => {
    let live = true;
    let ownedURL = '';
    setPreview(undefined);
    if (!open || !assetId) return;
    if (expired) {
      setPreview({ assetId, error: '参考图已过期，请重新上传' });
      return;
    }
    const epoch = getEpoch();
    // 每次打开重新鉴权读取，不能直接复用缩略图缓存而绕过过期或删除检查。
    void assetBlob(assetId).then(blob => {
      if (!live || getEpoch() !== epoch) return;
      if (!blob.size || !/^image\/(png|jpeg|webp)$/i.test(blob.type)) throw new Error('invalid image');
      ownedURL = URL.createObjectURL(blob);
      setPreview({ assetId, url: ownedURL });
    }).catch(error => {
      if (!live || getEpoch() !== epoch || error?.name === 'AbortError') return;
      const message = error instanceof ApiError && error.status === 410 ? '参考图已过期，请重新上传'
        : error instanceof ApiError && error.status === 401 ? '登录会话已失效，请重新登录'
        : error instanceof ApiError && [403, 404].includes(error.status) ? '参考图不存在或没有访问权限'
        : '参考图加载失败，请关闭后重新打开';
      setPreview({ assetId, error: message });
    });
    return () => {
      live = false;
      // 预览 URL 独立持有，关闭、换图或退出会话时撤销，不影响画布缩略图缓存。
      if (ownedURL) URL.revokeObjectURL(ownedURL);
    };
  }, [open, assetId, expired]);

  const current = preview?.assetId === assetId ? preview : undefined;
  return <>
    <button type="button" className="reference-thumb reference-image-trigger" disabled={!assetId}
      aria-label={`预览参考图 ${index + 1}：${title}`} title={assetId ? '点击预览参考图' : '该参考节点暂无素材'}
      onPointerDown={event => event.stopPropagation()} onMouseDown={event => event.stopPropagation()}
      onKeyDown={event => event.stopPropagation()} onDoubleClick={event => event.stopPropagation()}
      onClick={event => { event.stopPropagation(); setOpenedId(assetId); }}>
      <AssetView id={assetId} expired={expired}/>
    </button>
    <Modal title="参考图预览" open={open} onCancel={close} width={900} centered destroyOnHidden
      rootClassName="canvas-reference-preview" transitionName="" maskTransitionName=""
      footer={<button type="button" aria-label="关闭参考预览" onClick={close}>关闭</button>}>
      <div className="reference-preview-media" data-canvas-no-zoom onPointerDown={event => event.stopPropagation()}>
        {current?.error ? <p role="alert">{current.error}</p>
          : current?.url ? <img src={current.url} alt={title || '参考图'} draggable={false}/>
          : <div role="status" aria-label="正在加载参考图"><LoaderCircle className="animate-spin"/><span>正在加载参考图…</span></div>}
      </div>
    </Modal>
  </>;
}
