package app

import (
	"testing"
	"time"
)

func TestGroupDigits(t *testing.T) {
	for input, want := range map[string]string{
		"735000":   "735,000",
		"3200000":  "3,200,000",
		"125.50":   "125.50",
		"-1250.5":  "−1,250.5",
		"999":      "999",
		"twelve":   "twelve",
		"12,000":   "12,000",
		"":         "",
		"+1000000": "+1,000,000",
	} {
		if got := groupDigits(input); got != want {
			t.Errorf("groupDigits(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestRelativeDays(t *testing.T) {
	now := time.Date(2026, 11, 10, 22, 30, 0, 0, time.UTC)
	for offset, want := range map[int]string{0: "today", 1: "tomorrow", -1: "yesterday", 9: "in 9 days", -3: "3 days ago"} {
		if got := relativeDays(now.AddDate(0, 0, offset), now); got != want {
			t.Errorf("relativeDays(%d) = %q, want %q", offset, got, want)
		}
	}
}

func TestParseISODate(t *testing.T) {
	if _, ok := parseISODate("19 Nov"); ok {
		t.Fatal("a label is not an ISO date")
	}
	if date, ok := parseISODate("2026-11-19"); !ok || date.Day() != 19 {
		t.Fatalf("parseISODate = %v, %v", date, ok)
	}
}
