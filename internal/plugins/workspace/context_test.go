package workspace

import (
	"context"
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/plugin"
)

// The capability contributes exactly one block, of the reference kind, saying
// which project this session is in and what the paths are relative to. Anything
// the Kernel adds around it (the source label, the framing, the budget) is the
// Kernel's, so it is deliberately absent from this text.
func TestContextsRendersOneReferenceBlockNamingTheProject(t *testing.T) {
	p := newPlugin(t, projectRoot(t))
	blocks, err := p.Contexts(context.Background())
	if err != nil {
		t.Fatalf("Contexts: %v", err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks=%+v, want exactly one", blocks)
	}
	block := blocks[0]
	if block.ID != ProjectContextID || block.Kind != plugin.ContextReference {
		t.Fatalf("block=%+v, want the declared reference contribution", block)
	}
	for _, want := range []string{projectBlockHeader, "Project: luna-agent", "read root the file tools are bounded to", "relative to it"} {
		if !strings.Contains(block.Text, want) {
			t.Fatalf("block does not contain %q:\n%s", want, block.Text)
		}
	}
	if strings.HasPrefix(block.Text, "\n") || strings.HasSuffix(block.Text, "\n\n") {
		t.Fatalf("the block carries its own spacing: %q", block.Text)
	}
}

// The model-visible text never names a host path: another machine's layout must
// not become part of every run, and it is not information the model needs. The
// same convention governs the file tool's description.
func TestTheBlockCarriesNoHostPath(t *testing.T) {
	root := projectRoot(t)
	text := newPlugin(t, root).Render()
	for _, forbidden := range []string{root, strings.TrimSuffix(root, "luna-agent")} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("the block carries the host path %q:\n%s", forbidden, text)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "/") {
			t.Fatalf("the block has an absolute path line %q:\n%s", line, text)
		}
	}
}

// An identity statement carries identity, not metadata: no serving generation,
// version, process id or file path of a plugin process can appear here, because
// this capability has no process and holds no such field.
func TestTheBlockCarriesNoProcessIdentity(t *testing.T) {
	text := newPlugin(t, projectRoot(t)).Render()
	for _, forbidden := range []string{"pid", "process", "generation", "version", "proc"} {
		if strings.Contains(strings.ToLower(text), forbidden) {
			t.Fatalf("the block mentions %q:\n%s", forbidden, text)
		}
	}
}

// The block must fit the budget the descriptor asks the Kernel for: an identity
// sentence cut at the last complete line would tell the model something the
// capability never said.
func TestTheBlockFitsTheBudgetDeclaredForIt(t *testing.T) {
	declared := 0
	for _, c := range Descriptor().Contributions {
		if c.Kind == plugin.ContributionContext && c.ID == ProjectContextID {
			declared = c.BudgetBytes
		}
	}
	if declared != ProjectBudgetBytes {
		t.Fatalf("declared budget=%d, want %d", declared, ProjectBudgetBytes)
	}
	text := newPlugin(t, projectRoot(t)).Render()
	if len(text) == 0 || len(text) > declared {
		t.Fatalf("block size=%d, want 1..%d bytes", len(text), declared)
	}
}

// Two context reads inside one run render the same block: identity is fixed
// when the capability is constructed, so a capability that cannot change
// between reads cannot silently contribute nothing on the second one.
func TestTwoReadsOfTheSameRunRenderTheSameBlock(t *testing.T) {
	p := newPlugin(t, projectRoot(t))
	first, err := p.Contexts(context.Background())
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	second, err := p.Contexts(context.Background())
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	if len(first) != 1 || len(second) != 1 || first[0] != second[0] {
		t.Fatalf("first=%+v second=%+v, want the same single block", first, second)
	}
}
