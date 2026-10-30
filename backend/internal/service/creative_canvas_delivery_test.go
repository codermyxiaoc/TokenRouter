//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// canvasDeliveryRepo 只提供交付判定所需的执行与素材读取。
type canvasDeliveryRepo struct {
	CreativeCanvasRepository
	asset *CreativeCanvasAsset
}

func (r *canvasDeliveryRepo) FindExecutionByTaskID(context.Context, string) (*CreativeCanvasExecution, error) {
	return &CreativeCanvasExecution{ID: "exec", UserID: 7, Kind: "image"}, nil
}

func (r *canvasDeliveryRepo) GetAsset(context.Context, int64, string) (*CreativeCanvasAsset, error) {
	if r.asset == nil {
		return nil, ErrCanvasNotFound
	}
	copy := *r.asset
	return &copy, nil
}

// canvasDeliveryStore 模拟文件已丢失或可读。
type canvasDeliveryStore struct {
	CreativeCanvasAssetStore
	missing  bool
	notExist bool
}

type nopReadSeekCloser struct{ *bytes.Reader }

func (nopReadSeekCloser) Close() error { return nil }

func (s *canvasDeliveryStore) Open(context.Context, string) (io.ReadSeekCloser, error) {
	if s.missing {
		return nil, errors.New("file missing")
	}
	if s.notExist {
		return nil, fs.ErrNotExist
	}
	return nopReadSeekCloser{bytes.NewReader([]byte("png"))}, nil
}

// P2-01：首次结算前素材已过期或文件缺失时不能判定已交付；只有用户确实能取回才算交付。
func TestCreativeCanvasDeliveryRequiresReadableAsset(t *testing.T) {
	future, past := time.Now().Add(time.Hour), time.Now().Add(-time.Minute)
	for _, tc := range []struct {
		name      string
		asset     *CreativeCanvasAsset
		missing   bool
		delivered bool
	}{
		{"ready_and_readable", &CreativeCanvasAsset{ID: "a", Status: "ready", ExpiresAt: future}, false, true},
		{"expired_before_settlement", &CreativeCanvasAsset{ID: "a", Status: "ready", ExpiresAt: past}, false, false},
		{"file_missing", &CreativeCanvasAsset{ID: "a", Status: "ready", ExpiresAt: future}, true, false},
		{"not_ready", &CreativeCanvasAsset{ID: "a", Status: "pending", ExpiresAt: future}, false, false},
		{"no_record", nil, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewCreativeCanvasService(&canvasDeliveryRepo{asset: tc.asset}, &canvasDeliveryStore{missing: tc.missing}, nil)
			managed, delivered, err := svc.CreativeOutputDelivered(context.Background(), "run", 0)
			require.NoError(t, err)
			require.True(t, managed)
			require.Equal(t, tc.delivered, delivered)
		})
	}
}

// 参考视频不读入内存，但提交前要确认私有文件仍在且大小与登记一致，不能等签发链接、提交上游后才失败。
func TestCreativeCanvasLoadInputsChecksReferenceVideoFile(t *testing.T) {
	future := time.Now().Add(time.Hour)
	execution := &CreativeCanvasExecution{UserID: 7, Params: CreativeCanvasExecutionParams{References: []CreativeCanvasReference{{AssetID: "v", Role: VideoMediaRoleReferenceVideo}}}}
	video := func(size int64) *CreativeCanvasAsset {
		return &CreativeCanvasAsset{ID: "v", UserID: 7, MediaType: "video", MIMEType: "video/mp4", Status: "ready", ByteSize: size, ExpiresAt: future}
	}
	// 测试存储里的文件内容为 3 字节。
	svc := NewCreativeCanvasService(&canvasDeliveryRepo{asset: video(3)}, &canvasDeliveryStore{}, nil)
	assets, err := svc.loadInputs(context.Background(), execution)
	require.NoError(t, err)
	require.Len(t, assets, 1)
	require.Nil(t, assets[0].Content, "参考视频不读入内存")
	_, err = NewCreativeCanvasService(&canvasDeliveryRepo{asset: video(3)}, &canvasDeliveryStore{notExist: true}, nil).loadInputs(context.Background(), execution)
	require.ErrorIs(t, err, ErrCanvasNotFound)
	_, err = NewCreativeCanvasService(&canvasDeliveryRepo{asset: video(4)}, &canvasDeliveryStore{}, nil).loadInputs(context.Background(), execution)
	require.ErrorIs(t, err, ErrCanvasInvalid)
}
