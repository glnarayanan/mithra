package app

import (
	"html/template"
	"strconv"
	"strings"
	"time"
)

// Display formatters shared by every page. They only change how stored facts
// read; they never round, convert or add meaning to a value.

func parseISODate(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if len(value) < 10 {
		return time.Time{}, false
	}
	date, err := time.Parse("2006-01-02", value[:10])
	if err != nil {
		return time.Time{}, false
	}
	return date, true
}

// humanDate reads "Tue 17 Nov", adding the year only when it is not this year.
func humanDate(date time.Time) string {
	if date.Year() != time.Now().Year() {
		return date.Format("Mon 2 Jan 2006")
	}
	return date.Format("Mon 2 Jan")
}

// relativeDays describes a calendar date against today: "today", "tomorrow",
// "in 9 days", "yesterday" or "3 days ago".
func relativeDays(date, now time.Time) string {
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	day := time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, time.UTC)
	days := int(day.Sub(today).Hours() / 24)
	switch {
	case days == 0:
		return "today"
	case days == 1:
		return "tomorrow"
	case days == -1:
		return "yesterday"
	case days > 1:
		return "in " + strconv.Itoa(days) + " days"
	default:
		return strconv.Itoa(-days) + " days ago"
	}
}

// groupDigits adds thousands separators to the integer part of a plain
// decimal string such as "735000" or "-1250.50". Text that is not a plain
// number is returned unchanged.
func groupDigits(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return value
	}
	sign := ""
	if value[0] == '-' || value[0] == '+' {
		sign, value = value[:1], value[1:]
	}
	whole, fraction, hasFraction := strings.Cut(value, ".")
	if whole == "" || strings.Trim(whole, "0123456789") != "" || (hasFraction && strings.Trim(fraction, "0123456789") != "") {
		return sign + value
	}
	var grouped strings.Builder
	for i, digit := range whole {
		if i > 0 && (len(whole)-i)%3 == 0 {
			grouped.WriteByte(',')
		}
		grouped.WriteRune(digit)
	}
	if sign == "-" {
		sign = "−"
	}
	if hasFraction {
		return sign + grouped.String() + "." + fraction
	}
	return sign + grouped.String()
}

// FinanceRecordRow pairs a record with the CSRF token its forms need.
type FinanceRecordRow struct {
	Record FinanceRecordView
	CSRF   string
}

var templateFuncs = template.FuncMap{
	"recordRow": func(record FinanceRecordView, csrf string) FinanceRecordRow {
		return FinanceRecordRow{Record: record, CSRF: csrf}
	},
}
