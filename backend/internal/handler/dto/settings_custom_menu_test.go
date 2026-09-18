package dto

import (
	"encoding/json"
	"testing"
)

func TestCustomMenuHideOpenButtonRoundTrip(t *testing.T) {
	// 新展示字段必须经过保存和公开过滤；后台专用菜单仍不可见。
	raw := `[{"id":"old","visibility":"user"},{"id":"hidden","visibility":"user","hide_open_button":true},{"id":"admin","visibility":"admin","hide_open_button":true}]`
	items := ParseCustomMenuItems(raw)
	if len(items) != 3 || items[0].HideOpenButton || !items[1].HideOpenButton {
		t.Fatalf("unexpected custom menu settings: %+v", items)
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	public := ParseUserVisibleMenuItems(string(encoded))
	if len(public) != 2 || public[0].ID != "old" || public[0].HideOpenButton || public[1].ID != "hidden" || !public[1].HideOpenButton {
		t.Fatalf("unexpected public menus: %+v", public)
	}
}
