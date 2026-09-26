package plugin

import (
	"fmt"
	"path/filepath"
)

// StateDirFor resolves a descriptor's claimed state namespace into an absolute
// path: the state root plus the namespace directory name.
//
// It is a pure function over a descriptor rather than a method on the registry,
// because the path is needed before the plugin is constructed — a plugin whose
// store lives on disk cannot be built without knowing where to put it. The
// kernel hands over this directory and never learns which file the plugin keeps
// inside it; otherwise "the memory file is called memory.jsonl" would be a
// kernel detail again. The returned path is not created: the capability that needs
// it is responsible for that.
func StateDirFor(d Descriptor, root string) (string, error) {
	if !filepath.IsAbs(root) {
		return "", fmt.Errorf("state root %q must be absolute", root)
	}
	for _, c := range d.Claims {
		if c.Kind == ClaimStateNamespace {
			return filepath.Join(root, c.ID), nil
		}
	}
	return "", fmt.Errorf("plugin %q claims no state namespace", d.ID)
}
