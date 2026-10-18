package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/google/uuid"
)

// 私有目录不挂静态路由；文件名只能由规范 UUID 生成，禁止用户路径和链接穿透。
type CreativeCanvasLocalStore struct{ root string }

func NewCreativeCanvasLocalStore(dataDir string) (*CreativeCanvasLocalStore, error) {
	if strings.TrimSpace(dataDir) == "" {
		return nil, ErrCanvasUnavailable
	}
	root, err := filepath.Abs(filepath.Join(dataDir, "creative-canvas-assets"))
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	s := &CreativeCanvasLocalStore{root: root}
	if err = s.checkRoot(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *CreativeCanvasLocalStore) checkRoot() error {
	for p := s.root; p != ""; p = filepath.Dir(p) {
		info, err := os.Lstat(p)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return ErrCanvasUnavailable
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	return nil
}

func (s *CreativeCanvasLocalStore) path(id string) (string, error) {
	v, err := uuid.Parse(id)
	if err != nil || v.String() != id {
		return "", ErrCanvasInvalid
	}
	if err = s.checkRoot(); err != nil {
		return "", err
	}
	p := filepath.Join(s.root, id+".bin")
	info, err := os.Lstat(p)
	if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular()) {
		return "", ErrCanvasUnavailable
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	return p, nil
}

type canvasContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r canvasContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func (s *CreativeCanvasLocalStore) Put(ctx context.Context, id string, reader io.Reader, limit int64) (int64, string, error) {
	path, err := s.path(id)
	if err != nil {
		return 0, "", err
	}
	if limit <= 0 || limit > CreativeCanvasOutputLimit {
		return 0, "", ErrCanvasQuota
	}
	f, err := os.CreateTemp(s.root, ".upload-")
	if err != nil {
		return 0, "", err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(canvasContextReader{ctx, reader}, limit+1))
	if err != nil {
		return 0, "", err
	}
	if n == 0 || n > limit {
		return 0, "", ErrCanvasQuota
	}
	if err = f.Sync(); err != nil {
		return 0, "", err
	}
	if err = f.Close(); err != nil {
		return 0, "", err
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	if _, err = s.path(id); err != nil {
		return 0, "", err
	}
	// 多恢复器写同一产物只接受相同字节，绝不覆盖已经确认的不同结果。
	if existing, e := os.Open(path); e == nil {
		defer existing.Close()
		oldHash := sha256.New()
		oldN, e := io.Copy(oldHash, io.LimitReader(existing, limit+1))
		if e != nil {
			return 0, "", e
		}
		if oldN != n || hex.EncodeToString(oldHash.Sum(nil)) != digest {
			return 0, "", ErrCanvasConflict
		}
		return n, digest, nil
	} else if !errors.Is(e, os.ErrNotExist) {
		return 0, "", e
	}
	if err = os.Rename(tmp, path); err != nil {
		return 0, "", err
	}
	return n, digest, nil
}
func (s *CreativeCanvasLocalStore) Open(ctx context.Context, id string) (io.ReadSeekCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}
func (s *CreativeCanvasLocalStore) Delete(ctx context.Context, id string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.path(id)
	if err != nil {
		return err
	}
	err = os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
