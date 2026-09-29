package maintenance

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestVersionNeedsNoConfigurationOrAssets(t *testing.T) {
	t.Setenv("LUNA_HOME", "relative-invalid-home")
	var out bytes.Buffer
	handled, err := Handle([]string{"version"}, &out)
	if !handled || err != nil {
		t.Fatal(handled, err)
	}
	var value map[string]any
	if err = json.Unmarshal(out.Bytes(), &value); err != nil {
		t.Fatal(err)
	}
	if value["data_schema"] != float64(1) || value["version"] == "" {
		t.Fatal(value)
	}
}
func TestMaintenanceRejectsUnknownActionWithoutStartingServer(t *testing.T) {
	handled, err := Handle([]string{"release", "apply"}, &bytes.Buffer{})
	if !handled || err == nil {
		t.Fatal("unknown maintenance command passed through")
	}
	handled, err = Handle([]string{"-addr", "127.0.0.1:0"}, &bytes.Buffer{})
	if handled || err != nil {
		t.Fatal("server flags intercepted")
	}
}
