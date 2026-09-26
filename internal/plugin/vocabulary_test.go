package plugin

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The kernel must not understand what any capability means. That is the test of
// whether a capability was really extracted rather than renamed: if the kernel
// still needs to know what a stored fact is, the boundary moved back.
//
// The scan covers the kernel's own packages and skips three things on purpose:
//   - internal/plugins/**, which is where capabilities live and where this
//     vocabulary belongs;
//   - cmd/, the composition root, whose job is to name the capabilities it
//     builds — naming one is not the same as understanding it;
//   - this file, which has to spell out what it forbids.
func TestKernelHasNoCapabilityVocabulary(t *testing.T) {
	forbidden := regexp.MustCompile(`(?i)\bluna_remember\b|\bremember(s|ed|ing)?\b|\bretract(s|ed|ing|ion|ions)?\b|\bfacts?\b`)
	const self = "vocabulary_test.go"
	var offenders []string

	err := filepath.WalkDir("..", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "plugins" {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || filepath.Base(path) == self {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(data), "\n") {
			if forbidden.MatchString(line) {
				offenders = append(offenders, fmt.Sprintf("%s:%d: %s", path, i+1, strings.TrimSpace(line)))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk: %v", err)
	}
	if len(offenders) > 0 {
		t.Fatalf("the kernel knows capability vocabulary again:\n%s", strings.Join(offenders, "\n"))
	}
}
