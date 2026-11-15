package panel

import "testing"

// 国际版任务动作必须全部登记进 autoActions：缺一条就会出现「任务列表里有、
// 一键完成里没有」的半成品状态（前端按这张表渲染按钮）。
func TestGlobalTaskActionsRegistered(t *testing.T) {
	for _, code := range []string{"first_chat", "skill_installed", "template_used", "expert_summoned"} {
		act := autoActionFor(code)
		if act == nil {
			t.Fatalf("autoActionFor(%q) = nil, want registered action", code)
		}
		if act.run == nil {
			t.Errorf("action %q has nil run", code)
		}
		if act.Desc == "" {
			t.Errorf("action %q has empty description", code)
		}
	}
	// 国内码的动作不能被国际版条目覆盖（两者 code 不同，顺序也不应影响查表）。
	if act := autoActionFor("chat_5"); act == nil || act.run == nil {
		t.Fatalf("domestic action chat_5 missing after adding global actions")
	}
}
