package plugin

import "testing"

// TestTransition 逐对穷举 4 个状态 × 3 个事件的迁移。want 表就是生命周期契约本身：
// to 是期望得到的（非法时为原状态），ok 为 false 的每一行都必须返回错误。
func TestTransition(t *testing.T) {
	states := []State{StateRegistered, StateEnabled, StateDisabled, StateFailed}
	events := []Event{EventEnable, EventDisable, EventFail}

	want := map[string]struct {
		to State
		ok bool
	}{
		"registered|enable":  {to: StateEnabled, ok: true},
		"registered|disable": {to: StateRegistered, ok: false},
		"registered|fail":    {to: StateFailed, ok: true},

		"enabled|enable":  {to: StateEnabled, ok: false},
		"enabled|disable": {to: StateDisabled, ok: true},
		"enabled|fail":    {to: StateFailed, ok: true},

		"disabled|enable":  {to: StateEnabled, ok: true},
		"disabled|disable": {to: StateDisabled, ok: false},
		"disabled|fail":    {to: StateFailed, ok: true},

		"failed|enable":  {to: StateFailed, ok: false},
		"failed|disable": {to: StateFailed, ok: false},
		"failed|fail":    {to: StateFailed, ok: true},
	}

	if len(want) != len(states)*len(events) {
		t.Fatalf("expectation table covers %d pairs, want %d", len(want), len(states)*len(events))
	}

	for _, from := range states {
		for _, e := range events {
			key := string(from) + "|" + string(e)
			exp, ok := want[key]
			if !ok {
				t.Fatalf("missing expectation for %s", key)
			}
			t.Run(key, func(t *testing.T) {
				got, err := Transition(from, e)
				if exp.ok {
					if err != nil {
						t.Fatalf("Transition(%q, %q) = error %v, want nil", from, e, err)
					}
				} else if err == nil {
					t.Fatalf("Transition(%q, %q) = %q, nil; want an error", from, e, got)
				}
				if got != exp.to {
					t.Fatalf("Transition(%q, %q) = %q, want %q", from, e, got, exp.to)
				}
			})
		}
	}
}

// TestTransitionUnknownEvent 未知事件在任意状态下都非法，且不改变状态。
func TestTransitionUnknownEvent(t *testing.T) {
	for _, from := range []State{StateRegistered, StateEnabled, StateDisabled, StateFailed} {
		got, err := Transition(from, Event("reload"))
		if err == nil {
			t.Fatalf("Transition(%q, %q) = %q, nil; want an error", from, "reload", got)
		}
		if got != from {
			t.Fatalf("Transition(%q, %q) = %q, want the unchanged state %q", from, "reload", got, from)
		}
	}
}
