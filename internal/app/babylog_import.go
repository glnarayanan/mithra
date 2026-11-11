package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/carelog"
	"github.com/glnarayanan/mithra/internal/people"
	"github.com/glnarayanan/mithra/internal/policy"
)

// The baby log's CSV has one row per entry. The same columns are used for
// the sample, the import and the download, so a downloaded log imports back
// without changes.
var logColumns = []string{"date", "time", "what", "amount", "detail", "note"}

// sampleLogCSV is the file parents download, fill in and import.
const sampleLogCSV = "date,time,what,amount,detail,note\n" +
	"2026-09-08,,weight,3.2,,Birth weight\n" +
	"2026-09-15,10:30,weight,3.35,,Clinic visit\n" +
	"2026-09-15,07:10,breast,15,left,\n" +
	"2026-09-15,09:40,bottle,90,formula,\n" +
	"2026-09-15,09:55,diaper,,wet,\n" +
	"2026-09-15,10:05,spit-up,,small,\n" +
	"2026-10-20,,milestone,,First smile,\n"

// LogColumnHelp explains one column of the CSV on the settings page.
type LogColumnHelp struct{ Name, Meaning string }

var logColumnHelp = []LogColumnHelp{
	{"date", "The day, as 2026-09-15. Dates your spreadsheet rewrites, like 15/09/2026, work too."},
	{"time", "When it happened, as 07:10 or 7:10 pm. Needed for feeds, diapers and spit-ups."},
	{"what", "One of weight, breast, bottle, diaper, spit-up or milestone."},
	{"amount", "Kilograms for a weight, millilitres taken for a bottle, minutes for a breast feed."},
	{"detail", "Left, right or both for a breast feed. Formula or expressed for a bottle. Wet, dirty or both for a diaper. Small, medium or large for a spit-up. The milestone itself."},
	{"note", "Anything else. Optional."},
}

type entryImport struct{ imported, existing, skipped int }

var whatAliases = map[string]string{
	"weight": "weight", "breast": "breast", "breast feed": "breast", "breastfeed": "breast",
	"bottle": "bottle", "bottle feed": "bottle", "diaper": "diaper", "nappy": "diaper",
	"spit-up": "spitup", "spitup": "spitup", "spit up": "spitup", "milestone": "milestone",
}

// parseLogDate reads the date formats spreadsheets tend to write. Slashed
// dates read day first, except in a US household.
func parseLogDate(raw string, monthFirst bool, location *time.Location) (time.Time, bool) {
	raw = strings.TrimSpace(raw)
	layouts := []string{"2006-01-02", "2006/01/02", "2 Jan 2006", "2 January 2006", "Jan 2, 2006", "January 2, 2006", "2-Jan-2006", "2-Jan-06"}
	if monthFirst {
		layouts = append(layouts, "1/2/2006", "1/2/06", "1-2-2006")
	} else {
		layouts = append(layouts, "2/1/2006", "2/1/06", "2-1-2006", "2.1.2006")
	}
	for _, layout := range layouts {
		if day, err := time.ParseInLocation(layout, raw, location); err == nil {
			return day, true
		}
	}
	// A date with a time attached, as some spreadsheets export.
	if at, err := time.Parse(time.RFC3339, raw); err == nil {
		at = at.In(location)
		return time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, location), true
	}
	return time.Time{}, false
}

func parseLogTime(raw string) (int, int, bool) {
	raw = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(raw), ".", ""))
	for _, layout := range []string{"15:04", "15:04:05", "3:04 pm", "3:04pm", "3 pm", "3pm", "3:04:05 pm"} {
		if t, err := time.Parse(layout, raw); err == nil {
			return t.Hour(), t.Minute(), true
		}
	}
	return 0, 0, false
}

type pendingEntry struct {
	clientID string
	input    carelog.Input
}

