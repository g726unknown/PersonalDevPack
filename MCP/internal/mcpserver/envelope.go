package mcpserver

// Status 是统一信封的状态字段值域。
// schema 层不做 enum 硬校验（jsonschema tag 只提供描述），
// 值域执法靠 handler 只填这里的常量。
type Status string

const (
	StatusOK       Status = "ok"
	StatusPartial  Status = "partial"
	StatusConflict Status = "conflict"
	StatusRejected Status = "rejected"
	StatusNotFound Status = "not_found"
)

// envelope 是所有工具 Out 的公共字段，以嵌入复用。
// 探针已验证：SDK 的 schema 生成对嵌入字段做平铺（无 allOf/$ref），
// status/summary/warnings 与各工具自己的字段同级出现在 output schema；
// omitempty 字段自动不进 required，无值时输出里干脆不出现。
//
// Summary 口径：给 LLM 的一句话结果概述，数字优先
// （如 "3 packages recorded, 1 removed"）；
// 结论、判定、建议不进 Summary——那是调用方结合上下文的事。
type envelope struct {
	Status      Status   `json:"status" jsonschema:"ok | partial | conflict | rejected | not_found"`
	Summary     string   `json:"summary"`
	Warnings    []string `json:"warnings,omitempty"`
	ManualSteps []string `json:"manual_steps,omitempty"`
}
