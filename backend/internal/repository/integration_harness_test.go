//go:build integration

package repository

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	dbent "github.com/TokenFlux/TokenRouter/ent"
	_ "github.com/TokenFlux/TokenRouter/ent/runtime"
	"github.com/TokenFlux/TokenRouter/internal/pkg/timezone"
	"github.com/stretchr/testify/require"
	"github.com/stretchr/testify/suite"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/go-connections/nat"
	_ "github.com/lib/pq"
	redisclient "github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"
	"github.com/testcontainers/testcontainers-go/wait"
)

const (
	redisImageTag    = "redis:8.4-alpine"
	postgresImageTag = "postgres:18.1-alpine3.23"
)

var (
	integrationDB        *sql.DB
	integrationEntClient *dbent.Client
	integrationRedis     *redisclient.Client

	redisNamespaceSeq uint64
)

func TestMain(m *testing.M) {
	ctx := context.Background()

	if err := timezone.Init("UTC"); err != nil {
		log.Printf("failed to init timezone: %v", err)
		os.Exit(1)
	}

	// 外部基础设施模式：同时提供 TOKENROUTER_TEST_POSTGRES_DSN 与 TOKENROUTER_TEST_REDIS_ADDR 时
	// 直接连接（例如在同一 Docker 网络内运行 Linux 测试二进制），不启动 testcontainers。
	// 用于 Windows 端口转发在高并发下重置连接、导致业务用例无法执行的环境；默认行为不变。
	if dsn, redisAddr := strings.TrimSpace(os.Getenv("TOKENROUTER_TEST_POSTGRES_DSN")), strings.TrimSpace(os.Getenv("TOKENROUTER_TEST_REDIS_ADDR")); dsn != "" && redisAddr != "" {
		os.Exit(runIntegrationWithExternalInfra(ctx, m, dsn, redisAddr))
	}

	if !dockerIsAvailable(ctx) {
		// In CI we expect Docker to be available so integration tests should fail loudly.
		if os.Getenv("CI") != "" {
			log.Printf("docker is not available (CI=true); failing integration tests")
			os.Exit(1)
		}
		log.Printf("docker is not available; skipping integration tests (start Docker to enable)")
		os.Exit(0)
	}

	// 本机 Docker（尤其 Windows/WSL）冷启动容器可能远超默认 60s，等待时长可通过
	// TOKENROUTER_TEST_CONTAINER_STARTUP_TIMEOUT（如 10m）放大；CI 默认值不变。
	startupTimeout := integrationContainerStartupTimeout()
	postgresImage := selectDockerImage(ctx, postgresImageTag)
	pgContainer, err := tcpostgres.Run(
		ctx,
		postgresImage,
		tcpostgres.WithDatabase("sub2api_test"),
		tcpostgres.WithUsername("postgres"),
		tcpostgres.WithPassword("postgres"),
		// 并发结算类用例会同时打开大量连接，默认 100 在慢速环境下易耗尽导致连接被重置。
		testcontainers.WithCmd("postgres", "-c", "max_connections=400"),
		loopbackPortBinding("5432/tcp"),
		// 与 BasicWaitStrategies 相同的两步等待（日志出现两次就绪 + 端口可连），但使用可配置的超时。
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").WithOccurrence(2).WithStartupTimeout(startupTimeout),
			wait.ForListeningPort("5432/tcp").WithStartupTimeout(startupTimeout),
		),
	)
	if err != nil {
		log.Printf("failed to start postgres container: %v", err)
		os.Exit(1)
	}
	defer func() { _ = pgContainer.Terminate(ctx) }()

	redisContainer, err := tcredis.Run(
		ctx,
		redisImageTag,
		loopbackPortBinding("6379/tcp"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("Ready to accept connections").WithStartupTimeout(startupTimeout),
			wait.ForListeningPort("6379/tcp").WithStartupTimeout(startupTimeout),
		),
	)
	if err != nil {
		log.Printf("failed to start redis container: %v", err)
		os.Exit(1)
	}
	defer func() { _ = redisContainer.Terminate(ctx) }()

	dsn, err := pgContainer.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	if err != nil {
		log.Printf("failed to get postgres dsn: %v", err)
		os.Exit(1)
	}

	integrationDB, err = openSQLWithRetry(ctx, dsn, max(30*time.Second, startupTimeout/2))
	if err != nil {
		log.Printf("failed to open sql db: %v", err)
		os.Exit(1)
	}
	if err := ApplyMigrations(ctx, integrationDB); err != nil {
		log.Printf("failed to apply db migrations: %v", err)
		os.Exit(1)
	}

	// 创建 ent client 用于集成测试
	drv := entsql.OpenDB(dialect.Postgres, integrationDB)
	integrationEntClient = dbent.NewClient(dbent.Driver(drv))

	redisHost, err := redisContainer.Host(ctx)
	if err != nil {
		log.Printf("failed to get redis host: %v", err)
		os.Exit(1)
	}
	// 与数据库相同，避免 localhost 解析到 ::1 后被 Docker Desktop 的 IPv6 端口转发重置。
	if redisHost == "localhost" {
		redisHost = "127.0.0.1"
	}
	redisPort, err := redisContainer.MappedPort(ctx, "6379/tcp")
	if err != nil {
		log.Printf("failed to get redis port: %v", err)
		os.Exit(1)
	}

	// 并发结算用例会同时发起数十个 Redis 命令；go-redis 默认 3s 读超时与较小连接池在本机慢速
	// Docker 上会出现 i/o timeout，与业务无关。这里按生产 redis.go 的显式配置放宽，并随启动超时放大。
	redisIOTimeout := max(10*time.Second, startupTimeout/30)
	integrationRedis = redisclient.NewClient(&redisclient.Options{
		Addr:         fmt.Sprintf("%s:%d", redisHost, redisPort.Int()),
		DB:           0,
		PoolSize:     256,
		DialTimeout:  redisIOTimeout,
		ReadTimeout:  redisIOTimeout,
		WriteTimeout: redisIOTimeout,
		PoolTimeout:  redisIOTimeout + time.Second,
	})
	// 端口映射在慢速 Docker 上可能短暂不可用，Ping 带重试直至超时。
	if err := pingRedisWithRetry(ctx, integrationRedis, max(30*time.Second, startupTimeout/2)); err != nil {
		log.Printf("failed to ping redis: %v", err)
		os.Exit(1)
	}

	code := m.Run()

	_ = integrationEntClient.Close()
	_ = integrationRedis.Close()
	_ = integrationDB.Close()

	os.Exit(code)
}

