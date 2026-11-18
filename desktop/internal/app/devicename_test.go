package app

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestDeviceName(t *testing.T) {
	for in, want := range map[string]string{
		"  werk-laptop ":   "werk-laptop",
		"evil\x1b[31mpc":   "evil[31mpc",
		"":                 "Computer",
		"\t\n":             "Computer",
		"Albertos-MacBook": "Albertos-MacBook",
	} {
		if got := deviceName(in); got != want {
			t.Errorf("deviceName(%q) = %q, want %q", in, got, want)
		}
	}
	long := deviceName(strings.Repeat("é", 100))
	if utf8.RuneCountInString(long) != 64 {
		t.Errorf("long name has %d characters", utf8.RuneCountInString(long))
	}
}
