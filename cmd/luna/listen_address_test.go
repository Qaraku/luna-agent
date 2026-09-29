package main

import (
	"flag"
	"io"
	"testing"
)

func TestListenAddressFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{name: "stable default", want: "127.0.0.1:3210"},
		{name: "explicit ephemeral", args: []string{"-addr", "127.0.0.1:0"}, want: "127.0.0.1:0"},
		{name: "explicit fixed", args: []string{"-addr", "127.0.0.1:43210"}, want: "127.0.0.1:43210"},
		{name: "explicit IPv6", args: []string{"-addr", "[::1]:43210"}, want: "[::1]:43210"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			flags := flag.NewFlagSet("luna", flag.ContinueOnError)
			flags.SetOutput(io.Discard)
			addr := listenAddressFlag(flags)
			if err := flags.Parse(tc.args); err != nil {
				t.Fatal(err)
			}
			if *addr != tc.want {
				t.Fatalf("address = %q, want %q", *addr, tc.want)
			}
		})
	}
}
