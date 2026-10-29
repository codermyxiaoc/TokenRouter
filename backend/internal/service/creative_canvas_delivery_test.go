//go:build unit

package service

import (
	"bytes"
	"context"
	"errors"
	"io"
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
	missing bool
}

type nopReadSeekCloser struct{ *bytes.Reader }

func (nopReadSeekCloser) Close() error { return nil }

func (s *canvasDeliveryStore) Open(context.Context, string) (io.ReadSeekCloser, error) {
	if s.missing {
		return nil, errors.New("file missing")
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
