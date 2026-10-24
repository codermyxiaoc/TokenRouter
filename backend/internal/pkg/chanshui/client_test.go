package chanshui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"testing"
	"time"
)

const testID = "b6c1d0e29f4a4780a1b2c3d4e5f60718"

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func testClient(f roundTripFunc) *Client { return NewClient(&http.Client{Transport: f}) }

func validRequest() CreateRequest {
	return CreateRequest{BaseURL: "https://gateway.example/v1", APIKey: "sk-test-secret", Model: "claude-sonnet-5", Protocol: "auto", Timeout: 360, Sections: []string{"fingerprint", "injection"}}
}

func TestCreateUsesOfficialContractAndNoReplay(t *testing.T) {
	calls := 0
	client := testClient(func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != "POST" || req.URL.String() != "https://chanshui.dev/api/v1/audits" {
			t.Fatalf("unexpected method or fixed endpoint")
		}
		if req.GetBody != nil || req.Header.Get("Idempotency-Key") != "" {
			t.Fatal("POST must not be replayable")
		}
		if req.Header.Get("Content-Type") != "application/json" || req.Header.Get("Accept") != "application/json" {
			t.Fatal("missing JSON headers")
		}
		var payload map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if len(payload) != 6 || payload["api_key"] != "sk-test-secret" || payload["protocol"] != "auto" || payload["timeout"] != float64(360) {
			t.Fatal("request does not match public API schema")
		}
		return response(202, `{"id":"`+testID+`","status":"queued","sections":["fingerprint","injection"],"expires_at":2000000000,"poll_url":"https://attacker.example/steal","skipped":[]}`), nil
	})
	result, err := client.Create(context.Background(), validRequest())
	if err != nil || result.ID != testID || result.Terminal() || len(result.Sections) != 2 {
		t.Fatalf("unexpected create result: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d", calls)
	}
}

