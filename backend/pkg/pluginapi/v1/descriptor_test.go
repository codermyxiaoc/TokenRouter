package pluginv1

import (
	"testing"

	"google.golang.org/protobuf/types/descriptorpb"
)

// 验证生成描述符与本项目模块路径一致，避免修改包路径时损坏二进制长度字段。
func TestPluginDescriptorUsesForkModule(t *testing.T) {
	options := File_plugin_proto.Options().(*descriptorpb.FileOptions)
	if got := options.GetGoPackage(); got != "github.com/TokenFlux/TokenRouter/pkg/pluginapi/v1;pluginv1" {
		t.Fatalf("unexpected go_package: %s", got)
	}
	fields := (&ListAccountsResponse{}).ProtoReflect().Descriptor().Fields()
	if fields.ByName("account_ids").Number() != 1 || fields.ByName("accounts").Number() != 2 {
		t.Fatal("account directory changed wire compatibility")
	}
}
