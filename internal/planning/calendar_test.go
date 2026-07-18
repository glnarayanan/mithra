package planning

import (
	"strings"
	"testing"
)

func TestICSAllDayTimedEscapingAndFolding(t *testing.T) {
	allDay, err := ICS(Event{ID: "a", Title: "Leap, day; review", Description: "one\\two\nthree", AllDay: true, StartsOn: "2028-02-29", EndsOn: "2028-03-01"}, "Asia/Kolkata")
	if err != nil || !strings.Contains(allDay, "DTSTAMP:19700101T000000Z") || !strings.Contains(allDay, "DTSTART;VALUE=DATE:20280229\r\nDTEND;VALUE=DATE:20280302") || !strings.Contains(allDay, "SUMMARY:Leap\\, day\\; review") || !strings.Contains(allDay, "DESCRIPTION:one\\\\two\\nthree") {
		t.Fatalf("all-day=%q err=%v", allDay, err)
	}
	timed, err := ICS(Event{ID: "b", Title: strings.Repeat("é", 60), AllDay: false, StartsAt: "2026-03-08T01:30", EndsAt: "2026-03-08T03:30", Timezone: "America/New_York"}, "Asia/Kolkata")
	if err != nil || !strings.Contains(timed, "DTSTART;TZID=America/New_York:20260308T013000") || !strings.Contains(timed, "\r\n ") {
		t.Fatalf("timed=%q err=%v", timed, err)
	}
	for _, line := range strings.Split(strings.TrimSuffix(timed, "\r\n"), "\r\n") {
		if len([]byte(line)) > 75 {
			t.Fatalf("unfolded line exceeds 75 octets: %d", len([]byte(line)))
		}
	}
}

func TestGoogleCalendarURLRequiresCompleteDate(t *testing.T) {
	if _, err := GoogleCalendarURL(Event{ID: "x", Title: "Missing", AllDay: true}, "Asia/Kolkata"); err != ErrIncompleteDate {
		t.Fatalf("missing date: %v", err)
	}
	u, err := GoogleCalendarURL(Event{ID: "x", Title: "Leap day", AllDay: true, StartsOn: "2028-02-29"}, "Asia/Kolkata")
	if err != nil || !strings.Contains(u, "action=TEMPLATE") || !strings.Contains(u, "dates=20280229%2F20280301") {
		t.Fatalf("url=%q err=%v", u, err)
	}
	if _, err := GoogleCalendarURL(Event{ID: "x", Title: "Backwards", AllDay: true, StartsOn: "2028-03-02", EndsOn: "2028-03-01"}, "Asia/Kolkata"); err != ErrIncompleteDate {
		t.Fatalf("backwards date: %v", err)
	}
}
