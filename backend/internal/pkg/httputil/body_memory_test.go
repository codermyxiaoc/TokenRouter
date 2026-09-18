package httputil

import (
	"bytes"
	"compress/gzip"
	"compress/zlib"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"
	"testing/iotest"

	"github.com/klauspost/compress/zstd"
)

type bodyReaderOnly struct{ io.Reader }

func TestReadRequestBodyChunksPreservesContent(t *testing.T) {
	for _, size := range []int{0, 1, 512, 513, 1 << 20, (2 << 20) + 17} {
		body := bytes.Repeat([]byte{'x'}, size)
		for _, declared := range []int64{-1, 0, 1, int64(size), 1 << 40} {
			t.Run(fmt.Sprintf("size_%d_length_%d", size, declared), func(t *testing.T) {
				req := &http.Request{Body: io.NopCloser(bodyReaderOnly{bytes.NewReader(body)}), ContentLength: declared, Header: make(http.Header)}
				got, err := ReadRequestBodyWithPrealloc(req)
				if err != nil || !bytes.Equal(got, body) {
					t.Fatalf("body was lost or changed: %v", err)
				}
			})
		}
	}
}

func TestReadRequestBodyChunksPreservesReadErrors(t *testing.T) {
	for _, readErr := range []error{io.ErrUnexpectedEOF, errors.New("connection reset"), context.Canceled, context.DeadlineExceeded} {
		reader := io.MultiReader(bytes.NewReader([]byte("partial")), iotest.ErrReader(readErr))
		req := &http.Request{Body: io.NopCloser(reader), ContentLength: 100, Header: make(http.Header)}
		got, err := ReadRequestBodyWithPrealloc(req)
		if !errors.Is(err, readErr) || got != nil {
			t.Fatalf("truncated body accepted: body=%q err=%v", got, err)
		}
	}
}

// 压缩解码和请求头清理沿用原行为，块读取不得绕过解码或改变错误。
func TestReadRequestBodyMemoryOptimizationPreservesEncodings(t *testing.T) {
	payload := []byte(`{"model":"gpt-6-astra","input":"keep","opaque":9007199254740993}`)
	for _, encoding := range []string{"identity", "gzip", "deflate", "zstd", "unsupported", "gzip_invalid"} {
		t.Run(encoding, func(t *testing.T) {
			var wire bytes.Buffer
			var writer io.WriteCloser
			switch encoding {
			case "gzip":
				writer = gzip.NewWriter(&wire)
			case "deflate":
				writer = zlib.NewWriter(&wire)
			case "zstd":
				var err error
				writer, err = zstd.NewWriter(&wire)
				if err != nil {
					t.Fatal(err)
				}
			default:
				_, _ = wire.Write(payload)
			}
			if writer != nil {
				if _, err := writer.Write(payload); err != nil {
					t.Fatal(err)
				}
				if err := writer.Close(); err != nil {
					t.Fatal(err)
				}
			}
			makeRequest := func() *http.Request {
				header := make(http.Header)
				actualEncoding := encoding
				if actualEncoding == "gzip_invalid" {
					actualEncoding = "gzip"
				}
				header.Set("Content-Encoding", actualEncoding)
				header.Set("Content-Length", fmt.Sprint(wire.Len()))
				return &http.Request{Body: io.NopCloser(iotest.HalfReader(bytes.NewReader(wire.Bytes()))), ContentLength: int64(wire.Len()), Header: header}
			}
			before, after := makeRequest(), makeRequest()
			want, wantErr := memoryBaselineReadRequestBody(before)
			got, gotErr := ReadRequestBodyWithPrealloc(after)
			if (wantErr != nil) != (gotErr != nil) || !bytes.Equal(want, got) {
				t.Fatalf("encoding behavior changed: before=%v after=%v", wantErr, gotErr)
			}
			if before.ContentLength != after.ContentLength || before.Header.Get("Content-Encoding") != after.Header.Get("Content-Encoding") || before.Header.Get("Content-Length") != after.Header.Get("Content-Length") {
				t.Fatal("encoding metadata changed")
			}
		})
	}
}

