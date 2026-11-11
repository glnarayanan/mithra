package app

import (
	"bytes"
	"context"
	"encoding/csv"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/carelog"
	"github.com/glnarayanan/mithra/internal/people"
)

func uploadLog(t *testing.T, application *App, session browserSession, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var buffer bytes.Buffer
	writer := multipart.NewWriter(&buffer)
	writer.WriteField("csrf", session.csrf.Value)
	writer.WriteField("action", "import")
	part, _ := writer.CreateFormFile("file", "log.csv")
	part.Write([]byte(body))
	writer.Close()
	request := httptest.NewRequest(http.MethodPost, path, &buffer)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	request.Header.Set("Origin", testOrigin)
	request.Header.Set("Sec-Fetch-Site", "same-origin")
	request.AddCookie(session.session)
	request.AddCookie(session.csrf)
	return serve(application, request)
}

func TestChildPagesImportExportAndFocus(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	scope := ownerScope(t, application, session)
	ctx := context.Background()
	baby, err := application.people.Create(ctx, scope, people.Draft{Name: "Meera", Relationship: "child", BirthDate: time.Now().AddDate(0, -2, 0).Format("2006-01-02")})
	if err != nil {
		t.Fatal(err)
	}
	base := "/family/" + baby.ID
	get := func(target string) *httptest.ResponseRecorder {
		target, _, _ = strings.Cut(target, "#")
		return serve(application, authenticatedHealthRequest(session, http.MethodGet, target, nil))
	}

	// The sample downloads as a CSV, and imports as it is.
	sample := get(base + "/sample.csv")
	if sample.Code != http.StatusOK || !strings.HasPrefix(sample.Header().Get("Content-Type"), "text/csv") || !strings.Contains(sample.Header().Get("Content-Disposition"), "baby-log-sample.csv") {
		t.Fatalf("sample status=%d headers=%v", sample.Code, sample.Header())
	}
	imported := uploadLog(t, application, session, base+"/log", sample.Body.String())
	if imported.Code != http.StatusSeeOther || !strings.Contains(get(imported.Header().Get("Location")).Body.String(), "Imported 7 entries.") {
		t.Fatalf("sample import status=%d body=%q", imported.Code, imported.Body.String())
	}
	// The result offers to undo the whole import, and undo removes only it.
	location, _ := url.Parse(imported.Header().Get("Location"))
	batch := location.Query().Get("batch")
	if batch == "" || !strings.Contains(get(imported.Header().Get("Location")).Body.String(), "Undo import") {
		t.Fatal("no undo after an import")
	}
	undone := serve(application, authenticatedHealthRequest(session, http.MethodPost, base+"/log", url.Values{"action": {"undo_import"}, "batch": {batch}}))
	if body := get(undone.Header().Get("Location")).Body.String(); !strings.Contains(body, "Import undone. Removed 7 entries from the log.") {
		t.Fatalf("undo status=%d", undone.Code)
	}
	if left, _ := application.carelog.List(ctx, scope, baby.ID, time.Time{}); len(left) != 0 {
		t.Fatalf("undo left %d entries", len(left))
	}
	// Importing the file again after an undo brings the entries back.
	imported = uploadLog(t, application, session, base+"/log", sample.Body.String())
	if !strings.Contains(get(imported.Header().Get("Location")).Body.String(), "Imported 7 entries.") {
		t.Fatal("re-import after undo did not bring the entries back")
	}
	events, _ := application.carelog.List(ctx, scope, baby.ID, time.Time{})
	kinds := map[string]int{}
	for _, e := range events {
		kinds[e.Type]++
		if e.Running() {
			t.Fatalf("an imported breast feed is running: %#v", e)
		}
	}
	if kinds[carelog.Weight] != 2 || kinds[carelog.Feed] != 2 || kinds[carelog.Diaper] != 1 || kinds[carelog.SpitUp] != 1 || kinds[carelog.Milestone] != 1 {
		t.Fatalf("kinds=%v", kinds)
	}

	// The download has the same columns and imports back without adding anything.
	export := get(base + "/export.csv")
	rows, err := csv.NewReader(strings.NewReader(export.Body.String())).ReadAll()
	if err != nil || len(rows) != 8 || strings.Join(rows[0], ",") != "date,time,what,amount,detail,note" {
		t.Fatalf("export rows=%v err=%v", rows, err)
	}
	again := uploadLog(t, application, session, base+"/log", export.Body.String())
	if body := get(again.Header().Get("Location")).Body.String(); !strings.Contains(body, "Nothing new to add. All 7 entries in the file were already in the log.") {
		t.Fatal("re-importing the download added entries")
	}

	// Spreadsheet dates and 12-hour times read; a bad row names its line and adds nothing.
	ok := uploadLog(t, application, session, base+"/log", "Date,Time,What,Amount,Detail,Note\n15/10/2026,7:05 pm,Bottle,60,breast milk,\n")
	if ok.Code != http.StatusSeeOther {
		t.Fatalf("spreadsheet row status=%d %q", ok.Code, ok.Body.String())
	}
	bad := uploadLog(t, application, session, base+"/log", "date,time,what,amount,detail,note\n2026-10-01,,weight,4.1,,\n2026-10-01,,diaper,,wet,\n")
	if bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "Line 3: add a time.") {
		t.Fatalf("bad row status=%d", bad.Code)
	}
	if weights, _ := application.carelog.Weights(ctx, scope, baby.ID); len(weights) != 2 {
		t.Fatalf("a refused file added %d weights", len(weights)-2)
	}
	if wrong := uploadLog(t, application, session, base+"/log", "name,value\nx,1\n"); !strings.Contains(wrong.Body.String(), "column names from the sample") {
		t.Fatal("a file without the columns was not explained")
	}

	// Patterns: interactive chart data for each range, with the weight figures.
	patterns := get(base + "/patterns?days=90").Body.String()
	for _, want := range []string{"Patterns", "90 days", `data-chart="weight"`, `data-chart="rhythm"`, "/assets/babycharts.js", "Feeds a day", "since "} {
		if !strings.Contains(patterns, want) {
			t.Fatalf("patterns missing %q", want)
		}
	}
	if strings.Contains(get(base+"/patterns?days=5").Body.String(), `aria-current="page">5 days`) {
		t.Fatal("an unknown range was accepted")
	}

	// Child settings: choosing weight puts it in front on the page and on Home.
	if body := get(base + "/settings").Body.String(); !strings.Contains(body, "What to keep in front") || !strings.Contains(body, base+"/sample.csv") {
		t.Fatal("child settings page is incomplete")
	}
	saved := serve(application, authenticatedHealthRequest(session, http.MethodPost, base+"/settings", url.Values{"focus": {"weight"}}))
	if saved.Code != http.StatusSeeOther || !strings.Contains(get(saved.Header().Get("Location")).Body.String(), "now leads with weight") {
		t.Fatalf("focus save status=%d", saved.Code)
	}
	if bad := serve(application, authenticatedHealthRequest(session, http.MethodPost, base+"/settings", url.Values{"focus": {"sleep"}})); bad.Code != http.StatusBadRequest {
		t.Fatalf("unknown focus status=%d", bad.Code)
	}
	if body := get(base).Body.String(); !strings.Contains(body, "Latest weight") || !strings.Contains(body, "baby-hero-weight") {
		t.Fatal("child page does not lead with weight")
	}
	if body := get("/").Body.String(); !strings.Contains(body, "Latest weight") || !strings.Contains(body, "Log a weight") {
		t.Fatal("Home panel does not lead with weight")
	}
	if body := get("/family").Body.String(); !strings.Contains(body, "Latest weight: 3.35 kg") {
		t.Fatal("Family list does not state the weight")
	}

	// An adult has none of these pages.
	adult, _ := application.people.Create(ctx, scope, people.Draft{Name: "Amma", Relationship: "parent"})
	if response := get("/family/" + adult.ID + "/patterns"); response.Code != http.StatusNotFound {
		t.Fatalf("adult patterns status=%d", response.Code)
	}
}

