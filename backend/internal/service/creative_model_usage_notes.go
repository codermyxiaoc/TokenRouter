package service

import (
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// CreativeModelUsageNotesMaxChars 按 Unicode 码点计数，使中文和普通表情与前端字符数一致。
const CreativeModelUsageNotesMaxChars = 2000

// 管理员说明按纯文本保存；统一换行、去除边缘空白，禁止不可显示的控制字符进入用户目录。
func normalizeCreativeModelUsageNotes(raw, mediaType string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", fmt.Errorf("creative usage_notes must be valid UTF-8 text")
	}
	notes := strings.ReplaceAll(strings.ReplaceAll(raw, "\r\n", "\n"), "\r", "\n")
	for _, r := range notes {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return "", fmt.Errorf("creative usage_notes contains unsupported control characters")
		}
	}
	notes = strings.TrimSpace(notes)
	if notes == "" {
		return "", nil
	}
	if mediaType != "video" {
		return "", fmt.Errorf("creative usage_notes is only supported for video models")
	}
	if utf8.RuneCountInString(notes) > CreativeModelUsageNotesMaxChars {
		return "", fmt.Errorf("creative usage_notes must contain at most %d characters", CreativeModelUsageNotesMaxChars)
	}
	return notes, nil
}