func TestProtocolsAndSections(t *testing.T) {
	for _, protocol := range []string{"", "auto", "chat", "anthropic", "openai"} {
		t.Run(protocol, func(t *testing.T) {
			r := validRequest()
			r.Protocol = protocol
			r.Sections = []string{"fingerprint", "injection", "cache", "tools", "web", "knowledge", "thinking", "iq"}
			if err := ValidateCreate(r); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, timeout := range []int{0, 10, 360, 600} {
		r := validRequest()
		r.Timeout = timeout
		if err := ValidateCreate(r); err != nil {
			t.Fatal(err)
		}
	}
	r := validRequest()
	r.Sections = nil
	if err := ValidateCreate(r); err != nil {
		t.Fatal(err)
	}
}

func TestCreatePreservesPublicHTTPAndHTTPSBaseURL(t *testing.T) {
	for _, base := range []string{"http://gateway.example:8080/prefix/v1", "https://gateway.example:8443/prefix/v1"} {
		t.Run(base, func(t *testing.T) {
			input := validRequest()
			input.BaseURL = base
			calls := 0
			client := testClient(func(req *http.Request) (*http.Response, error) {
				calls++
				// 上游协议允许 HTTP，不改变发送凭据到官方检测服务时的固定 HTTPS 端点。
				if req.URL.String() != "https://chanshui.dev/api/v1/audits" {
					t.Fatal("request left the fixed HTTPS audit endpoint")
				}
				var payload CreateRequest
				if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
					t.Fatal(err)
				}
				if payload.BaseURL != base {
					t.Fatal("configured upstream scheme or path was changed")
				}
				return response(202, `{"id":"`+testID+`","status":"queued"}`), nil
			})
			if _, err := client.Create(context.Background(), input); err != nil || calls != 1 {
				t.Fatalf("create failed or repeated: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestInvalidCreateNeverCallsTransport(t *testing.T) {
	tests := map[string]func(*CreateRequest){
		"key":                 func(r *CreateRequest) { r.APIKey = " " },
		"key_length":          func(r *CreateRequest) { r.APIKey = strings.Repeat("x", 8193) },
		"model":               func(r *CreateRequest) { r.Model = "" },
		"model_length":        func(r *CreateRequest) { r.Model = strings.Repeat("x", 257) },
		"model_control":       func(r *CreateRequest) { r.Model = "gpt\nsecret" },
		"non_http_scheme":     func(r *CreateRequest) { r.BaseURL = "ftp://gateway.example" },
		"http_localhost":      func(r *CreateRequest) { r.BaseURL = "http://localhost:8080" },
		"https_localhost":     func(r *CreateRequest) { r.BaseURL = "https://localhost" },
		"loopback":            func(r *CreateRequest) { r.BaseURL = "http://127.0.0.1" },
		"ipv6_loopback":       func(r *CreateRequest) { r.BaseURL = "http://[::1]" },
		"private_ip":          func(r *CreateRequest) { r.BaseURL = "http://192.168.1.1" },
		"link_local":          func(r *CreateRequest) { r.BaseURL = "http://169.254.169.254" },
		"invalid_port":        func(r *CreateRequest) { r.BaseURL = "http://gateway.example:65536" },
		"http_userinfo":       func(r *CreateRequest) { r.BaseURL = "http://user:pass@gateway.example" },
		"http_query":          func(r *CreateRequest) { r.BaseURL = "http://gateway.example?key=secret" },
		"userinfo":            func(r *CreateRequest) { r.BaseURL = "https://user:pass@gateway.example" },
		"query":               func(r *CreateRequest) { r.BaseURL = "https://gateway.example?key=secret" },
		"empty_query":         func(r *CreateRequest) { r.BaseURL = "https://gateway.example?" },
		"fragment":            func(r *CreateRequest) { r.BaseURL = "https://gateway.example/#secret" },
		"protocol":            func(r *CreateRequest) { r.Protocol = "responses" },
		"timeout_small":       func(r *CreateRequest) { r.Timeout = 9 },
		"timeout_large":       func(r *CreateRequest) { r.Timeout = 601 },
		"section":             func(r *CreateRequest) { r.Sections = []string{"all"} },
		"duplicate":           func(r *CreateRequest) { r.Sections = []string{"iq", "iq"} },
		"non_claude_thinking": func(r *CreateRequest) { r.Model = "gpt-6"; r.Sections = []string{"thinking"} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			r := validRequest()
			mutate(&r)
			client := testClient(func(*http.Request) (*http.Response, error) {
				t.Fatal("invalid request reached transport")
				return nil, nil
			})
			if _, err := client.Create(context.Background(), r); !errors.Is(err, ErrInvalidRequest) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestGetValidatesIdentityAndIgnoresPollURL(t *testing.T) {
	client := testClient(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != DefaultBaseURL+"/api/v1/audits/"+testID || req.Method != "GET" {
			t.Fatal("incorrect polling endpoint")
		}
		return response(200, `{"id":"`+testID+`","status":"running","poll_url":"http://127.0.0.1/secret"}`), nil
	})
	if _, err := client.Get(context.Background(), strings.ToUpper(testID)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "abc", "../", testID + "?x=1", "https://attacker.example", strings.Repeat("g", 32)} {
		if _, err := client.Get(context.Background(), id); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("accepted invalid ID")
		}
	}
	for _, body := range []string{`{"id":"00000000000000000000000000000000","status":"done"}`, `{"status":"done"}`, `{"id":"` + testID + `"}`, `[]`, `null`, `{broken`} {
		client = testClient(func(*http.Request) (*http.Response, error) { return response(200, body), nil })
		if _, err := client.Get(context.Background(), testID); !errors.Is(err, ErrInvalidResponse) {
			t.Fatalf("invalid response error = %v", err)
		}
	}
}

func TestRedirectRefusedAndCallerConfigurationUnchanged(t *testing.T) {
	jar, _ := cookiejar.New(nil)
	calls := 0
	source := &http.Client{Jar: jar, Timeout: 9 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { t.Fatal("inherited redirect callback"); return nil }, Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		r := response(307, "secret")
		r.Header.Set("Location", "https://attacker.example")
		return r, nil
	})}
	client := NewClient(source)
	if client.httpClient.Jar != nil || source.Jar == nil || client.httpClient.Timeout != source.Timeout {
		t.Fatal("configuration cloning failed")
	}
	if _, err := client.Create(context.Background(), validRequest()); !errors.Is(err, ErrRedirect) {
		t.Fatalf("error = %v", err)
	}
	if calls != 1 {
		t.Fatalf("followed redirect or replayed POST %d times", calls)
	}
	if NewClient(nil).httpClient.Timeout != 45*time.Second {
		t.Fatal("missing default deadline")
	}
}

func TestHTTPErrorDoesNotExposeRemoteBody(t *testing.T) {
	for _, status := range []int{400, 401, 403, 404, 410, 429, 500, 503} {
		client := testClient(func(*http.Request) (*http.Response, error) {
			r := response(status, `{"error":"sk-secret-token `+testID+`"}`)
			r.Header.Set("Retry-After", "61")
			return r, nil
		})
		_, err := client.Get(context.Background(), testID)
		var httpErr *HTTPError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != status || httpErr.RetryAfter != 61*time.Second {
			t.Fatalf("unexpected HTTP error = %v", err)
		}
		if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), testID) {
			t.Fatal("response leaked")
		}
	}
}

func TestRetryAfterBounds(t *testing.T) {
	now := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	for _, item := range []struct {
		input string
		want  time.Duration
	}{{"-1", 0}, {"0", 0}, {"junk", 0}, {"9999999", 24 * time.Hour}, {"5", 5 * time.Second}, {now.Add(time.Hour).Format(http.TimeFormat), time.Hour}, {now.Add(48 * time.Hour).Format(http.TimeFormat), 24 * time.Hour}} {
		if got := parseRetryAfter(item.input, now); got != item.want {
			t.Fatalf("retry got %v want %v", got, item.want)
		}
	}
}

func TestResponseLimitAndTransportError(t *testing.T) {
	client := testClient(func(*http.Request) (*http.Response, error) {
		return response(200, strings.Repeat("x", MaxResponseBytes+1)), nil
	})
	if _, err := client.Get(context.Background(), testID); !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("error = %v", err)
	}
	for _, item := range []struct {
		err  error
		want error
	}{{context.DeadlineExceeded, context.DeadlineExceeded}, {context.Canceled, context.Canceled}, {errors.New("sk-secret-token"), ErrTransport}} {
		calls := 0
		client = testClient(func(*http.Request) (*http.Response, error) { calls++; return nil, item.err })
		_, err := client.Create(context.Background(), validRequest())
		if !errors.Is(err, item.want) || calls != 1 || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unsafe transport result = %v", err)
		}
	}
}

func TestResponseReadTimeoutIsSafe(t *testing.T) {
	client := testClient(func(*http.Request) (*http.Response, error) {
		r := response(200, "")
		r.Body = readErrorBody{}
		return r, nil
	})
	_, err := client.Get(context.Background(), testID)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}

type readErrorBody struct{}

func (readErrorBody) Read([]byte) (int, error) { return 0, context.DeadlineExceeded }
func (readErrorBody) Close() error             { return nil }

func TestFormattingRedactsCredentialsAndTaskIDs(t *testing.T) {
	for _, value := range []any{validRequest(), Result{ID: testID, Summary: "sk-secret"}} {
		for _, format := range []string{"%v", "%+v", "%#v"} {
			got := fmt.Sprintf(format, value)
			if strings.Contains(got, "sk-secret") || strings.Contains(got, "sk-test") || strings.Contains(got, testID) {
				t.Fatal("formatted secret")
			}
		}
	}
}