// importEntries reads a baby log CSV and adds every row. Nothing is added
// unless every row reads cleanly. Each row is keyed on its content, so
// importing the same file again adds nothing new.
func (a *App) importEntries(r *http.Request, scope policy.ActorScope, person people.Person, location *time.Location) (entryImport, error) {
	var result entryImport
	file, _, err := r.FormFile("file")
	if err != nil {
		return result, &carelog.InputError{Message: "Choose a CSV file to import."}
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil || len(raw) > 1<<20 {
		return result, &carelog.InputError{Message: "The file is larger than 1 MB. Split it into smaller files."}
	}
	reader := csv.NewReader(bytes.NewReader(raw))
	reader.FieldsPerRecord = -1
	reader.TrimLeadingSpace = true
	header, err := reader.Read()
	if err != nil {
		return result, &carelog.InputError{Message: "The file could not be read as a CSV. Save it from your spreadsheet as CSV and try again."}
	}
	columns := map[string]int{}
	for index, name := range header {
		columns[strings.ToLower(strings.TrimSpace(strings.TrimPrefix(name, "\uFEFF")))] = index
	}
	cell := func(row []string, name string) string {
		if i, ok := columns[name]; ok && i < len(row) {
			return strings.TrimSpace(row[i])
		}
		return ""
	}
	_, hasType := columns["type"]
	_, hasOccurred := columns["occurred_at"]
	if hasType && hasOccurred {
		return a.importLogExport(r, scope, person.ID, reader, columns, cell, location)
	}
	for _, need := range []string{"date", "what"} {
		if _, ok := columns[need]; !ok {
			return result, &carelog.InputError{Message: "The first row needs the column names from the sample: date, time, what, amount, detail and note."}
		}
	}
	monthFirst := a.householdLocale(r.Context()) == "en-US"
	var rows []pendingEntry
	for line := 2; ; line++ {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return result, &carelog.InputError{Message: "Line " + strconv.Itoa(line) + " could not be read. Nothing was imported."}
		}
		if strings.Join(row, "") == "" {
			continue
		}
		entry, err := logRow(cell(row, "date"), cell(row, "time"), cell(row, "what"), cell(row, "amount"), cell(row, "detail"), cell(row, "note"), monthFirst, location)
		if err != nil {
			return result, &carelog.InputError{Message: "Line " + strconv.Itoa(line) + ": " + err.Error() + " Nothing was imported."}
		}
		sum := sha256.Sum256([]byte(person.ID + "|" + strings.Join([]string{entry.input.OccurredAt.UTC().Format(time.RFC3339), entry.input.Type, entry.input.Fields["mode"], entry.input.Fields["kg"], entry.input.Fields["consumed_ml"], entry.input.Fields["duration_min"], entry.input.Fields["side"], entry.input.Fields["kind"], entry.input.Fields["size"], entry.input.Fields["title"]}, "|")))
		entry.clientID = "import:" + hex.EncodeToString(sum[:12])
		rows = append(rows, entry)
		if len(rows) > 5000 {
			return result, &carelog.InputError{Message: "The file has more than 5,000 entries. Split it into smaller files."}
		}
	}
	if len(rows) == 0 {
		return result, &carelog.InputError{Message: "The file has no entries under the column names."}
	}
	return result, a.saveImported(r, scope, person.ID, rows, &result)
}

