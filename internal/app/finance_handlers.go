package app

import (
	"bytes"
	"context"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
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
	// Glance panels and the stacked monthly chart.
	DueTotal, DueNext       string
	DueCount                int
	MonthLabel, MonthSpent  string
	PreviousLabel           string
	PreviousSpent           string
	IncomeTotal             string
	AssetsTotal, LoansTotal string
	Chart                   FinanceStackView
}

// FinanceStackView is one stacked bar per month, one colour per top category.
type FinanceStackView struct {
	Width   float64
	Bars    []FinanceStackBar
	Legend  []FinanceLegendItem
	Grid    []FinanceGridLine
	Summary string
}

type FinanceStackBar struct {
	X, LabelX, TotalY float64
	Label, Total      string
	Segments          []FinanceStackSegment
}

type FinanceStackSegment struct {
	Y, Height float64
	Class     string
	Title     string
}

type FinanceLegendItem struct{ Label, Class string }

type FinanceGridLine struct {
	Y     float64
	Label string
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
	StartLabel        string
	EndLabel          string
	StartValue        string
	EndValue          string
	ChangeText        string
	AccessibleSummary string
	Points            string
	TrendlinePoints   string
	Markers           []FinanceChartMarkerView
}

type FinanceChartMarkerView struct {
	X, Y, Label, Value string
	Zero               bool
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
	Private     bool
	Area        string
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
	locale, _ := a.planningRecords.GetLocale(r.Context(), scope)
	currency, _ := a.planningRecords.GetCurrency(r.Context(), scope)
	view := financeView(summary, filter, csrf, rangeMonths, locale, currency)
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
	a.localize(ctx, rendered)
	rendered.commit(w)
}

