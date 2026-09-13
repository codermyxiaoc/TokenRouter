package service

// stringValueOrEmpty 返回字符串指针的值，空指针返回空字符串。
func stringValueOrEmpty(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
