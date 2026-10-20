package service

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
)

const (
	imageSourceDownloadFailed = "image_source_download_failed"
	imageResultTooLarge       = "image_result_too_large"
	imageResultInvalid        = "image_result_invalid"
	imageResultTimeout        = "image_result_timeout"
	imageStorageAccessDenied  = "image_storage_access_denied"
	imageStorageBucketMissing = "image_storage_bucket_missing"
	imageStorageTimeout       = "image_storage_timeout"
	imageStorageUploadFailed  = "image_storage_upload_failed"
)

// imageStorageFailure 保留错误链供重试与排障；向任务及日志输出时必须使用固定分类信息。
type imageStorageFailure struct {
	stage string
	code  string
	cause error
}

func (e *imageStorageFailure) Error() string { return e.cause.Error() }
func (e *imageStorageFailure) Unwrap() error { return e.cause }

func classifyImageStorageFailure(stage, code string, err error) error {
	if err == nil {
		return nil
	}
	var failure *imageStorageFailure
	if errors.As(err, &failure) {
		return err
	}
	return &imageStorageFailure{stage: stage, code: code, cause: err}
}

// ImageStorageFailureDetails 仅输出允许公开的分类，不回显下载地址、签名、SDK 错误正文或凭据。
// 未包装错误来自存储预检或上传，因此只按结构化 S3 代码、HTTP 状态与超时接口分类。
func ImageStorageFailureDetails(err error) (stage, code, message string) {
	stage, code = "upload", imageStorageUploadFailed
	var failure *imageStorageFailure
	if errors.As(err, &failure) {
		stage, code = failure.stage, failure.code
	}
	if stage == "upload" {
		code = classifyImageUploadFailure(err)
	}
	message = imageStorageFailureMessage(code)
	return
}

func classifyImageUploadFailure(err error) string {
	var apiError interface{ ErrorCode() string }
	if errors.As(err, &apiError) {
		switch apiError.ErrorCode() {
		case "NoSuchBucket":
			return imageStorageBucketMissing
		case "AccessDenied", "AccessDeniedException", "InvalidAccessKeyId", "SignatureDoesNotMatch", "ExpiredToken", "InvalidToken", "TokenRefreshRequired":
			return imageStorageAccessDenied
		case "RequestTimeout", "RequestTimeoutException":
			return imageStorageTimeout
		}
	}
	var statusError interface{ HTTPStatusCode() int }
	if errors.As(err, &statusError) {
		switch statusError.HTTPStatusCode() {
		case http.StatusForbidden, http.StatusUnauthorized:
			return imageStorageAccessDenied
		case http.StatusRequestTimeout, http.StatusGatewayTimeout:
			return imageStorageTimeout
		}
	}
	var alternateStatus interface{ StatusCode() int }
	if errors.As(err, &alternateStatus) {
		switch alternateStatus.StatusCode() {
		case http.StatusForbidden, http.StatusUnauthorized:
			return imageStorageAccessDenied
		case http.StatusRequestTimeout, http.StatusGatewayTimeout:
			return imageStorageTimeout
		}
	}
	var timeout net.Error
	if errors.Is(err, context.DeadlineExceeded) || errors.As(err, &timeout) && timeout.Timeout() {
		return imageStorageTimeout
	}
	return imageStorageUploadFailed
}

func imageStorageFailureMessage(code string) string {
	const billing = "; generation usage may already have been billed"
	switch code {
	case imageSourceDownloadFailed:
		return "image was generated but downloading the source image failed" + billing
	case imageResultTooLarge:
		return "image was generated but the result exceeds the transfer size limit" + billing
	case imageResultInvalid:
		return "image was generated but the result format or encoding is invalid" + billing
	case imageResultTimeout:
		return "image was generated but result processing timed out" + billing
	case imageStorageAccessDenied:
		return "image was generated but object storage denied access" + billing
	case imageStorageBucketMissing:
		return "image was generated but the object storage bucket does not exist" + billing
	case imageStorageTimeout:
		return "image was generated but uploading to object storage timed out" + billing
	default:
		return "image was generated but uploading to object storage failed" + billing
	}
}

// ImageTaskFailureSummary 统一短期任务到列表投影的安全摘要，旧版本任务仍按原有分类显示。
func ImageTaskFailureSummary(taskErr json.RawMessage) string {
	var envelope struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	const fallback = "图片生成失败，请根据请求 ID 查看错误日志"
	if json.Unmarshal(taskErr, &envelope) != nil {
		return fallback
	}
	if envelope.Type == "image_storage_unavailable" {
		return "图片存储不可用，未开始生成且未产生生成费用，请联系管理员"
	}
	const billing = "，生成用量可能已经计费"
	switch envelope.Code {
	case imageSourceDownloadFailed:
		return "图片生成完成但来源图片下载失败" + billing
	case imageResultTooLarge:
		return "图片生成完成但结果超过转存大小限制" + billing
	case imageResultInvalid:
		return "图片生成完成但结果格式或编码无效" + billing
	case imageResultTimeout:
		return "图片生成完成但结果处理超时" + billing
	case imageStorageAccessDenied:
		return "图片生成完成但存储访问被拒绝，请检查存储权限与凭据" + billing
	case imageStorageBucketMissing:
		return "图片生成完成但目标存储桶不存在" + billing
	case imageStorageTimeout:
		return "图片生成完成但上传存储超时" + billing
	case imageStorageUploadFailed:
		return "图片生成完成但上传存储失败" + billing
	}
	if strings.Contains(envelope.Message, "image was generated but object storage failed") {
		return "图片生成完成但结果存储失败" + billing
	}
	switch envelope.Type {
	case "timeout_error":
		return "图片生成任务超时"
	case "execution_interrupted":
		return "执行中断，结果不确定且可能已计费；系统未重新生成"
	default:
		return fallback
	}
}
