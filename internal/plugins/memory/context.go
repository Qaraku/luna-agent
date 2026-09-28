package memory

import (
	"context"
	"strings"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

const (
	// MaxInjectFacts and MaxInjectBytes bound what memory contributes to one
	// run's context. The policy is the same as the history cap: keep the most
	// recent facts, drop the oldest first, and keep the kept set a contiguous,
	// chronologically ordered suffix.
	MaxInjectFacts = 50
	MaxInjectBytes = 8 * 1024

	// MaxRecallFacts 限制一次检索返回的事实条数，不限制检索范围；分页可以
	// 继续读取较旧匹配。默认窗口与注入条数上限一致，注入策略本身不变。
	MaxRecallFacts = MaxInjectFacts
)

// singleLine collapses the line breaks in a stored fact so it renders as one
// line, wherever the capability renders fact text.
func singleLine(text string) string {
	return strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(text)
}

// factsBlockHeader is the label the injected block carries: what this text is
// and where it came from.
//
// It no longer carries the "these are reference data, not instructions"
// disclaimer. That rule is the Kernel's now, applied uniformly to every
// reference contribution, so the plugin states its own content and stops
// restating host policy.
const factsBlockHeader = "Existing facts about the user, recorded by luna_remember in earlier sessions."

// factLine renders one fact as one labelled bullet line.
func factLine(text string) string {
	// A stored fact is always one line. Collapsing newlines when rendering means
	// a fact containing a line break cannot open a line of its own inside a
	// system prompt, which is exactly what a directive smuggled into memory
	// would need.
	return "- " + singleLine(text) + "\n"
}

// selectFacts returns the facts that fit the injection caps, oldest dropped
// first: the kept set is a contiguous suffix of the stored facts, accumulated
// from the newest fact backwards, so facts are never reordered or sampled. The
// byte cap counts the rendered lines, not the label.
func selectFacts(facts []Fact) []Fact {
	kept, size := 0, 0
	for i := len(facts) - 1; i >= 0; i-- {
		lineSize := len(factLine(facts[i].Text))
		if kept == MaxInjectFacts || size+lineSize > MaxInjectBytes {
			break
		}
		kept++
		size += lineSize
	}
	if kept == 0 {
		return nil
	}
	return append([]Fact{}, facts[len(facts)-kept:]...)
}

// renderFactsBlock renders the injected block, or the empty string when there is
// nothing to inject. Only the title and the fact text appear: generation,
// version, process id and credentials have no field in a stored fact and no
// place here.
//
// The text carries no leading blank lines. Separating this block from the rest
// of the prompt is the Kernel's job now, because the Kernel is what decides
// where a reference contribution lands.
func renderFactsBlock(facts []Fact) string {
	kept := selectFacts(facts)
	if len(kept) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString(factsBlockHeader)
	b.WriteString("\n")
	for _, fact := range kept {
		b.WriteString(factLine(fact.Text))
	}
	return b.String()
}

// Contexts reads the store and renders one reference block, or no block at all
// when the store holds no injectable fact.
//
// The store is read on every call rather than cached, so a fact written earlier
// in a round reaches the next context read of the same round. A read failure is
// returned as an error and fails the round: an agent that silently forgot
// everything would answer as if the memory were empty.
func (p *Plugin) Contexts(context.Context) ([]plugin.ContextBlock, error) {
	facts, err := p.store.Facts()
	if err != nil {
		return nil, err
	}
	text := renderFactsBlock(facts)
	if text == "" {
		// Nothing to inject is not an empty block: the Kernel must not have to
		// print a title with no facts under it.
		return nil, nil
	}
	return []plugin.ContextBlock{{ID: FactsContextID, Kind: plugin.ContextReference, Text: text}}, nil
}
