package service

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestMaskAuditCredential(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", ""},
		{"short", "abc", "****"},
		{"boundary_14", "12345678901234", "****"},
		{"long", "sk-ant-api03-abcdefghijklmnop1234", "sk-ant****1234"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := MaskAuditCredential(tc.in)
			if got != tc.want {
				t.Fatalf("MaskAuditCredential(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// 掩码结果绝不能包含原始凭证的中间部分。
			if len(tc.in) > 14 && strings.Contains(got, tc.in) {
				t.Fatalf("masked value leaks full credential: %q", got)
			}
		})
	}
}

func TestRedactAuditBody_JSONRedactsSecrets(t *testing.T) {
	raw := []byte(`{
		"name": "acc1",
		"base_url": "https://evil.example.com",
		"credentials": {"api_key": "sk-secret-123", "base_url": "https://evil.example.com"},
		"credential": "google-id-token-canary",
		"new_password": "hunter2",
		"totp_code": "123456",
		"nested": [{"access_token": "tok_abc"}]
	}`)
	out := RedactAuditBody(raw, "application/json")

	var parsed map[string]any
	if err := json.Unmarshal([]byte(out), &parsed); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out)
	}

	// 敏感字段被擦除。
	for _, secret := range []string{"sk-secret-123", "google-id-token-canary", "hunter2", "123456", "tok_abc"} {
		if strings.Contains(out, secret) {
			t.Fatalf("redacted body still contains secret %q: %s", secret, out)
		}
	}
	// 非敏感字段（base_url、name）保留以便追责。
	if !strings.Contains(out, "evil.example.com") {
		t.Fatalf("base_url should be preserved for accountability: %s", out)
	}
	if !strings.Contains(out, "acc1") {
		t.Fatalf("name should be preserved: %s", out)
	}
}

// 裸键 "session"（Ollama Cloud 会话保存的请求体字段）值整体就是浏览器 Cookie 明文，
// 必须命中键级脱敏；session_id 等运行态标识不受影响，保留以便追责。
func TestRedactAuditBody_BareSessionKeyRedacted(t *testing.T) {
	raw := []byte(`{"session": "wos-session=cookie-canary", "session_id": "sid-visible"}`)
	out := RedactAuditBody(raw, "application/json")

	if strings.Contains(out, "cookie-canary") {
		t.Fatalf("redacted body still contains the session cookie: %s", out)
	}
	if !strings.Contains(out, "sid-visible") {
		t.Fatalf("session_id should be preserved for accountability: %s", out)
	}
}

// 素材在截断前整体脱敏，既不能保存签名或媒体正文，也不能挤掉后面的测试参数。
func TestRedactAuditBody_AccountTestMediaRedactedBeforeTruncation(t *testing.T) {
	for _, keys := range [][3]string{
		{"reference_image_url", "image_data_url", "audio_data_url"},
		{"referenceImageURL", "imageDataURL", "audioDataURL"},
		{"REFERENCE_IMAGE_URL", "IMAGE_DATA_URL", "AUDIO_DATA_URL"},
		{"reference-image-url", "image.data.url", "audio data url"},
	} {
		t.Run(keys[0], func(t *testing.T) {
			body := map[string]any{
				keys[0]:    "https://private-media.example/first-frame-canary.png?signature=signed-media-canary&token=media-token-canary",
				keys[1]:    "data:image/png;base64," + strings.Repeat("a", 64*1024),
				keys[2]:    "data:audio/wav;base64," + strings.Repeat("b", 64*1024),
				"model_id": "MiniMax-H3", "video_endpoint": "minimax", "resolution": "2K", "duration": 5,
			}
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatal(err)
			}
			if len(raw) <= auditRequestBodyMaxBytes || len(raw) > AuditRequestBodyCaptureLimit {
				t.Fatalf("fixture must exceed storage limit while remaining within capture limit: %d bytes", len(raw))
			}
			out := RedactAuditBody(raw, "application/json")
			if len(out) > auditRequestBodyMaxBytes || strings.Contains(out, "<truncated>") {
				t.Fatal("redacted media must not force audit truncation")
			}
			var parsed map[string]any
			if err := json.Unmarshal([]byte(out), &parsed); err != nil {
				t.Fatalf("redacted audit must retain valid JSON: %v", err)
			}
			for _, key := range keys {
				if parsed[key] != auditRedactedPlaceholder {
					t.Errorf("media field %q was not completely redacted", key)
				}
			}
			for _, sensitive := range []string{"private-media.example", "first-frame-canary", "signed-media-canary", "media-token-canary", "data:image/", "data:audio/"} {
				if strings.Contains(out, sensitive) {
					t.Errorf("redacted audit contains media content %q", sensitive)
				}
			}
			for key, expected := range map[string]any{"model_id": "MiniMax-H3", "video_endpoint": "minimax", "resolution": "2K", "duration": float64(5)} {
				if parsed[key] != expected {
					t.Errorf("diagnostic field %q must survive media redaction: got %v", key, parsed[key])
				}
			}
		})
	}
}

