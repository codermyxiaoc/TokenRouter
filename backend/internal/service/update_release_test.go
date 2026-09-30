//go:build unit

package service

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 版本比较必须跟随本 fork 的产品版本，不能把 ct 后缀截掉或按字符串排序。
func TestUpdateVersionForkOrdering(t *testing.T) {
	for _, tc := range []struct {
		current, latest string
		want            int
	}{
		{"v0.1.278-ct-v2.5", "v0.1.278-ct-v2.6", -1},
		{"0.1.278-ct-v2.9", "0.1.278-ct-v2.10", -1},
		{"0.1.278-ct-v2.5", "0.1.278-ct-v1.8", 1},
		{"0.1.279-ct-v2.4", "0.1.278-ct-v2.5", -1},
		{"0.1.278-ct-v2.5", "0.1.279-ct-v2.5", -1},
		{"0.1.278-ct-v2.5", "v2.6", -1},
		{"v2.6", "0.1.278-ct-v2.6", 0},
		{"0.1.278-ct-v2.6", "2.6.1", -1},
		{"0.1.147", "0.1.148", -1},
		{"dev", "2.6", 0},
	} {
		t.Run(tc.current+"_to_"+tc.latest, func(t *testing.T) {
			require.Equal(t, tc.want, compareVersions(tc.current, tc.latest))
			require.Equal(t, -tc.want, compareVersions(tc.latest, tc.current))
		})
	}
	for _, version := range []string{"v0.1.278-ct-v2.06", "v0.1.278-ct-v2.6-rc1", "2.6;id", "2.6+meta", "2", "0.1.278-ct-v2.6/other", "999999999999999999999.1"} {
		_, valid := normalizeRollbackVersion(version)
		require.False(t, valid, version)
	}
}

// 正常检查和回退都只访问本项目，公开 latest 早于本地版本时不能诱导降级。
func TestUpdateServiceUsesForkSourceAndVersions(t *testing.T) {
	client := &updateServiceGitHubClientStub{release: &GitHubRelease{TagName: "v0.1.278-ct-v1.8"}}
	svc := NewUpdateService(&updateServiceCacheStub{}, client, "0.1.278-ct-v2.5", "release")
	info, err := svc.CheckUpdate(context.Background(), true)
	require.NoError(t, err)
	require.Equal(t, "codermyxiaoc/TokenRouter", client.latestRepo)
	require.False(t, info.HasUpdate)
	client.release.TagName = "v0.1.278-ct-v2.6"
	info, err = svc.CheckUpdate(context.Background(), true)
	require.NoError(t, err)
	require.True(t, info.HasUpdate)
	require.Equal(t, "0.1.278-ct-v2.6", info.LatestVersion)
	client.recentReleases = []*GitHubRelease{{TagName: "v0.1.278-ct-v2.6"}, {TagName: "v0.1.278-ct-v2.4"}, {TagName: "v0.1.278-ct-v2.3"}}
	versions, err := svc.ListRollbackVersions(context.Background())
	require.NoError(t, err)
	require.Equal(t, "codermyxiaoc/TokenRouter", client.recentRepo)
	require.Equal(t, []RollbackVersion{{Version: "0.1.278-ct-v2.4"}, {Version: "0.1.278-ct-v2.3"}}, versions)
}

// API 出错、空响应、预发布及跨来源链接都只能返回无可用升级，不能进入下载步骤。
func TestUpdateServiceRejectsInvalidReleaseResponse(t *testing.T) {
	for _, release := range []*GitHubRelease{
		nil,
		{TagName: "v0.1.278-ct-v2.6", Draft: true},
		{TagName: "v0.1.278-ct-v2.6", Prerelease: true},
		{TagName: "v0.1.278-ct-v2.6-rc1"},
		{TagName: "v0.1.278-ct-v2.6", HTMLURL: "https://github.com/Wei-Shaw/sub2api/releases/tag/v0.1.278-ct-v2.6"},
	} {
		svc := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{release: release}, "0.1.278-ct-v2.5", "release")
		info, err := svc.CheckUpdate(context.Background(), true)
		require.NoError(t, err)
		require.False(t, info.HasUpdate)
		require.NotEmpty(t, info.Warning)
		require.ErrorIs(t, svc.PerformUpdate(context.Background()), ErrNoUpdateAvailable)
	}
}