// runIntegrationWithExternalInfra 使用外部 PostgreSQL/Redis 执行与容器模式相同的初始化，返回测试退出码。
func runIntegrationWithExternalInfra(ctx context.Context, m *testing.M, dsn, redisAddr string) int {
	var err error
	integrationDB, err = openSQLWithRetry(ctx, dsn, 2*time.Minute)
	if err != nil {
		log.Printf("failed to open external sql db: %v", err)
		return 1
	}
	if err := ApplyMigrations(ctx, integrationDB); err != nil {
		log.Printf("failed to apply db migrations: %v", err)
		return 1
	}
	integrationEntClient = dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, integrationDB)))
	integrationRedis = redisclient.NewClient(&redisclient.Options{
		Addr:         redisAddr,
		DB:           0,
		PoolSize:     256,
		DialTimeout:  10 * time.Second,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 10 * time.Second,
		PoolTimeout:  11 * time.Second,
	})
	if err := pingRedisWithRetry(ctx, integrationRedis, 2*time.Minute); err != nil {
		log.Printf("failed to ping external redis: %v", err)
		return 1
	}
	code := m.Run()
	_ = integrationEntClient.Close()
	_ = integrationRedis.Close()
	_ = integrationDB.Close()
	return code
}

// integrationContainerStartupTimeout 返回容器就绪等待时长；默认 60s 与 testcontainers 默认一致，
// 可通过 TOKENROUTER_TEST_CONTAINER_STARTUP_TIMEOUT（Go duration 格式）放大。
func integrationContainerStartupTimeout() time.Duration {
	if raw := strings.TrimSpace(os.Getenv("TOKENROUTER_TEST_CONTAINER_STARTUP_TIMEOUT")); raw != "" {
		if d, err := time.ParseDuration(raw); err == nil && d > 0 {
			return d
		}
		log.Printf("ignoring invalid TOKENROUTER_TEST_CONTAINER_STARTUP_TIMEOUT=%q", raw)
	}
	return 60 * time.Second
}

