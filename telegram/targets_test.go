package telegram

import (
	"reflect"
	"testing"
)

func TestParseChatTargets(t *testing.T) {
	cases := []struct {
		name    string
		entries []string
		want    []ChatTarget
		wantErr bool
	}{
		{"private only", []string{"123456789"}, []ChatTarget{{ChatID: 123456789}}, false},
		{"group without topic", []string{"-100987654321"}, []ChatTarget{{ChatID: -100987654321}}, false},
		{"forum topic", []string{"-100987654321:42"}, []ChatTarget{{ChatID: -100987654321, ThreadID: 42}}, false},
		{"mixed", []string{"123456789", "-100987654321", "-100987654321:42"},
			[]ChatTarget{{ChatID: 123456789}, {ChatID: -100987654321}, {ChatID: -100987654321, ThreadID: 42}}, false},
		{"spaces trimmed", []string{" 42 ", " -7:9 "}, []ChatTarget{{ChatID: 42}, {ChatID: -7, ThreadID: 9}}, false},
		{"exact duplicates removed", []string{"5:1", "5:1", "5"}, []ChatTarget{{ChatID: 5, ThreadID: 1}, {ChatID: 5}}, false},
		{"same chat whole and topic kept", []string{"5", "5:1"}, []ChatTarget{{ChatID: 5}, {ChatID: 5, ThreadID: 1}}, false},
		{"empty entries skipped", []string{"", "  "}, []ChatTarget{}, false},
		{"empty topic", []string{"-100:"}, nil, true},
		{"zero topic", []string{"-100:0"}, nil, true},
		{"negative topic", []string{"-100:-3"}, nil, true},
		{"non numeric topic", []string{"-100:abc"}, nil, true},
		{"non numeric chat", []string{"abc"}, nil, true},
		{"zero chat", []string{"0"}, nil, true},
		{"chat with colon only", []string{":"}, nil, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ParseChatTargets(c.entries)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !reflect.DeepEqual(got, c.want) {
				t.Fatalf("expected %v, got %v", c.want, got)
			}
		})
	}
}

func TestChatTargetTargetKey(t *testing.T) {
	if got := (ChatTarget{ChatID: -100123}).targetKey(); got != "-100123" {
		t.Errorf("whole-chat key: got %q", got)
	}
	if got := (ChatTarget{ChatID: -100123, ThreadID: 42}).targetKey(); got != "-100123:42" {
		t.Errorf("topic key: got %q", got)
	}
}
