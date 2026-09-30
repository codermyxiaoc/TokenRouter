package service

import (
	"archive/zip"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// updateReleaseCache 同时标记仓库和格式，旧实例共享 Redis 时不能污染新来源的更新结果。
type updateReleaseCache struct {
	Source      string       `json:"source"`
	Schema      int          `json:"schema"`
	Latest      string       `json:"latest"`
	ReleaseInfo *ReleaseInfo `json:"release_info"`
	Timestamp   int64        `json:"timestamp"`
}

type updateVersion struct {
	product [3]int
	base    [3]int
	fork    bool
}

// @project-doc docs/operations/deployment_and_migrations.md#application_update_source
// parseUpdateVersion 保留完整发布标签；ct 后的数字是本项目版本，不是 SemVer 预发布号。
func parseUpdateVersion(raw string) (string, updateVersion, bool) {
	version := strings.TrimPrefix(strings.TrimSpace(raw), "v")
	var parsed updateVersion
	parts := strings.Split(version, "-ct-v")
	if len(parts) == 2 {
		var ok bool
		parsed.base, ok = parseUpdateNumbers(parts[0], 3)
		if !ok {
			return "", parsed, false
		}
		parsed.product, ok = parseUpdateNumbers(parts[1], 2)
		parsed.fork = true
		return version, parsed, ok
	}
	if len(parts) != 1 {
		return "", parsed, false
	}
	var ok bool
	parsed.product, ok = parseUpdateNumbers(version, 2)
	return version, parsed, ok
}

// parseUpdateNumbers 只接收两段或三段十进制版本，阻止预发布号及命令字符进入更新与回退命令。
func parseUpdateNumbers(raw string, minParts int) ([3]int, bool) {
	var result [3]int
	parts := strings.Split(raw, ".")
	if len(parts) < minParts || len(parts) > 3 {
		return result, false
	}
	for i, part := range parts {
		if part == "" || (len(part) > 1 && part[0] == '0') {
			return result, false
		}
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				return result, false
			}
		}
		value, err := strconv.Atoi(part)
		if err != nil {
			return result, false
		}
		result[i] = value
	}
	return result, true
}

func normalizeRollbackVersion(raw string) (string, bool) {
	version, _, valid := parseUpdateVersion(raw)
	return version, valid
}

// compareVersions 优先比较项目版本；两个 fork 标签产品版本相同时，再比较上游基线。
// 无法识别的本地开发版本保持不自动升级，不能猜测它比所有正式版本更旧。
func compareVersions(current, latest string) int {
	_, left, leftOK := parseUpdateVersion(current)
	_, right, rightOK := parseUpdateVersion(latest)
	if !leftOK || !rightOK {
		return 0
	}
	if compared := compareUpdateNumbers(left.product, right.product); compared != 0 {
		return compared
	}
	if left.fork && right.fork {
		return compareUpdateNumbers(left.base, right.base)
	}
	return 0
}

func compareUpdateNumbers(left, right [3]int) int {
	for i := range left {
		if left[i] < right[i] {
			return -1
		}
		if left[i] > right[i] {
			return 1
		}
	}
	return 0
}

// updateGitHubURL 只信任本项目的 GitHub HTTPS 路径；CDN 地址由 GitHub 自己重定向产生。
func updateGitHubURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	return u, err == nil && u.Scheme == "https" && strings.EqualFold(u.Host, "github.com") &&
		u.User == nil && u.RawQuery == "" && u.Fragment == "" && u.RawPath == ""
}

func validUpdateReleaseURL(raw, version string) bool {
	// GitHub API 已按固定仓库调用；缺失可选 HTML 链接时无需构造外部地址。
	if raw == "" {
		return true
	}
	u, valid := updateGitHubURL(raw)
	if !valid {
		return false
	}
	prefix := "/" + githubRepo + "/releases/tag/"
	if !strings.HasPrefix(u.Path, prefix) {
		return false
	}
	tag, valid := normalizeRollbackVersion(strings.TrimPrefix(u.Path, prefix))
	return valid && tag == version
}

func validUpdateAssetURL(raw, version, name string) bool {
	u, valid := updateGitHubURL(raw)
	if !valid {
		return false
	}
	prefix := "/" + githubRepo + "/releases/download/"
	if !strings.HasPrefix(u.Path, prefix) {
		return false
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, prefix), "/")
	if len(parts) != 2 || parts[1] != name {
		return false
	}
	tag, valid := normalizeRollbackVersion(parts[0])
	return valid && tag == version
}

// selectUpdateAssets 同时支持手工发布的带 v 归档及 GoReleaser 的不带 v 归档。
// 归档、校验文件都须来自选定版本，缺少平台资产或校验时在任何文件写入前失败。
func selectUpdateAssets(version string, assets []Asset, goos, goarch string) (Asset, Asset, error) {
	normalized, valid := normalizeRollbackVersion(version)
	if !valid {
		return Asset{}, Asset{}, fmt.Errorf("invalid release version")
	}
	extension := ".tar.gz"
	if goos == "windows" {
		extension = ".zip"
	}
	for _, prefix := range []string{"sub2api_v", "sub2api_"} {
		name := prefix + normalized + "_" + goos + "_" + goarch + extension
		for _, archive := range assets {
			if archive.Name != name {
				continue
			}
			if !validUpdateAssetURL(archive.DownloadURL, normalized, archive.Name) {
				return Asset{}, Asset{}, fmt.Errorf("release archive is outside the configured TokenRouter source")
			}
			for _, checksumName := range []string{name + ".sha256", "checksums.txt"} {
				for _, checksum := range assets {
					if checksum.Name == checksumName {
						if !validUpdateAssetURL(checksum.DownloadURL, normalized, checksum.Name) {
							return Asset{}, Asset{}, fmt.Errorf("release checksum is outside the configured TokenRouter source")
						}
						return archive, checksum, nil
					}
				}
			}
			return Asset{}, Asset{}, fmt.Errorf("release checksum is missing for %s", name)
		}
	}
	return Asset{}, Asset{}, fmt.Errorf("no compatible release found for %s/%s", goos, goarch)
}

// extractUpdateZIP 只写出包内的普通程序文件，并保留与 tar 包相同的大小、路径限制。
func extractUpdateZIP(archivePath, destPath string) error {
	reader, err := zip.OpenReader(archivePath)
	if err != nil {
		return err
	}
	defer func() { _ = reader.Close() }()
	for _, entry := range reader.File {
		if strings.Contains(entry.Name, "..") {
			return fmt.Errorf("path traversal attempt detected: %s", entry.Name)
		}
		name := filepath.Base(entry.Name)
		if (name != "sub2api.exe" && name != "sub2api") || !entry.Mode().IsRegular() {
			continue
		}
		if entry.UncompressedSize64 > maxDownloadSize {
			return fmt.Errorf("binary too large: %d bytes", entry.UncompressedSize64)
		}
		input, err := entry.Open()
		if err != nil {
			return err
		}
		defer func() { _ = input.Close() }()
		output, err := os.Create(destPath)
		if err != nil {
			return err
		}
		written, copyErr := io.Copy(output, io.LimitReader(input, maxDownloadSize+1))
		closeErr := output.Close()
		if copyErr != nil {
			return copyErr
		}
		if written > maxDownloadSize {
			return fmt.Errorf("binary exceeds maximum size")
		}
		return closeErr
	}
	return fmt.Errorf("binary not found in archive")
}
