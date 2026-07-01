package llm

import (
	"reflect"
	"testing"
)

func TestCacheBreakpoints(t *testing.T) {
	sys := Message{Role: RoleSystem}
	usr := Message{Role: RoleUser}
	asst := Message{Role: RoleAssistant}

	cases := []struct {
		name string
		msgs []Message
		want []int
	}{
		{"empty", nil, nil},
		{"system only", []Message{sys}, []int{0}},
		{"system prefix + short turn: no rolling yet", []Message{sys, sys, usr}, []int{1}},
		{"long conversation: system + rolling at len-2", []Message{sys, sys, usr, asst, usr}, []int{1, 3}},
		{"no system: rolling only", []Message{usr, asst, usr, asst}, []int{2}},
	}
	for _, c := range cases {
		if got := CacheBreakpoints(c.msgs); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: CacheBreakpoints=%v want %v", c.name, got, c.want)
		}
	}
}
