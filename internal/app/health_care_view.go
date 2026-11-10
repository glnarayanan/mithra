package app

import (
	"sort"
	"time"

	"github.com/glnarayanan/mithra/internal/health"
	"github.com/glnarayanan/mithra/internal/policy"
)

type CareView struct {
	Medicines   []MedicineView
	PastCount   int
	Notes       []NoteView
	Habits      []HabitView
	ShowSubject bool
}

type MedicineView struct {
	ID, Subject, Name, Dosage, Since, Ended, Note string
	Version                                       int64
	Current, Private                              bool
}

type NoteView struct {
	Subject, DateISO, Date, Content string
	Private                         bool
}

type HabitView struct {
	ID, Subject, Name, Kind, Unit, Today string
	Private                              bool
	Days                                 []HabitDayView
	DoneCount                            int
}

type HabitDayView struct {
	Label, Value string
	Done         bool
}

type HealthGroupView struct {
	Category string
	Series   []HealthSeriesView
}

// careView turns typed care entries into page rows. Habit days run from six
// days ago through today.
func careView(log health.CareLog, now time.Time, showSubject bool) CareView {
	view := CareView{ShowSubject: showSubject}
	for _, m := range log.Medicines {
		item := MedicineView{ID: m.ID, Subject: m.Subject, Name: m.Name, Dosage: m.Dosage, Note: m.Note, Version: m.Version, Current: m.EndedOn == "", Private: m.Visibility == policy.Personal}
		if date, ok := parseISODate(m.StartedOn); ok {
			item.Since = "Since " + shortDate(date)
		}
		if date, ok := parseISODate(m.EndedOn); ok {
			item.Ended = "Stopped " + shortDate(date)
		}
		if !item.Current {
			view.PastCount++
		}
		view.Medicines = append(view.Medicines, item)
	}
	for _, n := range log.Notes {
		item := NoteView{Subject: n.Subject, DateISO: n.RecordedOn, Content: n.Content, Private: n.Visibility == policy.Personal}
		if date, ok := parseISODate(n.RecordedOn); ok {
			item.Date = humanDate(date)
		}
		view.Notes = append(view.Notes, item)
	}
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	for _, h := range log.Habits {
		item := HabitView{ID: h.ID, Subject: h.Subject, Name: h.Name, Kind: h.Kind, Unit: h.Unit, Today: h.Logs[today.Format("2006-01-02")], Private: h.Visibility == policy.Personal}
		for offset := 6; offset >= 0; offset-- {
			day := today.AddDate(0, 0, -offset)
			value := h.Logs[day.Format("2006-01-02")]
			label := day.Format("Mon")
			if offset == 0 {
				label = "Today"
			}
			item.Days = append(item.Days, HabitDayView{Label: label, Value: value, Done: value != ""})
			if value != "" {
				item.DoneCount++
			}
		}
		view.Habits = append(view.Habits, item)
	}
	return view
}

// groupSeries sorts measurement cards under catalog categories.
func groupSeries(series []HealthSeriesView) []HealthGroupView {
	index := map[string]int{}
	var groups []HealthGroupView
	for _, item := range series {
		category := health.CategoryFor(item.Analyte)
		position, ok := index[category]
		if !ok {
			position = len(groups)
			index[category] = position
			groups = append(groups, HealthGroupView{Category: category})
		}
		groups[position].Series = append(groups[position].Series, item)
	}
	sort.SliceStable(groups, func(i, j int) bool {
		return health.CategoryRank(groups[i].Category) < health.CategoryRank(groups[j].Category)
	})
	return groups
}