// pingRedisWithRetry 在超时前重复 Ping，容忍容器端口映射就绪的短暂延迟。
func pingRedisWithRetry(ctx context.Context, client *redisclient.Client, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if lastErr = client.Ping(ctx).Err(); lastErr == nil {
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return lastErr
}

func dockerIsAvailable(ctx context.Context) bool {
	cmd := exec.CommandContext(ctx, "docker", "info")
	cmd.Env = os.Environ()
	return cmd.Run() == nil
}

// selectDockerImage 为集成测试框架解析容器镜像。
//
// TOKENROUTER_TEST_POSTGRES_IMAGE 可覆盖 PostgreSQL 镜像，使测试能在文档声明的
// 最低支持版本上运行，而不只验证最新版本。这对跨大版本行为不同的 SQL 很重要：
// PostgreSQL 17 起 jsonpath .datetime() 才接受 ISO-8601 的 "Z" 标识，
// 固定使用 18 的测试无法发现 14–16 上的故障。
//
//	TOKENROUTER_TEST_POSTGRES_IMAGE=postgres:15-alpine go test -tags integration ./internal/repository/
func selectDockerImage(ctx context.Context, preferred string) string {
	if override := strings.TrimSpace(os.Getenv("TOKENROUTER_TEST_POSTGRES_IMAGE")); override != "" &&
		strings.HasPrefix(preferred, "postgres:") {
		return override
	}
	if dockerImageExists(ctx, preferred) {
		return preferred
	}

	return preferred
}

func dockerImageExists(ctx context.Context, image string) bool {
	cmd := exec.CommandContext(ctx, "docker", "image", "inspect", image)
	cmd.Env = os.Environ()
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run() == nil
}

// loopbackPortBinding 把容器端口只发布到 127.0.0.1 的随机端口。
// 默认的 0.0.0.0 绑定在 Windows 上会与本机其它进程对同一端口的 127.0.0.1 监听冲突，
// 导致连接被重置；只绑定回环地址后，测试客户端连接的就是容器本身。
// HostConfigModifier 早于 testcontainers 合并暴露端口执行，因此这里直接声明完整绑定。
func loopbackPortBinding(ports ...string) testcontainers.CustomizeRequestOption {
	return testcontainers.WithHostConfigModifier(func(hostConfig *container.HostConfig) {
		if hostConfig.PortBindings == nil {
			hostConfig.PortBindings = nat.PortMap{}
		}
		for _, port := range ports {
			hostConfig.PortBindings[nat.Port(port)] = []nat.PortBinding{{HostIP: "127.0.0.1", HostPort: "0"}}
		}
	})
}

// preferIPv4Loopback 把 testcontainers 返回的 localhost 改写为 127.0.0.1。
// Go 会优先解析 localhost 为 ::1，而 Docker Desktop 的 IPv6 端口转发在 Windows 上会重置连接，
// 表现为 "connection was forcibly closed"；备份恢复测试早已对容器主机做同样处理。
func preferIPv4Loopback(dsn string) string {
	return strings.Replace(dsn, "@localhost:", "@127.0.0.1:", 1)
}

func openSQLWithRetry(ctx context.Context, dsn string, timeout time.Duration) (*sql.DB, error) {
	dsn = preferIPv4Loopback(dsn)
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		db, err := sql.Open("postgres", dsn)
		if err != nil {
			lastErr = err
			time.Sleep(250 * time.Millisecond)
			continue
		}

		if err := pingWithTimeout(ctx, db, 2*time.Second); err != nil {
			lastErr = err
			_ = db.Close()
			time.Sleep(250 * time.Millisecond)
			continue
		}

		return db, nil
	}

	return nil, fmt.Errorf("db not ready after %s: %w", timeout, lastErr)
}

