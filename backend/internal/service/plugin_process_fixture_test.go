package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"sync/atomic"
	"testing"
	"time"

	pluginv1 "github.com/TokenFlux/TokenRouter/pkg/pluginapi/v1"
	hcplugin "github.com/hashicorp/go-plugin"
	"github.com/stretchr/testify/require"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// 测试二进制只在 go-plugin 的专属握手环境下充当真实子进程，正常测试不进入此分支。
func init() {
	if os.Getenv(pluginv1.HandshakeConfig.MagicCookieKey) == pluginv1.HandshakeConfig.MagicCookieValue {
		pluginv1.Serve(&pluginProcessFixture{})
		os.Exit(0)
	}
}

type pluginProcessFixture struct {
	pluginv1.UnimplementedTransportPluginServer
	broker          *hcplugin.GRPCBroker
	applyCalls      atomic.Int64
	hostReady       atomic.Bool
	directoryDenied atomic.Bool
}

func (p *pluginProcessFixture) SetHostBroker(broker *hcplugin.GRPCBroker) { p.broker = broker }
func (*pluginProcessFixture) GetInfo(context.Context, *pluginv1.GetInfoRequest) (*pluginv1.GetInfoResponse, error) {
	return &pluginv1.GetInfoResponse{PluginId: "test.process-fixture", PluginVersion: "1.0.0", ProtocolVersion: pluginv1.ProtocolVersion, TransportApiVersion: pluginv1.TransportAPIVersion}, nil
}
func (p *pluginProcessFixture) Health(context.Context, *pluginv1.HealthRequest) (*pluginv1.HealthResponse, error) {
	// 只读取原子快照，不调用宿主或上游，也不修改配置。
	raw, _ := json.Marshal(map[string]any{"apply_calls": p.applyCalls.Load(), "host_ready": p.hostReady.Load(), "directory_denied": p.directoryDenied.Load()})
	return &pluginv1.HealthResponse{Healthy: true, StatusJson: string(raw)}, nil
}
func (*pluginProcessFixture) ValidateConfig(_ context.Context, request *pluginv1.ValidateConfigRequest) (*pluginv1.ValidateConfigResponse, error) {
	return &pluginv1.ValidateConfigResponse{Valid: true, NormalizedConfigJson: request.ConfigJson}, nil
}
func (p *pluginProcessFixture) ApplyConfig(context.Context, *pluginv1.ApplyConfigRequest) (*pluginv1.ApplyConfigResponse, error) {
	p.applyCalls.Add(1)
	return &pluginv1.ApplyConfigResponse{Applied: true}, nil
}
func (p *pluginProcessFixture) InitHostServices(ctx context.Context, request *pluginv1.InitHostServicesRequest) (*pluginv1.InitHostServicesResponse, error) {
	connection, err := p.broker.Dial(request.HostServiceId)
	if err != nil {
		return nil, err
	}
	defer func() { _ = connection.Close() }()
	client := pluginv1.NewHostServiceClient(connection)
	_, err = client.KVSet(ctx, &pluginv1.KVSetRequest{Namespace: "runtime", Key: "probe", Value: []byte("child-process"), TtlSeconds: 60})
	if err != nil {
		return nil, err
	}
	value, err := client.KVGet(ctx, &pluginv1.KVGetRequest{Namespace: "runtime", Key: "probe"})
	if err != nil {
		return nil, err
	}
	_, listErr := client.ListAccounts(ctx, &pluginv1.ListAccountsRequest{Platform: PlatformOpenAI, AccountType: AccountTypeOAuth})
	_, identityErr := client.ResolveOutboundIdentity(ctx, &pluginv1.ResolveOutboundIdentityRequest{AccountId: 1})
	p.directoryDenied.Store(status.Code(listErr) == codes.Unavailable && status.Code(identityErr) == codes.Unavailable)
	p.hostReady.Store(value.Found && string(value.Value) == "child-process")
	return &pluginv1.InitHostServicesResponse{Ready: p.hostReady.Load()}, nil
}

// 使用当前测试二进制验证真实子进程握手、校验和、gRPC broker 与停止流程，无需外部插件包。
func TestPluginProcessLifecycleHostServicesAndPassiveStatus(t *testing.T) {
	executable, err := os.Executable()
	require.NoError(t, err)
	file, err := os.Open(executable)
	require.NoError(t, err)
	hash := sha256.New()
	_, err = io.Copy(hash, file)
	require.NoError(t, file.Close())
	require.NoError(t, err)
	installation := &PluginInstallation{ID: 7, PluginKey: "test.process-fixture", Version: "1.0.0", BinaryPath: executable, BinarySHA256: hex.EncodeToString(hash.Sum(nil))}
	store := newFakePluginKVStore()
	manager := &PluginManager{repo: &statusStubRepository{}, kvStore: store, accountDirectory: &fakeAccountDirectory{ids: []int64{1}}}
	// 无账号能力声明的插件可以使用 KV，但不得拿到目录或凭据。
	runtime, err := startPluginRuntime(context.Background(), installation, 15*time.Second, t.TempDir(), manager.buildHostServices(installation))
	require.NoError(t, err)
	defer runtime.kill()
	manager.runtimes = map[int64]*pluginRuntime{7: runtime}
	require.NoError(t, runtime.validateAndApplyConfig(context.Background(), []byte(`{"enabled":true}`)))
	for range 3 {
		health, err := manager.Status(context.Background(), 7)
		require.NoError(t, err)
		require.True(t, health.Healthy)
		require.JSONEq(t, `{"apply_calls":1,"host_ready":true,"directory_denied":true}`, health.StatusJson)
	}
	value, found, err := store.Get(context.Background(), installation.PluginKey, "runtime", "probe")
	require.NoError(t, err)
	require.True(t, found)
	require.Equal(t, "child-process", string(value))
	_, found, err = store.Get(context.Background(), "another.plugin", "runtime", "probe")
	require.NoError(t, err)
	require.False(t, found)
	runtime.kill()
	require.True(t, runtime.client.Exited())
}
