package plugin

import (
	"errors"
	"testing"
)

type publishStub struct{ id, namespace string }

func (p publishStub) Descriptor() Descriptor {
	return Descriptor{ID: p.id, Title: p.id, Deployment: DeploymentBuiltin, Claims: []Claim{{Kind: ClaimStateNamespace, ID: p.namespace}}, Permissions: []Permission{{Kind: PermissionStateWrite}}}
}
func TestPublicationPersistsBeforeReplacingAndKeepsOldOnFailure(t *testing.T) {
	r := NewRegistry(PermissionStateWrite)
	old := publishStub{"example", "old"}
	if err := r.Register(old); err != nil {
		t.Fatal(err)
	}
	if err := r.Enable("example"); err != nil {
		t.Fatal(err)
	}
	before := r.Revision()
	disk := errors.New("disk unavailable")
	if err := r.Publish(publishStub{"example", "new"}, StateEnabled, func() error { return disk }); !errors.Is(err, disk) {
		t.Fatalf("publication=%v", err)
	}
	entry, _ := r.Entry("example")
	if entry.Descriptor.Claims[0].ID != "old" || r.Revision() != before {
		t.Fatal("failed persistence changed the registry")
	}
	committed := false
	if err := r.Publish(publishStub{"example", "new"}, StateEnabled, func() error { committed = true; return nil }); err != nil {
		t.Fatal(err)
	}
	entry, _ = r.Entry("example")
	if !committed || entry.Descriptor.Claims[0].ID != "new" || entry.State != StateEnabled {
		t.Fatal("new version not published")
	}
	if err := r.Register(publishStub{"other", "old"}); err != nil {
		t.Fatal("old claim was not released", err)
	}
}
func TestPublicationConflictDoesNotRunCommitAndRemovalIsTransactional(t *testing.T) {
	r := NewRegistry(PermissionStateWrite)
	r.Register(publishStub{"one", "shared"})
	r.Register(publishStub{"two", "second"})
	committed := false
	if err := r.Publish(publishStub{"two", "shared"}, StateEnabled, func() error { committed = true; return nil }); err == nil || committed {
		t.Fatal("conflict reached persistence")
	}
	if err := r.Remove("one", func() error { return errors.New("blocked") }); err == nil {
		t.Fatal("failed removal succeeded")
	}
	if _, ok := r.Entry("one"); !ok {
		t.Fatal("failed removal lost the entry")
	}
	if err := r.Remove("one", func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.Entry("one"); ok {
		t.Fatal("removed entry remains")
	}
	if err := r.Register(publishStub{"three", "shared"}); err != nil {
		t.Fatal("removed claim remains", err)
	}
}