// TestRedactAuditBody_AuthoritativeTablesSynced 覆盖曾经漏网的凭证字段：
// 账号 credentials 敏感子键、支付渠道无分隔符密钥、字符串值内嵌凭证的 proxy_key / custom_key，
// 以及 camelCase 等命名变体（归一化比对）。
func TestRedactAuditBody_AuthoritativeTablesSynced(t *testing.T) {
	raw := []byte(`{
		"credentials": {
			"session_key": "sk-session-aaa",
			"service_account_json": "{\"private_key\":\"pem-body-bbb\"}",
			"service_account": "sa-blob-ccc"
		},
		"proxy_key": "socks5|1.2.3.4|1080|proxyuser|proxypass-ddd",
		"custom_key": "sk-custom-eee",
		"config": {
			"pkey": "easypay-merchant-fff",
			"privateKey": "alipay-pem-ggg",
			"apiv3key": "wxpay-v3-hhh",
			"SecretKey": "stripe-sk-iii",
			"webhookSecret": "whsec-jjj"
		},
		"provider_key": "stripe",
		"name": "instance-1"
	}`)
	out := RedactAuditBody(raw, "application/json")

	for _, secret := range []string{
		"sk-session-aaa", "pem-body-bbb", "sa-blob-ccc",
		"proxypass-ddd", "sk-custom-eee",
		"easypay-merchant-fff", "alipay-pem-ggg", "wxpay-v3-hhh",
		"stripe-sk-iii", "whsec-jjj",
	} {
		if strings.Contains(out, secret) {
			t.Fatalf("redacted body still contains secret %q: %s", secret, out)
		}
	}
	// provider_key 是渠道标识而非密钥，必须保留以便追责。
	if !strings.Contains(out, `"provider_key":"stripe"`) {
		t.Fatalf("provider_key should be preserved for accountability: %s", out)
	}
	if !strings.Contains(out, "instance-1") {
		t.Fatalf("name should be preserved: %s", out)
	}
}

// SensitiveCredentialKeys 中的每个键都必须被审计脱敏判定命中（防两表漂移的守卫）。
func TestAuditSensitiveKeys_CoverCredentialTable(t *testing.T) {
	for _, k := range SensitiveCredentialKeys {
		if !isAuditSensitiveBodyKey(k) {
			t.Fatalf("credential key %q is not covered by audit redaction", k)
		}
	}
	for provider, fields := range providerSensitiveConfigFields {
		for k := range fields {
			if !isAuditSensitiveBodyKey(k) {
				t.Fatalf("payment provider %q sensitive field %q is not covered by audit redaction", provider, k)
			}
		}
	}
}

func TestRedactAuditBody_NonJSONOmitted(t *testing.T) {
	out := RedactAuditBody([]byte("username=admin&password=secret"), "application/x-www-form-urlencoded")
	if strings.Contains(out, "secret") {
		t.Fatalf("non-json body must not leak content: %s", out)
	}
	if !strings.Contains(out, "omitted") {
		t.Fatalf("expected omission marker, got: %s", out)
	}
}

func TestRedactAuditBody_Empty(t *testing.T) {
	if got := RedactAuditBody(nil, "application/json"); got != "" {
		t.Fatalf("expected empty for nil body, got %q", got)
	}
}

func TestSessionBindingHash(t *testing.T) {
	a := &SessionBinding{IP: "1.2.3.4", UserAgent: "Mozilla/5.0"}
	b := &SessionBinding{IP: "1.2.3.4", UserAgent: "Mozilla/5.0"}
	if a.Hash() != b.Hash() {
		t.Fatalf("identical bindings must hash equal")
	}
	if a.Hash() == "" {
		t.Fatalf("non-empty binding must produce non-empty hash")
	}

	// IP 变化 → 哈希变化。
	c := &SessionBinding{IP: "5.6.7.8", UserAgent: "Mozilla/5.0"}
	if a.Hash() == c.Hash() {
		t.Fatalf("changing IP must change hash")
	}
	// UA 变化 → 哈希变化。
	d := &SessionBinding{IP: "1.2.3.4", UserAgent: "curl/8.0"}
	if a.Hash() == d.Hash() {
		t.Fatalf("changing UA must change hash")
	}

	// 空指纹 → 空哈希（旧 token 兼容）。
	empty := &SessionBinding{}
	if empty.Hash() != "" {
		t.Fatalf("empty binding must hash to empty string")
	}
	var nilBinding *SessionBinding
	if nilBinding.Hash() != "" {
		t.Fatalf("nil binding must hash to empty string")
	}
}

func TestParseAuditLogRetentionDays(t *testing.T) {
	cases := map[string]int{
		"":       defaultAuditLogRetentionDays,
		"abc":    defaultAuditLogRetentionDays,
		"90":     90,
		"0":      0,
		"-1":     0,
		"  30  ": 30,
	}
	for in, want := range cases {
		if got := parseAuditLogRetentionDays(in); got != want {
			t.Fatalf("parseAuditLogRetentionDays(%q) = %d, want %d", in, got, want)
		}
	}
}
