package main

import (
	"strings"
	"testing"

	"github.com/Qaraku/luna-agent/internal/pluginprotocol"
)

func TestJSONFormattingIsUsefulAndPreservesNumbersAndDuplicateKeys(t *testing.T) {
	input := "{\"n\":900719925474099312345,\"x\":1,\"x\":2}"
	got, err := (tool{}).Invoke(pluginprotocol.Input{Text: input, Mode: "pretty"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "\n  \"n\": 900719925474099312345") || strings.Count(got, "\"x\"") != 2 {
		t.Fatalf("format changed precision/keys or did not indent: %s", got)
	}
	compact, err := (tool{}).Invoke(pluginprotocol.Input{Text: got, Mode: "compact"})
	if err != nil || compact != input {
		t.Fatalf("compact=%q err=%v", compact, err)
	}
}

func TestJSONFormattingRejectsInvalidAndExcessiveInput(t *testing.T) {
	for _, in := range []pluginprotocol.Input{
		{Text: "not JSON"}, {Text: "{} {}"}, {Text: "{}", Mode: "execute"}, {Text: strings.Repeat(" ", 16385)},
		{Text: strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65)},
	} {
		if _, err := (tool{}).Invoke(in); err == nil {
			t.Errorf("input/mode not rejected (length %d, mode %q)", len(in.Text), in.Mode)
		}
	}
}

func TestLargePrettyOutputIsRejectedButCompactRemainsUsable(t *testing.T) {
	input := strings.Repeat("[", 60) + strings.TrimSuffix(strings.Repeat("0,", 2000), ",") + strings.Repeat("]", 60)
	if len(input) > 16384 {
		t.Fatal("bad fixture")
	}
	if _, err := (tool{}).Invoke(pluginprotocol.Input{Text: input, Mode: "pretty"}); err == nil || !strings.Contains(err.Error(), "65536") {
		t.Fatalf("output limit=%v", err)
	}
	got, err := (tool{}).Invoke(pluginprotocol.Input{Text: input, Mode: "compact"})
	if err != nil || got != input {
		t.Fatalf("compact failed: %v", err)
	}
}
