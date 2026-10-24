package setup

import (
	"context"
	"database/sql"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 无密码的本机/托管信任连接仍必须连接指定数据库，不能回落到用户名对应的库。
func TestBuildPostgresDSN_DriverSendsConfiguredDatabase(t *testing.T) {
	for _, tc := range []struct{ name, password, target string }{
		{"no_password", "", "isolated_app"},
		{"with_password", "fake-test-password", "isolated_app"},
		{"bootstrap_database", "", "postgres"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			require.NoError(t, err)
			defer listener.Close()
			startup := make(chan []byte, 1)
			go func() {
				conn, acceptErr := listener.Accept()
				if acceptErr != nil {
					startup <- nil
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
				// 读取真实 pq 驱动的启动包，然后关闭连接，不模拟认证成功。
				var size uint32
				if binary.Read(conn, binary.BigEndian, &size) != nil || size < 8 || size > 8192 {
					startup <- nil
					return
				}
				payload := make([]byte, size-4)
				if _, readErr := io.ReadFull(conn, payload); readErr != nil {
					startup <- nil
					return
				}
				startup <- payload[4:]
			}()
			cfg := &DatabaseConfig{Host: "127.0.0.1", Port: listener.Addr().(*net.TCPAddr).Port, User: "postgres", Password: tc.password, DBName: "isolated_app", SSLMode: "disable"}
			db, err := sql.Open("postgres", buildPostgresDSN(cfg, tc.target))
			require.NoError(t, err)
			defer db.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			require.Error(t, db.PingContext(ctx))
			select {
			case payload := <-startup:
				require.NotEmpty(t, payload)
				fields := strings.Split(string(payload), "\x00")
				values := map[string]string{}
				for i := 0; i+1 < len(fields); i += 2 {
					values[fields[i]] = fields[i+1]
				}
				require.Equal(t, tc.target, values["database"])
				require.Equal(t, "postgres", values["user"])
			case <-ctx.Done():
				t.Fatal("数据库驱动未发送启动参数")
			}
		})
	}
}