// 同来源缓存可在网络失败时兜底；旧无来源、其它仓库和伪造外链均须重新查询且不能回退使用。
func TestUpdateServiceCacheSourceIsolation(t *testing.T) {
	ctx := context.Background()
	for _, source := range []string{"", "TokenFlux/TokenRouter", "Wei-Shaw/sub2api", githubRepo} {
		t.Run(source, func(t *testing.T) {
			data, err := json.Marshal(updateReleaseCache{Source: source, Schema: updateCacheSchema, Latest: "0.1.278-ct-v9.9", Timestamp: time.Now().Unix(), ReleaseInfo: &ReleaseInfo{}})
			require.NoError(t, err)
			client := &updateServiceGitHubClientStub{latestErr: errors.New("offline")}
			svc := NewUpdateService(&updateServiceCacheStub{data: string(data)}, client, "0.1.278-ct-v2.5", "release")
			info, err := svc.CheckUpdate(ctx, false)
			require.NoError(t, err)
			if source == githubRepo {
				require.True(t, info.Cached)
				require.True(t, info.HasUpdate)
				require.Empty(t, client.latestRepo)
			} else {
				require.False(t, info.Cached)
				require.False(t, info.HasUpdate)
				require.Equal(t, githubRepo, client.latestRepo)
				require.Equal(t, "offline", info.Warning)
			}
		})
	}
	cache := &updateServiceCacheStub{}
	client := &updateServiceGitHubClientStub{release: &GitHubRelease{TagName: "v0.1.278-ct-v2.6"}}
	svc := NewUpdateService(cache, client, "0.1.278-ct-v2.5", "release")
	_, err := svc.CheckUpdate(ctx, true)
	require.NoError(t, err)
	client.latestErr = errors.New("offline")
	info, err := svc.CheckUpdate(ctx, true)
	require.NoError(t, err)
	require.True(t, info.Cached)
	require.Contains(t, info.Warning, "offline")
	var cached updateReleaseCache
	require.NoError(t, json.Unmarshal([]byte(cache.data), &cached))
	cached.ReleaseInfo.HTMLURL = "https://github.com/Wei-Shaw/sub2api/releases/tag/v0.1.278-ct-v2.6"
	data, err := json.Marshal(cached)
	require.NoError(t, err)
	cache.data = string(data)
	info, err = svc.CheckUpdate(ctx, false)
	require.NoError(t, err)
	require.False(t, info.Cached)
	require.False(t, info.HasUpdate)
}

func updateTestAsset(version, name string) Asset {
	return Asset{Name: name, DownloadURL: "https://github.com/" + githubRepo + "/releases/download/v" + version + "/" + name}
}

// 已发布包使用带 v 文件名与独立校验，GoReleaser 使用不带 v 文件名和汇总校验。
func TestUpdateAssetsSelectPublishedLayouts(t *testing.T) {
	version := "0.1.278-ct-v2.6"
	for _, tc := range []struct{ prefix, os, extension, checksum string }{
		{"sub2api_v", "linux", ".tar.gz", ".sha256"},
		{"sub2api_", "linux", ".tar.gz", "checksums.txt"},
		{"sub2api_", "windows", ".zip", "checksums.txt"},
	} {
		t.Run(tc.os+tc.prefix, func(t *testing.T) {
			name := tc.prefix + version + "_" + tc.os + "_amd64" + tc.extension
			checksumName := tc.checksum
			if checksumName == ".sha256" {
				checksumName = name + checksumName
			}
			assets := []Asset{updateTestAsset(version, name), updateTestAsset(version, checksumName), updateTestAsset(version, name+".sig")}
			archive, checksum, err := selectUpdateAssets(version, assets, tc.os, "amd64")
			require.NoError(t, err)
			require.Equal(t, name, archive.Name)
			require.Equal(t, checksumName, checksum.Name)
		})
	}
}

