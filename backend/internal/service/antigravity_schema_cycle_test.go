//go:build unit

package service

import (
	"context"
	"os"
	"os/exec"
	"runtime/debug"
	"strings"
	"testing"
	"time"
)

// 递归 responseJsonSchema 在受限栈的子进程中复验：崩溃会直接终止进程，不能在主测试进程里验证。
func TestAntigravityRecursiveResponseSchemaDoesNotCrash(t *testing.T) {
	if os.Getenv("TOKENROUTER_PROTOCOL_SCHEMA_CHILD") == "1" {
		debug.SetMaxStack(256 << 10)
		body := []byte(`{"contents":[{"role":"user","parts":[{"text":"tree"}]}],"generationConfig":{"responseMimeType":"application/json","responseJsonSchema":{"$defs":{"Node":{"type":"object","properties":{"next":{"$ref":"#/$defs/Node"}}}},"$ref":"#/$defs/Node"}}}`)
		// 拒绝不支持的递归 schema 或原样保留都可以；不能崩溃或无界展开。
		_, _ = cleanGeminiRequest(body)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, "-test.run=^TestAntigravityRecursiveResponseSchemaDoesNotCrash$", "-test.count=1")
	cmd.Env = append(os.Environ(), "TOKENROUTER_PROTOCOL_SCHEMA_CHILD=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		excerpt := string(out)
		if len(excerpt) > 2400 {
			excerpt = excerpt[:2400]
		}
		t.Fatalf("recursive responseJsonSchema must not crash/hang; err=%v stack_overflow=%v timeout=%v child=%s", err, strings.Contains(string(out), "stack overflow"), ctx.Err() != nil, excerpt)
	}
}