func pingWithTimeout(ctx context.Context, db *sql.DB, timeout time.Duration) error {
	pingCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return db.PingContext(pingCtx)
}

func testTx(t *testing.T) *sql.Tx {
	t.Helper()

	tx, err := integrationDB.BeginTx(context.Background(), nil)
	require.NoError(t, err, "begin tx")
	t.Cleanup(func() {
		_ = tx.Rollback()
	})
	return tx
}

// testEntClient 返回全局的 ent client，用于测试需要内部管理事务的代码（如 Create/Update 方法）。
// 这类操作会真实提交，因此测试结束后统一截断核心业务表，避免新增外键让后续用例的手工 DELETE 静默失败。
func testEntClient(t *testing.T) *dbent.Client {
	t.Helper()
	t.Cleanup(func() {
		// 只以 users 为根清理其依赖数据，保留迁移预置的默认分组等全局基线。
		_, err := integrationDB.ExecContext(context.Background(), "TRUNCATE users RESTART IDENTITY CASCADE")
		require.NoError(t, err, "清理集成测试提交的数据")
	})
	return integrationEntClient
}

// testEntTx 返回一个 ent 事务，用于需要事务隔离的测试。
// 测试结束后会自动回滚，不会影响数据库状态。
func testEntTx(t *testing.T) *dbent.Tx {
	t.Helper()

	tx, err := integrationEntClient.Tx(context.Background())
	require.NoError(t, err, "begin ent tx")
	t.Cleanup(func() {
		_ = tx.Rollback()
	})
	return tx
}

// testEntSQLTx 已弃用：不要在新测试中使用此函数。
// 基于 *sql.Tx 创建的 ent client 在调用 client.Tx() 时会 panic。
// 对于需要测试内部使用事务的代码，请使用 testEntClient。
// 对于需要事务隔离的测试，请使用 testEntTx。
//
// Deprecated: Use testEntClient or testEntTx instead.
func testEntSQLTx(t *testing.T) (*dbent.Client, *sql.Tx) {
	t.Helper()

	// 直接失败，避免旧测试误用导致的事务嵌套 panic。
	t.Fatalf("testEntSQLTx 已弃用：请使用 testEntClient 或 testEntTx")
	return nil, nil
}

func testRedis(t *testing.T) *redisclient.Client {
	t.Helper()

	prefix := fmt.Sprintf(
		"it:%s:%d:%d:",
		sanitizeRedisNamespace(t.Name()),
		time.Now().UnixNano(),
		atomic.AddUint64(&redisNamespaceSeq, 1),
	)

	opts := *integrationRedis.Options()
	rdb := redisclient.NewClient(&opts)
	rdb.AddHook(prefixHook{prefix: prefix})

	t.Cleanup(func() {
		ctx := context.Background()

		var cursor uint64
		for {
			keys, nextCursor, err := integrationRedis.Scan(ctx, cursor, prefix+"*", 500).Result()
			require.NoError(t, err, "scan redis keys for cleanup")
			if len(keys) > 0 {
				require.NoError(t, integrationRedis.Unlink(ctx, keys...).Err(), "unlink redis keys for cleanup")
			}

			cursor = nextCursor
			if cursor == 0 {
				break
			}
		}

		_ = rdb.Close()
	})

	return rdb
}

func assertTTLWithin(t *testing.T, ttl time.Duration, min, max time.Duration) {
	t.Helper()
	require.GreaterOrEqual(t, ttl, min, "ttl should be >= min")
	require.LessOrEqual(t, ttl, max, "ttl should be <= max")
}

func sanitizeRedisNamespace(name string) string {
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.ReplaceAll(name, " ", "_")
	return name
}

type prefixHook struct {
	prefix string
}

func (h prefixHook) DialHook(next redisclient.DialHook) redisclient.DialHook { return next }

