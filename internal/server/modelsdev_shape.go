package server

import "github.com/linguo2625469/workbuddy2api-panel/internal/upstream"

// applyModelsDevShape 在 /v1/models 条目上「追加」models.dev 形状的能力声明。
//
// 为什么要有这一份：网关原有的声明键（supports_images / supports_tool_call /
// reasoning_supported_efforts / can_disable_thinking …）是上游模型对象的原样透出，
// 语义清楚但**下游读不懂**——只认 models.dev / OpenRouter 词表的客户端（magpie 等）
// 取不到「能不能看图」「能选哪几档思考」，只能退化成纯文本模型。
//
// 三条硬规则（改这个函数时别破坏）：
//  1. 纯追加：只写 models.dev 独有键，绝不覆盖 id/name/object/created/owned_by 与
//     原有能力旗标。原有输出逐字节不变是这次改动的验收前提。
//  2. 同源派生：limit.context / limit.output 直接搬调用方已经算好的 entry 值，本函数
//     **不重算**上下文与输出上限——两处各算一次必然漂移。efforts 同理，由调用方
//     （upstream.EffortListing 的策略结果）传进来。
//  3. 不编造：没有数据源的字段一律不写。models.dev 目录里还有 attachment / cost /
//     open_weights / release_date / knowledge / temperature / family /
//     structured_output，我们一个都拿不到（尤其 cost 没有美元口径，credits 是积分
//     倍率，硬换算就是假数据），因此整体省略，宁可缺字段也不给错值。
//
// imageOut 为 true 时是出图模型：output 模态写 ["image"]，这类条目本来也没有
// context/output，limit 自然省略。
func applyModelsDevShape(entry map[string]any, mi upstream.ModelInfo, efforts []string, imageOut bool) {
	// —— modalities：看图给 input 加 image，出图给 output 写 image ——
	input := []string{"text"}
	if mi.SupportsImages {
		input = []string{"text", "image"}
	}
	output := []string{"text"}
	if imageOut {
		output = []string{"image"}
	}
	entry["modalities"] = map[string]any{"input": input, "output": output}

	// —— limit：只搬运，不计算 ——
	// 上游零值时调用方本就不写 context_length / max_output_tokens，这里跟着省略对应
	// 子键；两个都没有（出图模型）时整个 limit 不出现。
	limit := map[string]any{}
	if v, ok := entry["context_length"]; ok {
		limit["context"] = v
	}
	if v, ok := entry["max_output_tokens"]; ok {
		limit["output"] = v
	}
	if len(limit) > 0 {
		entry["limit"] = limit
	}

	// —— 能力布尔：false 不写（缺省即不支持，与上方原有旗标同口径） ——
	if mi.SupportsToolCall {
		entry["tool_call"] = true
	}
	if mi.SupportsReasoning || len(efforts) > 0 {
		entry["reasoning"] = true
	}

	// —— reasoning_options：档位列表 + 可关闭开关 ——
	// "off"/"none"/"disabled" 是「关闭思考」而不是一个思考档，混进 effort 的 values
	// 会被客户端当成「有个叫 off 的档位」；统一折成独立的 {"type":"toggle"}。
	levels := make([]string, 0, len(efforts))
	disable := mi.CanDisableThinking
	for _, e := range efforts {
		switch e {
		case "off", "none", "disabled":
			disable = true
		default:
			levels = append(levels, e)
		}
	}
	opts := make([]map[string]any, 0, 2)
	if len(levels) > 0 {
		opts = append(opts, map[string]any{"type": "effort", "values": levels})
	}
	if disable {
		opts = append(opts, map[string]any{"type": "toggle"})
	}
	if len(opts) > 0 {
		entry["reasoning_options"] = opts
	}

	// —— supported_reasoning_levels：magpie 的私有方言 ——
	// 这是本函数唯一不属于 models.dev 词表的键：magpie 解析任意厂商 /v1/models 时
	// 只认这个键（liveModel.Levels，接受 [{"effort":"high"}] 也接受 ["high"]），
	// 上面那份 reasoning_options 它不读。
	// 值必须是它 EffortRank 认识的词；「关闭思考」在那边叫 none，故 disable 折成
	// none 而不是 off。出图模型（efforts 为 nil、无 toggle）整键不写。
	if len(levels) > 0 || disable {
		list := make([]map[string]string, 0, len(levels)+1)
		if disable {
			list = append(list, map[string]string{"effort": "none"})
		}
		for _, l := range levels {
			list = append(list, map[string]string{"effort": l})
		}
		entry["supported_reasoning_levels"] = list
	}
}
