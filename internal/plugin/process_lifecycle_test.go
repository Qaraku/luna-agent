package plugin

import "testing"

func TestProcessCapabilityCanBeEnabledAndDisabledWithoutChangingDeployment(t *testing.T) {
	registry := NewRegistry()
	if err := registry.Register(barePlugin{d: desc("json-format", DeploymentProcess)}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Enable("json-format"); err != nil {
		t.Fatalf("enable process capability: %v", err)
	}
	if err := registry.Disable("json-format"); err != nil {
		t.Fatal(err)
	}
	entry, _ := registry.Entry("json-format")
	if entry.State != StateDisabled || entry.Descriptor.Deployment != DeploymentProcess {
		t.Fatalf("entry=%+v", entry)
	}
}
