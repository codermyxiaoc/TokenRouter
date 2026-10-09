package jsonutil

import "testing"

// 精确计量不能受浮点有效位数、指数溢出或字符串类型影响。
func TestParseNonNegativeIntExact(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{
		{"0", 0}, {"-0.0", 0}, {"42", 42}, {"4.20e1", 42}, {"4200e-2", 42},
		{"1099511627776", 1 << 40}, {"1099511627776.0", 1 << 40}, {"0e99999999999999", 0},
	} {
		got, ok := ParseNonNegativeInt(tc.raw, 1<<40)
		if !ok || got != tc.want {
			t.Errorf("ParseNonNegativeInt(%q) = %d, %v; want %d", tc.raw, got, ok, tc.want)
		}
	}
	for _, raw := range []string{"-1", "1.9", "1099511627775.99999", "1099511627776.00001", "1099511627777", "1e100", "1e-100", "1e999999999999", `"42"`, "null", "true", "NaN", "+1", "01", "1/1"} {
		if got, ok := ParseNonNegativeInt(raw, 1<<40); ok {
			t.Errorf("ParseNonNegativeInt(%q) accepted %d", raw, got)
		}
	}
}
