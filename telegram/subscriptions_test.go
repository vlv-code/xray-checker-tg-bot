package telegram

import "testing"

func TestCommandArg(t *testing.T) {
	cases := []struct {
		text string
		want string
	}{
		{"/addsub https://example.com/sub", "https://example.com/sub"},
		{"/addsub", ""},
		{"/addsub   ", ""},
		{"/addsub@MyBot https://example.com/sub", "https://example.com/sub"},
		{"/addsub https://example.com/sub extra-ignored-arg", "https://example.com/sub"},
	}

	for _, c := range cases {
		if got := commandArg(c.text); got != c.want {
			t.Errorf("commandArg(%q) = %q, want %q", c.text, got, c.want)
		}
	}
}
