package app

import (
	"bytes"
	"context"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/glnarayanan/mithra/internal/people"
)

func TestBabyLogOnChildPage(t *testing.T) {
	application, mailer := newAuthTestApp(t, "owner@example.com")
	session := activate(t, application, mailer, "owner@example.com", "an owner secure password", nil)
	scope := ownerScope(t, application, session)
	ctx := context.Background()
	baby, err := application.people.Create(ctx, scope, people.Draft{Name: "Meera", Relationship: "child", BirthDate: time.Now().AddDate(0, -1, 0).Format("2006-01-02")})
	if err != nil {
		t.Fatal(err)
	}
	parent, _ := application.people.Create(ctx, scope, people.Draft{Name: "Amma", Relationship: "parent"})
	logPath := "/family/" + baby.ID + "/log"
	post := func(values url.Values) *httptest.ResponseRecorder {
		return serve(application, authenticatedHealthRequest(session, http.MethodPost, logPath, values))
	}
	page := func(target string) string {
		target, _, _ = strings.Cut(target, "#")
		return serve(application, authenticatedHealthRequest(session, http.MethodGet, target, nil)).Body.String()
	}

	if body := page("/family/" + parent.ID); strings.Contains(body, "Baby log") {
		t.Fatal("an adult's page shows the baby log")
	}
	if response := serve(application, authenticatedHealthRequest(session, http.MethodPost, "/family/"+parent.ID+"/log", url.Values{"action": {"diaper"}, "kind": {"wet"}})); response.Code != http.StatusNotFound {
		t.Fatalf("adult log status=%d", response.Code)
	}

	started := post(url.Values{"action": {"feed"}, "mode": {"breast"}, "side": {"left"}, "client_id": {"phone-a"}})
	if started.Code != http.StatusSeeOther || !strings.Contains(started.Header().Get("Location"), "?logged=") {
		t.Fatalf("feed status=%d location=%q", started.Code, started.Header().Get("Location"))
	}
	body := page(started.Header().Get("Location"))
	for _, want := range []string{"Baby log", "Feed running since", "Finish feed", "Breast feed started at", "Undo", "/assets/babylog.js"} {
		if !strings.Contains(body, want) {
			i := strings.Index(body, "baby-log")
			t.Fatalf("child page missing %q in %s", want, body[i:min(len(body), i+1500)])
		}
	}
	// The same client id from an offline retry does not log a second feed.
	post(url.Values{"action": {"feed"}, "mode": {"breast"}, "side": {"left"}, "client_id": {"phone-a"}})
	events, _ := application.carelog.List(ctx, scope, baby.ID, time.Now().Add(-time.Hour))
	if len(events) != 1 || !events[0].Running() {
		t.Fatalf("events=%#v", events)
	}
	if second := post(url.Values{"action": {"feed"}, "mode": {"breast"}, "side": {"right"}, "client_id": {"phone-b"}}); second.Code != http.StatusBadRequest || !strings.Contains(second.Body.String(), "A feed is already running") {
		t.Fatalf("overlapping feed status=%d", second.Code)
	}
	if response := post(url.Values{"action": {"finish"}, "event": {events[0].ID}}); response.Code != http.StatusSeeOther {
		t.Fatalf("finish status=%d", response.Code)
	}
	post(url.Values{"action": {"diaper"}, "kind": {"both"}})
	post(url.Values{"action": {"feed"}, "mode": {"bottle"}, "milk_type": {"formula"}, "offered_ml": {"90"}, "consumed_ml": {"60"}})
	if bad := post(url.Values{"action": {"feed"}, "mode": {"bottle"}, "offered_ml": {"60"}, "consumed_ml": {"90"}}); bad.Code != http.StatusBadRequest || !strings.Contains(bad.Body.String(), "Consumed cannot be more than offered") {
		t.Fatalf("bad bottle status=%d", bad.Code)
	}
	body = page("/family/" + baby.ID)
	for _, want := range []string{"Since the last feed started", "Bottle feed", "Formula, 60 of 90 ml", "Wet and dirty diaper", "Feeds a day", "Bottle intake", "60 ml, 1 breast"} {
		if !strings.Contains(body, want) {
			t.Fatalf("child page missing %q", want)
		}
	}

	events, _ = application.carelog.List(ctx, scope, baby.ID, time.Now().Add(-time.Hour))
	removed := post(url.Values{"action": {"delete"}, "event": {events[0].ID}})
	if !strings.Contains(page(removed.Header().Get("Location")), "Entry removed from the log.") {
		t.Fatal("no undo after remove")
	}
	post(url.Values{"action": {"restore"}, "event": {events[0].ID}})
	if after, _ := application.carelog.List(ctx, scope, baby.ID, time.Now().Add(-time.Hour)); len(after) != len(events) {
		t.Fatalf("restore left %d of %d", len(after), len(events))
	}

	csv := "id,baby,type,occurred_at,ended_at,mode,side,duration_min,milk_type,offered_ml,consumed_ml,kind,size,linked_feed,kg,title,note,actor,source,created_at\n" +
		"w1,Meera,weight,2026-10-02T09:00:00+05:30,,,,,,,,,,,3.2,,,,web,\n" +
		"f1,Meera,feed,2026-10-02T10:00:00+05:30,,breast,left,12,,,,,,,,,,,web,\n" +
		"w2,Meera,weight,2026-10-16T09:00:00+05:30,,,,,,,,,,,3.6,,,,web,\n"
	upload := func() *httptest.ResponseRecorder {
		var buffer bytes.Buffer
		writer := multipart.NewWriter(&buffer)
		writer.WriteField("csrf", session.csrf.Value)
		writer.WriteField("action", "import_weights")
		part, _ := writer.CreateFormFile("file", "tinytots.csv")
		part.Write([]byte(csv))
		writer.Close()
		request := httptest.NewRequest(http.MethodPost, logPath, &buffer)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		request.Header.Set("Origin", testOrigin)
		request.Header.Set("Sec-Fetch-Site", "same-origin")
		request.AddCookie(session.session)
		request.AddCookie(session.csrf)
		return serve(application, request)
	}
	first := upload()
	if first.Code != http.StatusSeeOther {
		t.Fatalf("import status=%d %q", first.Code, first.Body.String())
	}
	if body := page(first.Header().Get("Location")); !strings.Contains(body, "Imported 2 weights. Skipped 1 row that is not a weight.") || !strings.Contains(body, "3.6<small>kg</small>") {
		t.Fatal("import summary or latest weight missing")
	}
	if again := page(upload().Header().Get("Location")); !strings.Contains(again, "Imported 0 weights. 2 weights were already in the log.") {
		t.Fatal("second import was not a no-op")
	}
	if weights, _ := application.carelog.Weights(ctx, scope, baby.ID); len(weights) != 2 || weights[0].Details["kg"] != "3.2" || weights[0].Source != "import" {
		t.Fatalf("weights=%#v", weights)
	}
}
