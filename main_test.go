package main

import (
	"flag"
	"testing"
)

// parseArgs must accept flags before OR after positional args, since users write
// `serve <dir> --addr x` as readily as `serve --addr x <dir>`. Go's flag package
// stops at the first positional, which this works around.
func TestParseArgsInterleaved(t *testing.T) {
	cases := []struct {
		name     string
		args     []string
		wantAddr string
		wantPos  []string
	}{
		{"flag after positional", []string{"dir", "--addr", "1.2.3.4:9"}, "1.2.3.4:9", []string{"dir"}},
		{"flag before positional", []string{"--addr", "1.2.3.4:9", "dir"}, "1.2.3.4:9", []string{"dir"}},
		{"positional only", []string{"dir"}, "def", []string{"dir"}},
		{"no args", nil, "def", nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := flag.NewFlagSet("serve", flag.ContinueOnError)
			addr := fs.String("addr", "def", "")
			pos := parseArgs(fs, tc.args)
			if *addr != tc.wantAddr {
				t.Errorf("addr = %q, want %q", *addr, tc.wantAddr)
			}
			if len(pos) != len(tc.wantPos) {
				t.Fatalf("positionals = %v, want %v", pos, tc.wantPos)
			}
			for i := range pos {
				if pos[i] != tc.wantPos[i] {
					t.Errorf("positional[%d] = %q, want %q", i, pos[i], tc.wantPos[i])
				}
			}
		})
	}
}
