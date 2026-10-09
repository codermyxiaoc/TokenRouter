package jsonutil

import (
	"encoding/json"
	"math/big"
	"strconv"
	"strings"
	"testing"
)

// 使用独立有理数实现核对计量解析，覆盖随机小数、指数、边界和类型污染。
func FuzzParseNonNegativeIntExact(f *testing.F) {
	for _, seed := range []string{"0", "-0.0", "1e3", "1.9", "1099511627775.99999", "1099511627776.00001", "1e99999999", `"42"`, "null", "[]"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		raw = strings.TrimSpace(raw)
		if len(raw) > 256 {
			return
		}
		got, ok := ParseNonNegativeInt(raw, 1<<40)
		var parsed any
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		if !json.Valid([]byte(raw)) || decoder.Decode(&parsed) != nil {
			if ok {
				t.Fatalf("invalid JSON accepted: %q", raw)
			}
			return
		}
		if _, number := parsed.(json.Number); !number {
			if ok {
				t.Fatalf("non-number accepted: %q", raw)
			}
			return
		}
		// 只限制参考实现的分配，生产解析器仍先执行并需自行拒绝危险指数。
		if i := strings.IndexAny(raw, "eE"); i >= 0 {
			exponent, err := strconv.ParseInt(raw[i+1:], 10, 32)
			if err != nil || exponent > 4096 || exponent < -4096 {
				return
			}
		}
		value, valid := new(big.Rat).SetString(raw)
		if !valid {
			t.Fatalf("reference rejected valid JSON number: %q", raw)
		}
		wantOK := value.Sign() >= 0 && value.IsInt() && value.Num().Cmp(big.NewInt(1<<40)) <= 0
		if ok != wantOK || (ok && int64(got) != value.Num().Int64()) {
			t.Fatalf("raw=%q got=(%d,%v), reference=%s accepted=%v", raw, got, ok, value.RatString(), wantOK)
		}
	})
}
