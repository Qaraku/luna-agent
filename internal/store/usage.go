package store

// Usage 是一次运行已报告的累计用量，不是估算的上下文占用或费用。
// 缓存和推理是输入/输出的分项，不再次加入 TotalTokens。
// Complete 只在已观察调用均有有效报告且正常结束时为真；nil Usage 表示完全未知。
type Usage struct {
	InputTokens     int `json:"input_tokens"`
	OutputTokens    int `json:"output_tokens"`
	TotalTokens     int `json:"total_tokens"`
	CachedTokens    int `json:"cached_tokens,omitempty"`
	ReasoningTokens int `json:"reasoning_tokens,omitempty"`
	// ModelCalls 统计已观察到输出流/消息的调用；失败前未返回输出的请求无法计数。
	ModelCalls    int  `json:"model_calls"`
	ReportedCalls int  `json:"reported_calls"`
	Complete      bool `json:"complete"`
}
