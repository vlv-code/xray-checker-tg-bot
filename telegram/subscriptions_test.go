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

func TestIsAllowedSubURL(t *testing.T) {
	valid := []string{
		"http://example.com/sub",
		"https://example.com/sub?key=1",
		"  https://sub.domain.org/path  ",
	}
	for _, u := range valid {
		if !isAllowedSubURL(u) {
			t.Errorf("expected %q to be allowed, but rejected", u)
		}
	}

	invalid := []string{
		"file:///etc/passwd",
		"file://c:/windows/win.ini",
		"folder:///etc",
		"base64://dGVzdA==",
		"ftp://example.com/sub",
		"",
		"not-a-url",
	}
	for _, u := range invalid {
		if isAllowedSubURL(u) {
			t.Errorf("expected %q to be rejected, but allowed", u)
		}
	}
}
