package subscription

import (
	"testing"
)

func TestRedactURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"https://panel.example.com/sub?token=SECRET12345", "https://panel.example.com/sub?<redacted>"},
		{"https://panel.example.com/sub", "https://panel.example.com/sub"},
		{"not-a-valid-url?token=XYZ", "not-a-valid-url?<redacted>"},
		{"https://panel.example.com/sub?token=SECRET#MyProfile", "https://panel.example.com/sub?<redacted>"},
		{"https://panel.example.com/sub#MyProfile", "https://panel.example.com/sub"},
	}

	for _, c := range cases {
		got := RedactURL(c.in)
		if got != c.want {
			t.Errorf("RedactURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRedactedList(t *testing.T) {
	list := []string{
		"https://a.com/?token=1",
		"https://b.com/clean",
	}
	redacted := RedactedList(list)
	if len(redacted) != 2 || redacted[0] != "https://a.com/?<redacted>" || redacted[1] != "https://b.com/clean" {
		t.Fatalf("unexpected redacted list: %v", redacted)
	}
}
