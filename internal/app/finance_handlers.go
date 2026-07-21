package app

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/finance"
)

type FinanceView struct {
	Navigation      []NavigationItem
	CSRF            string
	Scope           string
	Range           int
	HasRecords      bool
	CompleteCount   int
	IncompleteCount int
	Totals          []FinanceTotalView
	Trends          []FinanceTrendView
	Obligations     []FinanceObligationView
	Issues          []FinanceIssueView
	Records         []FinanceRecordView
	Status          string
	Error           string
}

type FinanceTotalView struct {
	Label string
	Value string
	Count int
}

type FinanceTrendView struct {
	ID                string
	Category          string
	PeriodLabel       string
	ChangeText        string
	AccessibleSummary string
	Points            string
	TrendlinePoints   string
}

type FinanceObligationView struct {
	Name        string
	Amount      string
	DateISO     string
	DateLabel   string
	EvidenceURL string
}

type FinanceIssueView struct {
	ID, Label, Kind, Reason, Date, EndDate, Amount, EvidenceURL string
	Version                                                     int64
}

type FinanceRecordView struct {
	ID          string
	Label       string
	Kind        string
	Category    string
	Date        string
	Amount      string
	Scope       string
	EvidenceURL string
	Version     int64
}

func (a *App) financeLens(w http.ResponseWriter, r *http.Request) {
	if !allowsRead(r.Method) {
		methodNotAllowed(w)
		return
	}
	scope, csrf, ok := a.authenticated(r)
	if !ok {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	if r.Method == http.MethodHead {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		return
	}
	filter := finance.ScopeFilter(r.URL.Query().Get("scope"))
	if filter != finance.SharedRecords && filter != finance.PersonalRecords {
		filter = finance.AllRecords
	}
	rangeMonths := financeTrendRange(r.URL.Query().Get("range"))
	summary, err := a.finance.SummarizeRange(r.Context(), scope, filter, time.Now(), rangeMonths)
	if err != nil {
		logRequestError(a.logger, r.Context(), "finance_query_failed")
		a.renderFinance(r.Context(), w, FinanceView{Navigation: navigationForPath("/finance"), CSRF: csrf, Scope: string(filter), Error: "Your information could not be loaded. Try again."})
		return
	}
	view := financeView(summary, filter, csrf, rangeMonths)
	if r.URL.Query().Get("corrected") == "1" {
		view.Status = "Your correction is now active. The original source remains linked."
	} else if r.URL.Query().Get("correction") == "failed" {
		view.Error = "That correction could not be saved. Reload the page and try again."
	}
	a.renderFinance(r.Context(), w, view)
}

func (a *App) correctFinanceRecord(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		methodNotAllowedFor(w, "POST")
		return
	}
	scope, _, ok := a.authenticated(r)
	if !ok {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	if !a.validSessionMutation(r, a.sessionCookie(r)) {
		http.Redirect(w, r, "/finance?correction=failed", http.StatusSeeOther)
		return
	}
	id := boundedField(r, "record_id", 128)
	kind := finance.Kind(boundedField(r, "kind", 32))
	version, err := strconv.ParseInt(r.PostForm.Get("version"), 10, 64)
	if err != nil || version < 1 || id == "" {
		http.Redirect(w, r, "/finance?correction=failed", http.StatusSeeOther)
		return
	}
	records, err := a.finance.List(r.Context(), scope, finance.AllRecords)
	if err != nil {
		http.Redirect(w, r, "/finance?correction=failed", http.StatusSeeOther)
		return
	}
	var current *finance.Record
	for index := range records {
		if records[index].ID == id && records[index].Kind == kind && records[index].Version == version {
			current = &records[index]
			break
		}
	}
	if current == nil {
		http.Redirect(w, r, "/finance?correction=failed", http.StatusSeeOther)
		return
	}
	date := current.Date
	if _, supplied := r.PostForm["date"]; supplied {
		date = boundedField(r, "date", 10)
	}
	amount := current.OriginalAmount
	if _, supplied := r.PostForm["amount"]; supplied {
		amount = boundedField(r, "amount", 128)
	}
	category := current.Category
	if _, supplied := r.PostForm["category"]; supplied {
		category = boundedField(r, "category", 128)
	}
	endDate := current.EndDate
	if kind == finance.Budget {
		if _, supplied := r.PostForm["end_date"]; supplied {
			endDate = boundedField(r, "end_date", 10)
		}
	}
	_, err = a.finance.Correct(r.Context(), scope, kind, id, version, finance.Draft{Visibility: current.Visibility, Label: current.Label, Category: category, Date: date, EndDate: endDate, Status: current.Status, AmountText: amount, CurrencyContext: "", Provenance: finance.Provenance{SourceID: current.SourceID, SourceFamily: current.SourceFamily, SourceVersion: current.SourceVersion, LocatorKind: current.LocatorKind, LocatorValue: current.LocatorValue, GeneratedBy: "user", Model: current.Model, PromptVersion: current.PromptVersion, SchemaVersion: current.SchemaVersion}})
	if err != nil {
		http.Redirect(w, r, "/finance?correction=failed", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/finance?corrected=1", http.StatusSeeOther)
}

func (a *App) renderFinance(ctx context.Context, w http.ResponseWriter, view FinanceView) {
	if view.Navigation == nil {
		view.Navigation = navigationForPath("/finance")
	}
	if view.Scope == "" {
		view.Scope = string(finance.AllRecords)
	}
	if view.Range == 0 {
		view.Range = 3
	}
	rendered := newBufferedResponse(maxResponseBodyBytes)
	if err := a.templates.ExecuteTemplate(rendered, "finance.html", view); err != nil || rendered.overflow {
		logRequestError(a.logger, ctx, "finance_render_failed")
		writeError(w, http.StatusInternalServerError, "internal server error")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	rendered.commit(w)
}

func financeView(summary finance.Summary, filter finance.ScopeFilter, csrf string, rangeMonths int) FinanceView {
	view := FinanceView{
		Navigation: navigationForPath("/finance"), CSRF: csrf, Scope: string(filter), HasRecords: len(summary.Records) > 0,
		Range: rangeMonths, CompleteCount: summary.Complete, IncompleteCount: summary.Incomplete,
	}
	for _, item := range []struct {
		kind  finance.Kind
		label string
	}{{finance.Income, "Income"}, {finance.Spending, "Spending"}, {finance.Asset, "Assets"}, {finance.Liability, "Liabilities"}, {finance.Budget, "Budgets"}, {finance.Obligation, "Obligations"}} {
		view.Totals = append(view.Totals, FinanceTotalView{Label: item.label, Value: summary.Totals[item.kind].PlainString(), Count: summary.Counts[item.kind]})
	}
	for index, trend := range summary.Trends {
		change := trend.Change.PlainString()
		periodLabel := trend.Months[0].Month.Format("January 2006") + " to " + trend.Months[len(trend.Months)-1].Month.Format("January 2006")
		changeText := trend.CurrentPeriod + " " + trend.Current.PlainString() + " compared with " + trend.PreviousPeriod + " " + trend.Previous.PlainString() + ": " + signedNumber(change) + ". Overall direction: " + trendDirection(trend.Trendline) + "."
		if trend.PreviousCount == 0 {
			changeText = trend.CurrentPeriod + " " + trend.Current.PlainString() + "; no prior record"
		}
		values := make([]string, 0, len(trend.Months))
		for _, month := range trend.Months {
			values = append(values, month.Month.Format("January 2006")+" "+month.Value.PlainString())
		}
		view.Trends = append(view.Trends, FinanceTrendView{
			ID: strconv.Itoa(index + 1), Category: trend.Category, PeriodLabel: periodLabel,
			ChangeText:        changeText,
			AccessibleSummary: strings.Join(values, "; ") + ". Overall direction: " + trendDirection(trend.Trendline) + ".",
			Points:            chartPoints(trend.Months, trend.Trendline, false),
			TrendlinePoints:   chartPoints(trend.Months, trend.Trendline, true),
		})
	}
	for _, record := range summary.Upcoming {
		date, _ := time.Parse("2006-01-02", record.Date)
		view.Obligations = append(view.Obligations, FinanceObligationView{Name: record.Label, Amount: record.Amount.PlainString(), DateISO: record.Date, DateLabel: date.Format("2 Jan 2006"), EvidenceURL: sourceURL(record.SourceID)})
	}
	recordsByID := make(map[string]finance.Record, len(summary.Records))
	for _, record := range summary.Records {
		recordsByID[record.ID] = record
	}
	for _, issue := range summary.Issues {
		record := recordsByID[issue.RecordID]
		date := record.Date
		if _, err := time.Parse("2006-01-02", date); err != nil {
			date = ""
		}
		endDate := record.EndDate
		if _, err := time.Parse("2006-01-02", endDate); err != nil {
			endDate = ""
		}
		view.Issues = append(view.Issues, FinanceIssueView{ID: record.ID, Label: issue.Label, Kind: string(issue.Kind), Reason: issue.Reason, Date: date, EndDate: endDate, Amount: record.OriginalAmount, Version: record.Version, EvidenceURL: sourceURL(issue.SourceID)})
	}
	for _, record := range summary.Records {
		amount := "Excluded"
		if record.Amount != nil {
			amount = record.Amount.PlainString()
		}
		date := record.Date
		if parsed, err := time.Parse("2006-01-02", record.Date); err == nil {
			date = parsed.Format("2 Jan 2006")
		}
		scope := "Shared"
		if record.Visibility == "personal" {
			scope = "Only you"
		}
		view.Records = append(view.Records, FinanceRecordView{ID: record.ID, Label: record.Label, Kind: string(record.Kind), Category: record.Category, Date: date, Amount: amount, Scope: scope, EvidenceURL: sourceURL(record.SourceID), Version: record.Version})
	}
	return view
}

func chartPoints(months []finance.MonthlyValue, trendline []float64, useTrendline bool) string {
	values := make([]float64, len(months))
	minimum, maximum := 0.0, 0.0
	for index, month := range months {
		value, _ := strconv.ParseFloat(month.Value.PlainString(), 64)
		values[index] = value
		minimum = math.Min(minimum, value)
		maximum = math.Max(maximum, value)
	}
	for _, value := range trendline {
		minimum = math.Min(minimum, value)
		maximum = math.Max(maximum, value)
	}
	if maximum == minimum {
		maximum = minimum + 1
	}
	points := make([]string, len(months))
	for index := range months {
		value := values[index]
		if useTrendline && index < len(trendline) {
			value = trendline[index]
		}
		x := 12.0
		if len(months) > 1 {
			x += 196 * float64(index) / float64(len(months)-1)
		}
		y := 40 - ((value-minimum)/(maximum-minimum))*30
		points[index] = fmt.Sprintf("%.1f,%.1f", x, y)
	}
	return strings.Join(points, " ")
}

func financeTrendRange(value string) int {
	switch value {
	case "6":
		return 6
	case "12":
		return 12
	default:
		return 3
	}
}

func trendDirection(values []float64) string {
	if len(values) < 2 {
		return "flat"
	}
	first, last := values[0], values[len(values)-1]
	if last > first {
		return "rising"
	}
	if last < first {
		return "falling"
	}
	return "flat"
}

func signedNumber(value string) string {
	if strings.HasPrefix(value, "-") || value == "0" {
		return value
	}
	return "+" + value
}

func sourceURL(id string) string {
	return "/sources/" + url.PathEscape(id)
}

func (a *App) sourceFile(w http.ResponseWriter, r *http.Request) {
	if !allowsRead(r.Method) {
		methodNotAllowed(w)
		return
	}
	scope, ok := a.sessionScope(r)
	if !ok {
		http.Redirect(w, r, "/auth/login", http.StatusSeeOther)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/sources/")
	if id == "" || strings.Contains(id, "/") {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	metadata, err := a.sources.Info(r.Context(), scope, id)
	if err != nil || metadata.Family == "voice" {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	plaintext, source, err := a.sources.Read(r.Context(), scope, id)
	if err != nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	defer clear(plaintext)
	contentType := map[string]string{"text": "text/plain; charset=utf-8", "voice": "audio/webm", "csv": "text/csv; charset=utf-8", "xlsx": "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", "pdf": "application/pdf"}[source.Family]
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `inline; filename="mithra-source.`+source.Family+`"`)
	http.ServeContent(w, r, "mithra-source."+source.Family, source.CreatedAt, bytes.NewReader(plaintext))
}