// A log with no entries in the chosen days, or only past weights, must still
// render Patterns: the weight panel leads when nothing else can.
func TestPatternsWithOnlyPastWeights(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	scope := ownerScope(t, application, session)
	ctx := context.Background()
	get := func(target string) *httptest.ResponseRecorder {
		return serve(application, authenticatedHealthRequest(session, http.MethodGet, target, nil))
	}
	blank, err := application.people.Create(ctx, scope, people.Draft{Name: "Asha", Relationship: "child"})
	if err != nil {
		t.Fatal(err)
	}
	baby, err := application.people.Create(ctx, scope, people.Draft{Name: "Meera", Relationship: "child", BirthDate: time.Now().AddDate(-1, 0, 0).Format("2006-01-02")})
	if err != nil {
		t.Fatal(err)
	}
	var export strings.Builder
	export.WriteString("id,type,occurred_at,kg\n")
	for i := 0; i < 20; i++ {
		at := time.Now().AddDate(0, 0, -150+i*7).UTC().Format(time.RFC3339)
		export.WriteString("w" + strconv.Itoa(i) + ",weight," + at + "," + strconv.FormatFloat(3.2+float64(i)*0.15, 'f', 2, 64) + "\n")
	}
	if imported := uploadLog(t, application, session, "/family/"+baby.ID+"/log", export.String()); imported.Code != http.StatusSeeOther {
		t.Fatalf("import status=%d", imported.Code)
	}
	for _, child := range []people.Person{blank, baby} {
		for _, focus := range append([]string{""}, people.Focuses...) {
			if focus != "" {
				if err := application.people.SetFocus(ctx, scope, child.ID, focus); err != nil {
					t.Fatal(err)
				}
			}
			for _, days := range []string{"7", "30", "90"} {
				response := get("/family/" + child.ID + "/patterns?days=" + days)
				if response.Code != http.StatusOK {
					t.Fatalf("%s focus=%q days=%s status=%d", child.Name, focus, days, response.Code)
				}
				body := response.Body.String()
				switch {
				case child.ID == blank.ID || days == "7":
					if !strings.Contains(body, "Nothing logged in these days yet.") {
						t.Fatalf("%s focus=%q days=%s: no empty state", child.Name, focus, days)
					}
				case !strings.Contains(body, `pattern-lead"><span class="panel-label">Weight</span>`):
					t.Fatalf("%s focus=%q days=%s: weight does not lead", child.Name, focus, days)
				}
			}
		}
	}
}
