package memory

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"
)

// recallOptions 既声明公开参数，也保存本次调用的匹配器；匹配器不进入 schema，
// 不缓存到工具或 Store。offset 从较新端计数，页内仍按文件顺序从旧到新排列。
type recallOptions struct {
	Query  string `json:"query,omitempty" jsonschema_description:"Optional case-insensitive literal substring of fact text, not a regex; empty means all effective facts"`
	Offset int    `json:"offset,omitempty" jsonschema:"minimum=0" jsonschema_description:"Number of newer matching facts to skip, default 0; use next_offset to read older matches"`
	Limit  int    `json:"limit,omitempty" jsonschema:"minimum=1,maximum=50" jsonschema_description:"Maximum facts on this page, 1..50, default 50"`

	pattern *regexp.Regexp
}

func parseRecallOptions(arguments string) (recallOptions, error) {
	options := recallOptions{Limit: MaxRecallFacts}
	if strings.TrimSpace(arguments) == "" {
		arguments = "{}"
	}
	// RawMessage 区分“未提供”和显式 null；保留历史上的根 null 默认调用，
	// 但新增字段必须符合自己的类型，不能让 null 悄悄变成 0 或空串。
	var raw struct {
		Query  json.RawMessage `json:"query"`
		Offset json.RawMessage `json:"offset"`
		Limit  json.RawMessage `json:"limit"`
	}
	if err := decodeOne(arguments, &raw); err != nil {
		return options, err
	}
	for _, field := range []struct {
		name string
		raw  json.RawMessage
		into any
	}{{"query", raw.Query, &options.Query}, {"offset", raw.Offset, &options.Offset}, {"limit", raw.Limit, &options.Limit}} {
		if len(field.raw) == 0 {
			continue
		}
		if string(field.raw) == "null" {
			return options, fmt.Errorf("%s must not be null; omit it to use the default", field.name)
		}
		if err := json.Unmarshal(field.raw, field.into); err != nil {
			return options, fmt.Errorf("%s: %w", field.name, err)
		}
	}
	if options.Offset < 0 {
		return options, fmt.Errorf("offset must be non-negative (got %d)", options.Offset)
	}
	if options.Limit < 1 || options.Limit > MaxRecallFacts {
		return options, fmt.Errorf("limit must be 1..%d (got %d)", MaxRecallFacts, options.Limit)
	}
	if options.Query != "" {
		// 转义后再启用 Unicode 大小写折叠：.*、[ 等仍是字面文本，不是模式。
		pattern, err := regexp.Compile("(?i)" + regexp.QuoteMeta(options.Query))
		if err != nil {
			return options, fmt.Errorf("prepare the literal query: %w", err)
		}
		options.pattern = pattern
	}
	return options, nil
}

func recallPage(facts []Fact, options recallOptions) string {
	if options.Query == "" && options.Offset == 0 && options.Limit == MaxRecallFacts {
		return recallListing(facts)
	}
	matches := facts
	if options.pattern != nil {
		matches = make([]Fact, 0, len(facts))
		for _, fact := range facts {
			if options.pattern.MatchString(fact.Text) {
				matches = append(matches, fact)
			}
		}
	}
	// 先夹住 offset 再做减法，极大的合法 offset 也是空页，而不是整数溢出。
	skipped := min(options.Offset, len(matches))
	end := len(matches) - skipped
	start := max(0, end-options.Limit)
	kept := matches[start:end]
	var b strings.Builder
	fmt.Fprintf(&b, "stored facts in effect: %d; matching facts: %d; returned: %d\n", len(facts), len(matches), len(kept))
	fmt.Fprintf(&b, "offset: %d; limit: %d; skipped newer matches: %d; remaining older matches: %d\n", options.Offset, options.Limit, skipped, start)
	if options.Query != "" {
		b.WriteString("match mode: case-insensitive literal text\n")
	}
	switch {
	case len(facts) == 0:
		b.WriteString(recallEmpty + "\n")
	case len(matches) == 0:
		b.WriteString("no facts match this query\n")
	case len(kept) == 0:
		b.WriteString("no facts on this page: offset is at or beyond the matching facts\n")
	}
	for _, fact := range kept {
		fmt.Fprintf(&b, "- %s (recorded %s)\n", singleLine(fact.Text), fact.At.UTC().Format(time.RFC3339Nano))
	}
	if start > 0 {
		fmt.Fprintf(&b, "page stopped at limit %d; keep the same query to continue\nnext_offset: %d\n", options.Limit, options.Offset+len(kept))
	} else {
		b.WriteString("next_offset: none\n")
	}
	b.WriteString("Each call reads the current effective facts; changes may shift offsets.\n")
	return b.String()
}