// logRow turns one CSV row into a log entry, with a message a parent can act
// on when something is missing.
func logRow(date, clock, what, amount, detail, note string, monthFirst bool, location *time.Location) (pendingEntry, error) {
	kind, ok := whatAliases[strings.ToLower(what)]
	if !ok {
		return pendingEntry{}, errors.New("“" + what + "” is not something the log keeps. Use weight, breast, bottle, diaper, spit-up or milestone.")
	}
	day, ok := parseLogDate(date, monthFirst, location)
	if !ok {
		return pendingEntry{}, errors.New("write the date as 2026-09-15.")
	}
	at := day.Add(12 * time.Hour)
	if clock != "" {
		hour, minute, ok := parseLogTime(clock)
		if !ok {
			return pendingEntry{}, errors.New("write the time as 07:10 or 7:10 pm.")
		}
		at = time.Date(day.Year(), day.Month(), day.Day(), hour, minute, 0, 0, location)
	} else if kind != "weight" && kind != "milestone" {
		return pendingEntry{}, errors.New("add a time. Feeds, diapers and spit-ups need one.")
	}
	title := strings.TrimSpace(detail)
	detail = strings.ToLower(title)
	fields := map[string]string{"note": note}
	in := carelog.Input{OccurredAt: at, Source: "import", Fields: fields}
	switch kind {
	case "weight":
		in.Type = carelog.Weight
		fields["kg"] = amount
	case "breast":
		in.Type = carelog.Feed
		fields["mode"], fields["side"], fields["duration_min"] = "breast", detail, amount
		in.EndedAt = at
		if minutes, err := strconv.Atoi(amount); err == nil && minutes > 0 {
			in.EndedAt = at.Add(time.Duration(minutes) * time.Minute)
		}
	case "bottle":
		in.Type = carelog.Feed
		milk := detail
		if milk == "breast milk" || milk == "breastmilk" || milk == "pumped" {
			milk = "expressed"
		}
		fields["mode"], fields["milk_type"], fields["consumed_ml"] = "bottle", milk, amount
	case "diaper":
		in.Type = carelog.Diaper
		fields["kind"] = detail
	case "spitup":
		in.Type = carelog.SpitUp
		fields["size"] = detail
	case "milestone":
		in.Type = carelog.Milestone
		fields["title"] = title
		if title == "" {
			fields["title"], fields["note"] = note, ""
		}
	}
	if _, err := carelog.Validate(in); err != nil {
		return pendingEntry{}, errors.New(lowerFirst(err.Error()))
	}
	if at.After(time.Now().Add(5 * time.Minute)) {
		return pendingEntry{}, errors.New("that date is in the future.")
	}
	return pendingEntry{input: in}, nil
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func (a *App) saveImported(r *http.Request, scope policy.ActorScope, personID string, rows []pendingEntry, result *entryImport) error {
	for _, row := range rows {
		row.input.ClientID = row.clientID
		event, err := a.carelog.Create(r.Context(), scope, personID, row.input)
		if err != nil {
			return err
		}
		if event.Duplicate {
			result.existing++
		} else {
			result.imported++
		}
	}
	return nil
}

// importLogExport reads the older one-row-per-entry export with id, type,
// occurred_at and kg columns, and adds its weights. Each row keeps its id, so
// an import repeated later adds nothing new.
func (a *App) importLogExport(r *http.Request, scope policy.ActorScope, personID string, reader *csv.Reader, columns map[string]int, cell func([]string, string) string, location *time.Location) (entryImport, error) {
	var result entryImport
	for _, need := range []string{"id", "kg"} {
		if _, ok := columns[need]; !ok {
			return result, &carelog.InputError{Message: "The first row needs the column names from the sample: date, time, what, amount, detail and note."}
		}
	}
	var rows []pendingEntry
	for line := 2; ; line++ {
		row, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return result, &carelog.InputError{Message: "Line " + strconv.Itoa(line) + " could not be read. Nothing was imported."}
		}
		if cell(row, "type") != carelog.Weight {
			result.skipped++
			continue
		}
		raw := cell(row, "occurred_at")
		at, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			at, err = time.ParseInLocation("2006-01-02T15:04", raw, location)
		}
		id := cell(row, "id")
		if err != nil || id == "" {
			return result, &carelog.InputError{Message: "Line " + strconv.Itoa(line) + " needs an id and a time. Nothing was imported."}
		}
		in := carelog.Input{Type: carelog.Weight, OccurredAt: at, Source: "import", Fields: map[string]string{"kg": cell(row, "kg")}}
		if _, err := carelog.Validate(in); err != nil {
			return result, &carelog.InputError{Message: "Line " + strconv.Itoa(line) + ": " + err.Error() + " Nothing was imported."}
		}
		rows = append(rows, pendingEntry{clientID: "import:" + id, input: in})
		if len(rows) > 5000 {
			return result, &carelog.InputError{Message: "The file has more than 5,000 entries. Split it into smaller files."}
		}
	}
	return result, a.saveImported(r, scope, personID, rows, &result)
}

// logCSV writes entries, oldest first, in the import columns.
func logCSV(events []carelog.Event, location *time.Location) []byte {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	writer.Write(logColumns)
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		at := e.OccurredAt.In(location)
		d := e.Details
		clock := at.Format("15:04")
		var what, amount, detail string
		switch e.Type {
		case carelog.Weight:
			what, amount = "weight", d["kg"]
		case carelog.Feed:
			if d["mode"] == "breast" {
				what, amount, detail = "breast", d["duration_min"], d["side"]
			} else {
				what, amount, detail = "bottle", firstNonEmpty(d["consumed_ml"], d["offered_ml"]), d["milk_type"]
			}
		case carelog.Diaper:
			what, detail = "diaper", d["kind"]
		case carelog.SpitUp:
			what, detail = "spit-up", d["size"]
		case carelog.Milestone:
			what, detail = "milestone", d["title"]
		default:
			continue
		}
		writer.Write([]string{at.Format("2006-01-02"), clock, what, amount, detail, d["note"]})
	}
	writer.Flush()
	return buffer.Bytes()
}

func importStatus(imported, kept, skipped int) string {
	line := "Imported " + plural(imported, "entry", "entries") + "."
	if imported == 0 && kept > 0 && skipped == 0 {
		return "Nothing new to add. All " + plural(kept, "entry", "entries") + " in the file " + map[bool]string{true: "was", false: "were"}[kept == 1] + " already in the log."
	}
	if kept > 0 {
		if kept == 1 {
			line += " 1 was already in the log."
		} else {
			line += " " + strconv.Itoa(kept) + " were already in the log."
		}
	}
	if skipped > 0 {
		line += " Skipped " + plural(skipped, "row that is not a weight", "rows that are not weights") + "."
	}
	return line
}
