package jsonutil

import (
	"encoding/json"
	"strconv"
	"strings"
)

// ParseNonNegativeInt 精确解析有界非负 JSON 整数，避免浮点舍入把小数或越界数变成计量。
// 数学上为整数的十进制和科学计数保持兼容；调用方负责数字字符串的解包。
func ParseNonNegativeInt(raw string, maxValue int) (int, bool) {
	raw = strings.TrimSpace(raw)
	if maxValue < 0 || len(raw) == 0 || len(raw) > 256 || (raw[0] != '-' && (raw[0] < '0' || raw[0] > '9')) || !json.Valid([]byte(raw)) {
		return 0, false
	}
	negative := raw[0] == '-'
	if negative {
		raw = raw[1:]
	}
	mantissa, exponentText := raw, ""
	if index := strings.IndexAny(raw, "eE"); index >= 0 {
		mantissa, exponentText = raw[:index], raw[index+1:]
	}
	fractionDigits := 0
	if index := strings.IndexByte(mantissa, '.'); index >= 0 {
		fractionDigits = len(mantissa) - index - 1
		mantissa = mantissa[:index] + mantissa[index+1:]
	}
	digits := strings.TrimLeft(mantissa, "0")
	if digits == "" {
		return 0, true
	}
	if negative {
		return 0, false
	}
	exponent := int64(0)
	if exponentText != "" {
		var err error
		exponent, err = strconv.ParseInt(exponentText, 10, 32)
		if err != nil {
			return 0, false
		}
	}
	exponent -= int64(fractionDigits)
	if exponent < 0 {
		if -exponent >= int64(len(digits)) {
			return 0, false
		}
		cut := len(digits) + int(exponent)
		if strings.Trim(digits[cut:], "0") != "" {
			return 0, false
		}
		digits = digits[:cut]
	} else {
		// 先比较位数，超长指数不能触发巨额分配或整数溢出。
		if int64(len(digits))+exponent > int64(len(strconv.Itoa(maxValue))) {
			return 0, false
		}
		digits += strings.Repeat("0", int(exponent))
	}
	value, err := strconv.ParseUint(digits, 10, strconv.IntSize)
	if err != nil || value > uint64(maxValue) {
		return 0, false
	}
	return int(value), true
}