func (h prefixHook) ProcessHook(next redisclient.ProcessHook) redisclient.ProcessHook {
	return func(ctx context.Context, cmd redisclient.Cmder) error {
		h.prefixCmd(cmd)
		return next(ctx, cmd)
	}
}

func (h prefixHook) ProcessPipelineHook(next redisclient.ProcessPipelineHook) redisclient.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redisclient.Cmder) error {
		for _, cmd := range cmds {
			h.prefixCmd(cmd)
		}
		return next(ctx, cmds)
	}
}

func (h prefixHook) prefixCmd(cmd redisclient.Cmder) {
	args := cmd.Args()
	if len(args) < 2 {
		return
	}

	prefixOne := func(i int) {
		if i < 0 || i >= len(args) {
			return
		}

		switch v := args[i].(type) {
		case string:
			if v != "" && !strings.HasPrefix(v, h.prefix) {
				args[i] = h.prefix + v
			}
		case []byte:
			s := string(v)
			if s != "" && !strings.HasPrefix(s, h.prefix) {
				args[i] = []byte(h.prefix + s)
			}
		}
	}

	switch strings.ToLower(cmd.Name()) {
	case "get", "set", "setnx", "setex", "psetex", "incr", "decr", "incrby", "expire", "pexpire", "ttl", "pttl",
		"hgetall", "hget", "hset", "hdel", "hincrbyfloat", "exists",
		"zadd", "zcard", "zrange", "zrangebyscore", "zrem", "zremrangebyscore", "zrevrange", "zrevrangebyscore", "zscore":
		prefixOne(1)
	case "mget":
		for i := 1; i < len(args); i++ {
			prefixOne(i)
		}
	case "del", "unlink":
		for i := 1; i < len(args); i++ {
			prefixOne(i)
		}
	case "eval", "evalsha", "eval_ro", "evalsha_ro":
		if len(args) < 3 {
			return
		}
		numKeys, err := strconv.Atoi(fmt.Sprint(args[2]))
		if err != nil || numKeys <= 0 {
			return
		}
		for i := 0; i < numKeys && 3+i < len(args); i++ {
			prefixOne(3 + i)
		}
	case "scan":
		for i := 2; i+1 < len(args); i++ {
			if strings.EqualFold(fmt.Sprint(args[i]), "match") {
				prefixOne(i + 1)
				break
			}
		}
	}
}

// IntegrationRedisSuite provides a base suite for Redis integration tests.
// Embedding suites should call SetupTest to initialize ctx and rdb.
type IntegrationRedisSuite struct {
	suite.Suite
	ctx context.Context
	rdb *redisclient.Client
}

// SetupTest initializes ctx and rdb for each test method.
func (s *IntegrationRedisSuite) SetupTest() {
	s.ctx = context.Background()
	s.rdb = testRedis(s.T())
}

// RequireNoError is a convenience method wrapping require.NoError with s.T().
func (s *IntegrationRedisSuite) RequireNoError(err error, msgAndArgs ...any) {
	s.T().Helper()
	require.NoError(s.T(), err, msgAndArgs...)
}

// AssertTTLWithin asserts that ttl is within [min, max].
func (s *IntegrationRedisSuite) AssertTTLWithin(ttl, min, max time.Duration) {
	s.T().Helper()
	assertTTLWithin(s.T(), ttl, min, max)
}

// IntegrationDBSuite provides a base suite for DB integration tests.
// Embedding suites should call SetupTest to initialize ctx and client.
type IntegrationDBSuite struct {
	suite.Suite
	ctx    context.Context
	client *dbent.Client
	tx     *dbent.Tx
}

// SetupTest initializes ctx and client for each test method.
func (s *IntegrationDBSuite) SetupTest() {
	s.ctx = context.Background()
	// 统一使用 ent.Tx，确保每个测试都有独立事务并自动回滚。
	tx := testEntTx(s.T())
	s.tx = tx
	s.client = tx.Client()
}

// RequireNoError is a convenience method wrapping require.NoError with s.T().
func (s *IntegrationDBSuite) RequireNoError(err error, msgAndArgs ...any) {
	s.T().Helper()
	require.NoError(s.T(), err, msgAndArgs...)
}
