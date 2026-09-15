package service

import (
	"path"
	"strings"
)

// PreviewTicketAttachment 仅在调用方鉴权后使用，历史文件也必须通过当前内容安全校验。
// @project-doc docs/domains/support_tickets.md#ticket_attachments
func PreviewTicketAttachment(file *TicketAttachment) (*TicketAttachmentUpload, error) {
	if file == nil || len(file.Data) == 0 || len(file.Data) > 20<<20 {
		return nil, ticketAttachmentInvalid("附件为空或超过预览大小限制")
	}
	name := sanitizeTicketFilename(file.Filename)
	if err := validateTicketFilename(name); err != nil {
		return nil, ticketAttachmentInvalid(err.Error())
	}
	ext := strings.ToLower(path.Ext(name))
	if ext == ".doc" || ext == ".docx" {
		expectedMIME := "application/msword"
		if ext == ".docx" {
			expectedMIME = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
		}
		if file.ContentType != expectedMIME {
			return nil, ticketAttachmentInvalid("附件格式与内容类型不一致")
		}
		text, err := PreviewTicketWord(file.Data, expectedMIME)
		if err != nil {
			return nil, err
		}
		// Word 只返回正文文本，绝不把文档中的标记当作站点 HTML 执行。
		return &TicketAttachmentUpload{Name: name + ".txt", ContentType: "text/plain; charset=utf-8", Data: []byte(text)}, nil
	}
	// 使用格式硬上限而非当前上传设置，管理员调低上传数量或大小不影响历史预览。
	files, err := ValidateTicketAttachments([]TicketAttachmentUpload{{Name: name, Data: file.Data}}, &TicketConfig{
		MaxAttachments: 1, MaxAttachmentSizeMB: 20,
	})
	if err != nil {
		return nil, err
	}
	preview := files[0]
	// WebP 的安全重编码会改为 PNG；其他历史元数据必须与实际格式一致。
	if preview.ContentType != file.ContentType && !(ext == ".webp" && file.ContentType == "image/webp" && preview.ContentType == "image/png") {
		return nil, ticketAttachmentInvalid("附件格式与内容类型不一致")
	}
	return &preview, nil
}
