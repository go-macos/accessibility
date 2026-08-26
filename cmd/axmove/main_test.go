package main

import (
	"strings"
	"testing"

	"github.com/go-macos/accessibility"
)

// The flag parsing is the only logic in this tool that can be wrong in a way a
// user would not immediately see, so it is what is tested. Everything else here
// is a call into the library, which has its own suite.

func TestParsePlacement(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want accessibility.Placement
		ok   bool
	}{
		{"relative", accessibility.Relative, true},
		{"RELATIVE", accessibility.Relative, true},
		{"origin", accessibility.Origin, true},
		{"center", accessibility.Center, true},
		{"centre", accessibility.Center, true},
		{"fill", accessibility.Fill, true},
		{"sideways", 0, false},
		{"", 0, false},
	} {
		got, err := parsePlacement(tc.in)
		if (err == nil) != tc.ok {
			t.Errorf("parsePlacement(%q) err = %v, want ok=%v", tc.in, err, tc.ok)
			continue
		}
		if tc.ok && got != tc.want {
			t.Errorf("parsePlacement(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestParseRect(t *testing.T) {
	got, err := parseRect(" 100 , -200 , 800 , 600 ")
	if err != nil {
		t.Fatalf("parseRect: %v", err)
	}
	if want := (accessibility.Rect{X: 100, Y: -200, W: 800, H: 600}); got != want {
		t.Errorf("parseRect = %v, want %v", got, want)
	}
	for _, bad := range []string{"", "1,2,3", "1,2,3,4,5", "a,2,3,4", "1,2,0,4", "1,2,3,-4"} {
		if _, err := parseRect(bad); err == nil {
			t.Errorf("parseRect(%q) was accepted", bad)
		} else if !strings.Contains(err.Error(), "-rect") {
			t.Errorf("parseRect(%q) = %v, want it to name the flag", bad, err)
		}
	}
}
