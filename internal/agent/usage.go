package agent

import (
	"context"

	"github.com/Qaraku/luna-agent/internal/store"
	"github.com/cloudwego/eino/schema"
)

type usageCall struct {
	report *store.Usage
	done   bool
}
type runUsage struct {
	calls []usageCall
	sent  *store.Usage
}

func (u *runUsage) begin() { u.calls = append(u.calls, usageCall{}) }

// 同一流里的多个 usage 都是该调用的累计快照，只替换、不累加。
func (u *runUsage) observe(raw *schema.TokenUsage) {
	if raw == nil || len(u.calls) == 0 {
		return
	}
	maxInt := int(^uint(0) >> 1)
	var report *store.Usage
	if raw.PromptTokens >= 0 && raw.CompletionTokens >= 0 && raw.PromptTokenDetails.CachedTokens >= 0 && raw.CompletionTokensDetails.ReasoningTokens >= 0 && raw.PromptTokens <= maxInt-raw.CompletionTokens {
		report = &store.Usage{InputTokens: raw.PromptTokens, OutputTokens: raw.CompletionTokens, TotalTokens: raw.PromptTokens + raw.CompletionTokens, CachedTokens: raw.PromptTokenDetails.CachedTokens, ReasoningTokens: raw.CompletionTokensDetails.ReasoningTokens}
	}
	u.calls[len(u.calls)-1].report = report
}
func (u *runUsage) finish(ctx context.Context, id string) {
	if len(u.calls) > 0 {
		u.calls[len(u.calls)-1].done = true
	}
	u.emit(ctx, id, true)
}
func (u *runUsage) snapshot(success bool) *store.Usage {
	total := store.Usage{ModelCalls: len(u.calls), Complete: success}
	maxInt := int(^uint(0) >> 1)
	for _, call := range u.calls {
		r := call.report
		if r == nil {
			total.Complete = false
			continue
		}
		if total.InputTokens > maxInt-r.InputTokens || total.OutputTokens > maxInt-r.OutputTokens || total.TotalTokens > maxInt-r.TotalTokens || total.CachedTokens > maxInt-r.CachedTokens || total.ReasoningTokens > maxInt-r.ReasoningTokens {
			total.Complete = false
			continue
		}
		total.InputTokens += r.InputTokens
		total.OutputTokens += r.OutputTokens
		total.TotalTokens += r.TotalTokens
		total.CachedTokens += r.CachedTokens
		total.ReasoningTokens += r.ReasoningTokens
		total.ReportedCalls++
		if !call.done {
			total.Complete = false
		}
	}
	if total.ReportedCalls == 0 {
		return nil
	}
	return &total
}
func (u *runUsage) emit(ctx context.Context, id string, success bool) *store.Usage {
	snapshot := u.snapshot(success)
	if snapshot != nil && (u.sent == nil || *u.sent != *snapshot) {
		emit(ctx, Event{Type: "usage.updated", Data: UsageUpdated{RunID: id, Scope: "run", Usage: *snapshot}})
		u.sent = snapshot
	}
	return snapshot
}
