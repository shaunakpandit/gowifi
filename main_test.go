package main

import (
	"regexp"
	"slices"
	"testing"
)

func TestParseTerse(t *testing.T) {
	got := parseTerse(`My\:Net:80:WPA2`)
	if !slices.Equal(got, []string{"My:Net", "80", "WPA2"}) {
		t.Fatalf("got %q", got)
	}
}

func TestParseNetworks(t *testing.T) {
	got := parseNetworks("Home:40:WPA2\nCafe:90:\nHome:70:WPA2\n:99:WPA2\nOpen:10:--")
	want := []network{{"Cafe", 90, false}, {"Home", 70, true}, {"Open", 10, false}}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v", got)
	}
}

func TestMoveSkipsHeaders(t *testing.T) {
	rows := []row{{header: true}, {}, {header: true}, {}}
	for _, test := range []struct{ cursor, delta, want int }{{-1, 1, 1}, {1, 1, 3}, {3, -1, 1}, {1, -1, 1}, {3, 1, 3}} {
		if got := move(rows, test.cursor, test.delta); got != test.want {
			t.Errorf("move(%d,%d) = %d, want %d", test.cursor, test.delta, got, test.want)
		}
	}
}

func TestBars(t *testing.T) {
	for signal, want := range map[int]string{0: "▂___", 30: "▂▄__", 75: "▂▄▆_", 100: "▂▄▆█"} {
		if got := regexp.MustCompile(`\x1b\[\d+m`).ReplaceAllString(bars(signal), ""); got != want {
			t.Errorf("bars(%d) = %q, want %q", signal, got, want)
		}
	}
}
