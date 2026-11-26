package server

import "testing"

// 同域同名的优惠条目必须补标签：上游 hy4-flash-free 与 hy4-flash 显示名都是
// 「Hy4 flash」，一个 0 分（限时免费）一个 x0.03；只认 name 的客户端里两行长一样，
// 选了免费那行的可能实际发出付费 id。
func TestDisambiguatePromoNames(t *testing.T) {
	t.Run("同名兄弟只补带标签的那条，id 不动", func(t *testing.T) {
		list := []map[string]any{
			{"id": "cn:hy4flash-free", "name": "Hy4 flash", "promo_label": "限时免费"},
			{"id": "cn:hy4flash", "name": "Hy4 flash"},
		}
		disambiguatePromoNames(list)
		if got := list[0]["name"]; got != "Hy4 flash（限时免费）" {
			t.Fatalf("免费条目 name = %#v，期望补标签", got)
		}
		if got := list[1]["name"]; got != "Hy4 flash" {
			t.Fatalf("兄弟条目 name = %#v，期望保持原样（只改一条就够区分）", got)
		}
		if got := list[0]["id"]; got != "cn:hy4flash-free" {
			t.Fatalf("id 被改动 = %#v（路由与计费只看 id，绝不允许改）", got)
		}
	})

	t.Run("名字唯一不补", func(t *testing.T) {
		list := []map[string]any{{"id": "cn:glm-5.2", "name": "GLM-5.2", "promo_label": "夜间折扣"}}
		disambiguatePromoNames(list)
		if got := list[0]["name"]; got != "GLM-5.2" {
			t.Fatalf("唯一名被改名 = %#v", got)
		}
	})

	t.Run("跨域同名不补", func(t *testing.T) {
		list := []map[string]any{
			{"id": "cn:hy4flash", "name": "Hy4 flash", "promo_label": "限时免费"},
			{"id": "global:hy4flash", "name": "Hy4 flash"},
		}
		disambiguatePromoNames(list)
		if got := list[0]["name"]; got != "Hy4 flash" {
			t.Fatalf("跨域同名被改名 = %#v（id 前缀本来就不同）", got)
		}
	})

	t.Run("无优惠标签不补", func(t *testing.T) {
		list := []map[string]any{
			{"id": "cn:a", "name": "同名"},
			{"id": "cn:b", "name": "同名"},
		}
		disambiguatePromoNames(list)
		for i, e := range list {
			if e["name"] != "同名" {
				t.Fatalf("第 %d 条无标签条目被改名 = %#v", i, e["name"])
			}
		}
	})

	t.Run("同名带不同标签全部补，无标签的不动", func(t *testing.T) {
		list := []map[string]any{
			{"id": "cn:a", "name": "同名", "promo_label": "限时免费"},
			{"id": "cn:b", "name": "同名", "promo_label": "夜间折扣"},
			{"id": "cn:c", "name": "同名"},
		}
		disambiguatePromoNames(list)
		if got := list[0]["name"]; got != "同名（限时免费）" {
			t.Fatalf("第一条 = %#v", got)
		}
		if got := list[1]["name"]; got != "同名（夜间折扣）" {
			t.Fatalf("第二条 = %#v", got)
		}
		if got := list[2]["name"]; got != "同名" {
			t.Fatalf("无标签兄弟被改名 = %#v（应保持官方名）", got)
		}
	})
}
