package repository

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/TokenFlux/TokenRouter/migrations"
	"github.com/stretchr/testify/require"
)

func TestMigrationChecksumCompatibility_PublishedWindowsNewlines(t *testing.T) {
	// 用真实嵌入内容复核两个历史发布哈希，避免兼容列表意外掩盖 SQL 内容变化。
	cases := []struct {
		name string
		lf   string
		crlf string
	}{
		{"286_content_moderation_engine_meta.sql", "32072ca5a51ca04f68f4c9e41fde7901d8711c0dd745f1b3e4a93f2ae878d3a7", "ce84191f20247ccc03e386f25f43317a5b8f9af6d1ce623e86902a17219f1efa"},
		{"288_affiliate_ledger_operation_id.sql", "3823bfea5f64feb58f5fcebc341c6877eaefc97834b4cd652c8e83ad08ed78da", "3ea23cf05adc7578dad4f0fe5da94870ef741173fac6462d94b863d273649753"},
	}
	checksum := func(content string) string {
		return fmt.Sprintf("%x", sha256.Sum256([]byte(strings.TrimSpace(content))))
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content, err := migrations.FS.ReadFile(tc.name)
			require.NoError(t, err)
			lf := strings.ReplaceAll(string(content), "\r\n", "\n")
			require.Equal(t, tc.lf, checksum(lf))
			require.Equal(t, tc.crlf, checksum(strings.ReplaceAll(lf, "\n", "\r\n")))
			require.True(t, isMigrationChecksumCompatible(tc.name, tc.crlf, tc.lf))
			require.True(t, isMigrationChecksumCompatible(tc.name, tc.lf, tc.crlf))

			// 文件内容、数据库记录或迁移名任一不属于对应已知版本，都必须拒绝。
			changed := checksum(lf + "\nSELECT 1;")
			require.False(t, isMigrationChecksumCompatible(tc.name, tc.crlf, changed))
			require.False(t, isMigrationChecksumCompatible(tc.name, changed, tc.lf))
			require.False(t, isMigrationChecksumCompatible("296_add_payment_order_bonus_amount.sql", tc.crlf, tc.lf))
			for _, other := range cases {
				if other.name != tc.name {
					require.False(t, isMigrationChecksumCompatible(tc.name, other.crlf, tc.lf))
					require.False(t, isMigrationChecksumCompatible(tc.name, tc.crlf, other.lf))
				}
			}
		})
	}
}
