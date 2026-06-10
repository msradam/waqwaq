package config

import "testing"

func TestResolveAccent(t *testing.T) {
	cases := []struct {
		in, color, textOn string
	}{
		{"marigold", "#FBBC0E", ""},
		{" Madder ", "#8E3B30", "#FAF8EA"},
		{"turmeric-light", "#E0B452", ""},
		{"#7b2ff7", "#7b2ff7", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		color, textOn := ResolveAccent(c.in)
		if color != c.color || textOn != c.textOn {
			t.Errorf("ResolveAccent(%q) = %q, %q; want %q, %q", c.in, color, textOn, c.color, c.textOn)
		}
	}
}