func financeView(summary finance.Summary, filter finance.ScopeFilter, csrf string, rangeMonths int, locale, currency string) FinanceView {
	money := func(value string) string { return moneyIn(value, locale, currency) }
	view := FinanceView{
		Navigation: navigationForPath("/finance"), CSRF: csrf, Scope: string(filter), HasRecords: len(summary.Records) > 0,
		Range: rangeMonths, CompleteCount: summary.Complete, IncompleteCount: summary.Incomplete,
	}
	for _, item := range []struct {
		kind  finance.Kind
		label string
	}{{finance.Income, "Income"}, {finance.Spending, "Spending"}, {finance.Asset, "Assets"}, {finance.Liability, "Liabilities"}, {finance.Budget, "Budgets"}, {finance.Obligation, "Obligations"}} {
		view.Totals = append(view.Totals, FinanceTotalView{Label: item.label, Value: money(summary.Totals[item.kind].PlainString()), Count: summary.Counts[item.kind]})
	}
	view.IncomeTotal = money(summary.Totals[finance.Income].PlainString())
	view.AssetsTotal = money(summary.Totals[finance.Asset].PlainString())
	view.LoansTotal = money(summary.Totals[finance.Liability].PlainString())
	view.Chart = financeStack(summary.Trends, locale, currency)
	if len(summary.Trends) > 0 {
		months := summary.Trends[0].Months
		current, previous := []finance.Decimal{}, []finance.Decimal{}
		for _, trend := range summary.Trends {
			if n := len(trend.Months); n > 0 {
				current = append(current, trend.Months[n-1].Value)
				if n > 1 {
					previous = append(previous, trend.Months[n-2].Value)
				}
			}
		}
		if total, err := finance.Sum(current); err == nil {
			view.MonthSpent = money(total.PlainString())
			view.MonthLabel = months[len(months)-1].Month.Format("January")
		}
		if total, err := finance.Sum(previous); err == nil && len(months) > 1 {
			view.PreviousSpent = money(total.PlainString())
			view.PreviousLabel = months[len(months)-2].Month.Format("January")
		}
	}
	due := make([]finance.Decimal, 0, len(summary.Upcoming))
	for _, record := range summary.Upcoming {
		if record.Amount != nil {
			due = append(due, *record.Amount)
		}
	}
	view.DueCount = len(summary.Upcoming)
	if total, err := finance.Sum(due); err == nil {
		view.DueTotal = money(total.PlainString())
	}
	if len(summary.Upcoming) > 0 {
		if date, ok := parseISODate(summary.Upcoming[0].Date); ok {
			view.DueNext = humanDate(date)
		}
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
		points, trendline, markers := financeChartPoints(trend.Months, trend.Trendline)
		view.Trends = append(view.Trends, FinanceTrendView{
			ID: strconv.Itoa(index + 1), Category: trend.Category, PeriodLabel: periodLabel,
			StartLabel: trend.Months[0].Month.Format("Jan 2006"), EndLabel: trend.Months[len(trend.Months)-1].Month.Format("Jan 2006"),
			StartValue: trend.Months[0].Value.PlainString(), EndValue: trend.Months[len(trend.Months)-1].Value.PlainString(),
			ChangeText:        changeText,
			AccessibleSummary: strings.Join(values, "; ") + ". Overall direction: " + trendDirection(trend.Trendline) + ".",
			Points:            points,
			TrendlinePoints:   trendline,
			Markers:           markers,
		})
	}
	for _, record := range summary.Upcoming {
		date, _ := time.Parse("2006-01-02", record.Date)
		amount := ""
		if record.Amount != nil {
			amount = money(record.Amount.PlainString())
		}
		view.Obligations = append(view.Obligations, FinanceObligationView{Name: record.Label, Amount: amount, DateISO: record.Date, DateLabel: humanDate(date) + ", " + relativeDays(date, time.Now()), EvidenceURL: sourceURL(record.SourceID)})
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
	records := append([]finance.Record(nil), summary.Records...)
	sort.SliceStable(records, func(i, j int) bool { return records[i].Date > records[j].Date })
	for _, record := range records {
		amount := "Excluded"
		if record.Amount != nil {
			amount = money(record.Amount.PlainString())
			if record.Kind == finance.Income {
				amount = "+" + amount
			}
		}
		date := record.Date
		if parsed, ok := parseISODate(record.Date); ok {
			date = humanDate(parsed)
		}
		scope := "Shared"
		if record.Visibility == "personal" {
			scope = "Only you"
		}
		area := "money"
		if record.Kind == finance.Income || record.Kind == finance.Asset {
			area = "income"
		}
		view.Records = append(view.Records, FinanceRecordView{ID: record.ID, Label: record.Label, Kind: string(record.Kind), Category: record.Category, Date: date, Amount: amount, Scope: scope, EvidenceURL: sourceURL(record.SourceID), Version: record.Version, Private: record.Visibility == "personal", Area: area})
	}
	return view
}

func financeChartPoints(months []finance.MonthlyValue, trendline []float64) (string, string, []FinanceChartMarkerView) {
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
	trendlinePoints := make([]string, len(months))
	markers := make([]FinanceChartMarkerView, len(months))
	for index := range months {
		x := 16.0
		if len(months) > 1 {
			x += 208 * float64(index) / float64(len(months)-1)
		}
		y := 68 - ((values[index]-minimum)/(maximum-minimum))*48
		points[index] = fmt.Sprintf("%.1f,%.1f", x, y)
		trendValue := values[index]
		if index < len(trendline) {
			trendValue = trendline[index]
		}
		trendY := 68 - ((trendValue-minimum)/(maximum-minimum))*48
		trendlinePoints[index] = fmt.Sprintf("%.1f,%.1f", x, trendY)
		markers[index] = FinanceChartMarkerView{X: fmt.Sprintf("%.1f", x), Y: fmt.Sprintf("%.1f", y), Label: months[index].Month.Format("January 2006"), Value: months[index].Value.PlainString(), Zero: values[index] == 0}
	}
	return strings.Join(points, " "), strings.Join(trendlinePoints, " "), markers
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

// financeStack lays out one stacked bar per month. The four largest
// categories keep their own colour; the rest share one "Other" segment.
func financeStack(trends []finance.CategoryTrend, locale, currency string) FinanceStackView {
	if len(trends) == 0 || len(trends[0].Months) == 0 {
		return FinanceStackView{}
	}
	type series struct {
		name   string
		values []float64
		total  float64
	}
	months := trends[0].Months
	all := make([]series, 0, len(trends))
	for _, trend := range trends {
		s := series{name: trend.Category, values: make([]float64, len(months))}
		for i := range months {
			if i < len(trend.Months) {
				value, _ := strconv.ParseFloat(trend.Months[i].Value.PlainString(), 64)
				s.values[i] = math.Max(value, 0)
				s.total += s.values[i]
			}
		}
		all = append(all, s)
	}
	for i := 1; i < len(all); i++ {
		for j := i; j > 0 && all[j].total > all[j-1].total; j-- {
			all[j], all[j-1] = all[j-1], all[j]
		}
	}
	shown := all
	if len(all) > 4 {
		other := series{name: "Other", values: make([]float64, len(months))}
		for _, s := range all[4:] {
			for i, value := range s.values {
				other.values[i] += value
			}
		}
		shown = append(append([]series{}, all[:4]...), other)
	}
	totals := make([]float64, len(months))
	maximum := 0.0
	for i := range months {
		for _, s := range shown {
			totals[i] += s.values[i]
		}
		maximum = math.Max(maximum, totals[i])
	}
	if maximum <= 0 {
		maximum = 1
	}
	maximum = niceCeiling(maximum/3) * 3
	const top, bottom, left, right = 18.0, 160.0, 56.0, 412.0
	scale := func(value float64) float64 { return (bottom - top) * value / maximum }
	view := FinanceStackView{}
	for step := 0; step <= 3; step++ {
		value := maximum * float64(step) / 3
		view.Grid = append(view.Grid, FinanceGridLine{Y: math.Round((bottom-scale(value))*10) / 10, Label: groupDigitsIn(strconv.FormatFloat(value, 'f', 0, 64), locale)})
	}
	slot := (right - left) / float64(len(months))
	width := math.Round(math.Min(64, slot*0.6)*10) / 10
	view.Width = width
	summary := make([]string, 0, len(months))
	for i, month := range months {
		x := left + slot*float64(i) + (slot-width)/2
		bar := FinanceStackBar{X: math.Round(x*10) / 10, LabelX: math.Round((x+width/2)*10) / 10, Label: month.Month.Format("Jan"), Total: moneyIn(strconv.FormatFloat(totals[i], 'f', -1, 64), locale, currency)}
		y := bottom
		for index, s := range shown {
			if s.values[i] <= 0 {
				continue
			}
			height := scale(s.values[i])
			y -= height
			bar.Segments = append(bar.Segments, FinanceStackSegment{Y: math.Round(y*10) / 10, Height: math.Round(height*10) / 10, Class: "c" + strconv.Itoa(index+1), Title: s.name + ": " + moneyIn(strconv.FormatFloat(s.values[i], 'f', -1, 64), locale, currency)})
		}
		bar.TotalY = math.Round((y-6)*10) / 10
		view.Bars = append(view.Bars, bar)
		summary = append(summary, month.Month.Format("January 2006")+" "+bar.Total)
	}
	for index, s := range shown {
		view.Legend = append(view.Legend, FinanceLegendItem{Label: s.name, Class: "c" + strconv.Itoa(index+1)})
	}
	view.Summary = "Spending by month, stacked by category: " + strings.Join(summary, "; ") + "."
	return view
}

func niceCeiling(value float64) float64 {
	magnitude := math.Pow(10, math.Floor(math.Log10(value)))
	for _, step := range []float64{1, 1.5, 2, 3, 5, 10} {
		if value <= step*magnitude {
			return step * magnitude
		}
	}
	return 10 * magnitude
}