// 宽容 JSON 仍先处理 BOM 和字符串控制字节，并按规范化后的长度返回 413 类型错误。
func TestReadRequestBodyMemoryOptimizationPreservesLenientJSON(t *testing.T) {
	body := []byte("\xef\xbb\xbf{\"input\":\"line\nnext\"}")
	want, err := NormalizeLenientJSONRequestBody(bytes.Clone(body), 1024)
	if err != nil {
		t.Fatal(err)
	}
	makeRequest := func() *http.Request {
		return &http.Request{Body: io.NopCloser(iotest.OneByteReader(bytes.NewReader(body))), ContentLength: -1, Header: make(http.Header)}
	}
	got, err := ReadLenientJSONRequestBodyWithPrealloc(makeRequest(), 1024)
	if err != nil || !bytes.Equal(want, got) {
		t.Fatalf("lenient JSON changed: %q %v", got, err)
	}
	got, err = ReadLenientJSONRequestBodyWithPrealloc(makeRequest(), int64(len(want)-1))
	var limitErr *http.MaxBytesError
	if !errors.As(err, &limitErr) || got != nil {
		t.Fatalf("normalized body limit changed: %q %v", got, err)
	}
}

// 同进程比较网络式 Reader，避免 bytes.Reader.WriteTo 的优化掩盖真实接收分配。
func BenchmarkRequestBodyMemoryComparison(b *testing.B) {
	for _, sizeMiB := range []int{1, 10, 50} {
		b.Run(fmt.Sprintf("%dMiB", sizeMiB), func(b *testing.B) {
			body := bytes.Repeat([]byte{'x'}, sizeMiB<<20)
			for _, impl := range []struct {
				name string
				fn   func(*http.Request) ([]byte, error)
			}{{"before", memoryBaselineReadRequestBody}, {"after", ReadRequestBodyWithPrealloc}} {
				b.Run(impl.name, func(b *testing.B) {
					b.ReportAllocs()
					b.SetBytes(int64(len(body)))
					b.ResetTimer()
					for i := 0; i < b.N; i++ {
						req := &http.Request{Body: io.NopCloser(bodyReaderOnly{bytes.NewReader(body)}), ContentLength: int64(len(body)), Header: make(http.Header)}
						got, err := impl.fn(req)
						if err != nil || !bytes.Equal(got, body) {
							b.Fatalf("body changed: %v", err)
						}
					}
				})
			}
		})
	}
}

func TestReadRequestBodyChunksPreservesMaxBytesReader(t *testing.T) {
	body := bytes.Repeat([]byte{'x'}, (2<<20)+1)
	req := &http.Request{Body: http.MaxBytesReader(nil, io.NopCloser(bytes.NewReader(body)), 2<<20), ContentLength: int64(len(body)), Header: make(http.Header)}
	got, err := ReadRequestBodyWithPrealloc(req)
	var limitErr *http.MaxBytesError
	if !errors.As(err, &limitErr) || limitErr.Limit != 2<<20 || got != nil {
		t.Fatalf("body limit changed: len=%d err=%v", len(got), err)
	}
}

func BenchmarkReadLargeBody(b *testing.B) {
	for _, size := range []int{512 << 10, 69 << 20} {
		b.Run(fmt.Sprintf("bytes_%d", size), func(b *testing.B) {
			body := bytes.Repeat([]byte{'x'}, size)
			b.ReportAllocs()
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				req := &http.Request{Body: io.NopCloser(bodyReaderOnly{bytes.NewReader(body)}), ContentLength: int64(size), Header: make(http.Header)}
				got, err := ReadRequestBodyWithPrealloc(req)
				if err != nil || !bytes.Equal(got, body) {
					b.Fatalf("body changed: %v", err)
				}
			}
		})
	}
}