// 任何跨仓库、跨版本或只有校验文件的响应都必须在下载与替换当前程序之前被拒绝。
func TestUpdateAssetsRejectForeignOrUnverifiedDownloads(t *testing.T) {
	version := "0.1.278-ct-v2.6"
	extension := ".tar.gz"
	if runtime.GOOS == "windows" {
		extension = ".zip"
	}
	name := "sub2api_v" + version + "_" + runtime.GOOS + "_" + runtime.GOARCH + extension
	archive, checksum := updateTestAsset(version, name), updateTestAsset(version, name+".sha256")
	for _, foreign := range []string{
		strings.Replace(archive.DownloadURL, githubRepo, "Wei-Shaw/sub2api", 1),
		strings.Replace(archive.DownloadURL, githubRepo, "TokenFlux/TokenRouter", 1),
		strings.Replace(archive.DownloadURL, "github.com/", "github.com.evil.invalid/", 1),
		strings.Replace(archive.DownloadURL, "https://", "http://", 1),
		strings.Replace(archive.DownloadURL, "https://", "https://user@", 1),
		strings.Replace(archive.DownloadURL, "/download/v"+version+"/", "/download/v2.4/", 1),
		archive.DownloadURL + "?redirect=other",
		"https://objects.githubusercontent.com/foreign/package.tar.gz",
	} {
		bad := archive
		bad.DownloadURL = foreign
		_, _, err := selectUpdateAssets(version, []Asset{bad, checksum}, runtime.GOOS, runtime.GOARCH)
		require.Error(t, err, foreign)
	}
	for _, assets := range [][]Asset{{checksum}, {archive}, {archive, updateTestAsset("2.4", checksum.Name)}} {
		svc := NewUpdateService(&updateServiceCacheStub{}, &updateServiceGitHubClientStub{}, "0.1.278-ct-v2.5", "release")
		require.Error(t, svc.applyReleaseAssets(context.Background(), version, assets))
	}
}

// 校验文件名称兼容 sha256sum 普通/二进制格式，但始终要求当前归档的完整文件名和正确摘要。
type updateChecksumClient struct {
	updateServiceGitHubClientStub
	checksum []byte
}

func (c *updateChecksumClient) FetchChecksumFile(context.Context, string) ([]byte, error) {
	return c.checksum, nil
}

func TestUpdateReleaseChecksumAndArchiveExtraction(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	content := []byte("TokenRouter test binary")
	file := filepath.Join(root, "sub2api_v0.1.278-ct-v2.6_linux_amd64.tar.gz")
	require.NoError(t, os.WriteFile(file, content, 0600))
	sum := sha256.Sum256(content)
	client := &updateChecksumClient{}
	svc := NewUpdateService(&updateServiceCacheStub{}, client, "0.1.278-ct-v2.5", "release")
	for _, marker := range []string{"", "*"} {
		client.checksum = []byte(fmt.Sprintf("%s  %s%s\n", hex.EncodeToString(sum[:]), marker, filepath.Base(file)))
		require.NoError(t, svc.verifyChecksum(ctx, file, "unused"))
	}
	client.checksum = []byte(strings.Repeat("0", 64) + "  " + filepath.Base(file))
	require.ErrorContains(t, svc.verifyChecksum(ctx, file, "unused"), "checksum mismatch")
	client.checksum = []byte(hex.EncodeToString(sum[:]) + "  other.tar.gz")
	require.ErrorContains(t, svc.verifyChecksum(ctx, file, "unused"), "checksum not found")
	for _, zipArchive := range []bool{false, true} {
		for _, prefix := range []string{"", "sub2api_v0.1.278-ct-v2.6_linux_amd64/", "../"} {
			t.Run(fmt.Sprintf("zip_%t/prefix_%s", zipArchive, prefix), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "package.tar.gz")
				if zipArchive {
					path = strings.TrimSuffix(path, ".tar.gz") + ".zip"
				}
				out, err := os.Create(path)
				require.NoError(t, err)
				if zipArchive {
					zw := zip.NewWriter(out)
					entry, err := zw.Create(prefix + "sub2api.exe")
					require.NoError(t, err)
					_, err = entry.Write(content)
					require.NoError(t, err)
					require.NoError(t, zw.Close())
				} else {
					gz := gzip.NewWriter(out)
					tw := tar.NewWriter(gz)
					require.NoError(t, tw.WriteHeader(&tar.Header{Name: prefix + "sub2api", Mode: 0755, Size: int64(len(content))}))
					_, err := tw.Write(content)
					require.NoError(t, err)
					require.NoError(t, tw.Close())
					require.NoError(t, gz.Close())
				}
				require.NoError(t, out.Close())
				dest := filepath.Join(t.TempDir(), "binary")
				err = svc.extractBinary(path, dest)
				if prefix == "../" {
					require.ErrorContains(t, err, "path traversal")
					return
				}
				require.NoError(t, err)
				got, err := os.ReadFile(dest)
				require.NoError(t, err)
				require.Equal(t, content, got)
			})
		}
	}
}
