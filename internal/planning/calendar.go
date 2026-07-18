package planning

import (
	"errors"
	"net/url"
	"strings"
	"time"
)

var ErrIncompleteDate = errors.New("event needs a complete date before calendar export")

// ICS returns one deterministic RFC5545 calendar event without sync state.
func ICS(e Event, householdTimezone string) (string, error) {
	if e.ID == "" || strings.TrimSpace(e.Title) == "" {
		return "", ErrInvalidRecord
	}
	stamp := "19700101T000000Z"
	if created, err := time.Parse(time.RFC3339Nano, e.CreatedAt); err == nil {
		stamp = created.UTC().Format("20060102T150405Z")
	}
	lines := []string{"BEGIN:VCALENDAR", "VERSION:2.0", "PRODID:-//Mithra//Planning//EN", "CALSCALE:GREGORIAN", "BEGIN:VEVENT", "UID:" + escape(e.ID) + "@mithra", "DTSTAMP:" + stamp, "SUMMARY:" + escape(e.Title)}
	if e.Description != "" {
		lines = append(lines, "DESCRIPTION:"+escape(e.Description))
	}
	if e.Location != "" {
		lines = append(lines, "LOCATION:"+escape(e.Location))
	}
	if e.AllDay {
		if !date(e.StartsOn) {
			return "", ErrIncompleteDate
		}
		end := e.EndsOn
		if end == "" {
			end = e.StartsOn
		}
		if !date(end) || end < e.StartsOn {
			return "", ErrIncompleteDate
		}
		z, _ := time.Parse("2006-01-02", end)
		lines = append(lines, "DTSTART;VALUE=DATE:"+strings.ReplaceAll(e.StartsOn, "-", "")+"", "DTEND;VALUE=DATE:"+z.AddDate(0, 0, 1).Format("20060102"))
	} else {
		zone := householdTimezone
		if e.Timezone != "" {
			zone = e.Timezone
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return "", ErrInvalidRecord
		}
		start, err := time.ParseInLocation("2006-01-02T15:04", e.StartsAt, loc)
		if err != nil {
			return "", ErrIncompleteDate
		}
		end, err := time.ParseInLocation("2006-01-02T15:04", e.EndsAt, loc)
		if err != nil || !end.After(start) {
			return "", ErrIncompleteDate
		}
		lines = append(lines, "DTSTART;TZID="+escape(zone)+":"+start.Format("20060102T150405"), "DTEND;TZID="+escape(zone)+":"+end.Format("20060102T150405"))
	}
	lines = append(lines, "END:VEVENT", "END:VCALENDAR")
	for i := range lines {
		lines[i] = fold(lines[i])
	}
	return strings.Join(lines, "\r\n") + "\r\n", nil
}

// GoogleCalendarURL opens a draft for review; it never creates a calendar sync.
func GoogleCalendarURL(e Event, householdTimezone string) (string, error) {
	if e.AllDay && !date(e.StartsOn) {
		return "", ErrIncompleteDate
	}
	if !e.AllDay && (e.StartsAt == "" || e.EndsAt == "") {
		return "", ErrIncompleteDate
	}
	q := url.Values{"action": {"TEMPLATE"}, "text": {e.Title}, "details": {e.Description}, "location": {e.Location}}
	if e.AllDay {
		end := e.EndsOn
		if end == "" {
			end = e.StartsOn
		}
		if !date(end) || end < e.StartsOn {
			return "", ErrIncompleteDate
		}
		z, _ := time.Parse("2006-01-02", end)
		q.Set("dates", strings.ReplaceAll(e.StartsOn, "-", "")+"/"+z.AddDate(0, 0, 1).Format("20060102"))
	} else {
		zone := e.Timezone
		if zone == "" {
			zone = householdTimezone
		}
		loc, err := time.LoadLocation(zone)
		if err != nil {
			return "", ErrInvalidRecord
		}
		a, err := time.ParseInLocation("2006-01-02T15:04", e.StartsAt, loc)
		if err != nil {
			return "", ErrIncompleteDate
		}
		b, err := time.ParseInLocation("2006-01-02T15:04", e.EndsAt, loc)
		if err != nil || !b.After(a) {
			return "", ErrIncompleteDate
		}
		q.Set("dates", a.UTC().Format("20060102T150405Z")+"/"+b.UTC().Format("20060102T150405Z"))
		q.Set("ctz", zone)
	}
	return "https://calendar.google.com/calendar/render?" + q.Encode(), nil
}
func escape(v string) string {
	v = strings.ReplaceAll(v, "\\", "\\\\")
	v = strings.ReplaceAll(v, ";", "\\;")
	v = strings.ReplaceAll(v, ",", "\\,")
	return strings.ReplaceAll(strings.ReplaceAll(v, "\r\n", "\\n"), "\n", "\\n")
}
func fold(v string) string {
	var out strings.Builder
	limit := 75
	for len(v) > limit {
		cut := limit
		for cut > 0 && v[cut]&0xc0 == 0x80 {
			cut--
		}
		out.WriteString(v[:cut])
		out.WriteString("\r\n ")
		v = v[cut:]
		limit = 74
	}
	out.WriteString(v)
	return out.String()
}
