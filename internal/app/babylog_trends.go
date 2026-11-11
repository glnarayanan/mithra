package app

import (
	"encoding/json"
	"math"
	"net/url"
	"sort"
	"strconv"
	"time"

	"github.com/glnarayanan/mithra/internal/carelog"
	"github.com/glnarayanan/mithra/internal/people"
)

// Focus options a child's page can lead with.
const (
	focusFeeds   = "feeds"
	focusWeight  = "weight"
	focusBottle  = "bottle"
	focusDiapers = "diapers"
)

// FocusOption is one choice on the child settings page.
type FocusOption struct {
	Value, Label, Note string
	Selected           bool
}

var focusLabels = map[string][2]string{
	focusFeeds:   {"Time since the last feed", "The clock since a feed started, with today's feeds and diapers."},
	focusWeight:  {"Weight", "The latest weight, the change since the one before, and the weight chart."},
	focusBottle:  {"Bottle intake", "Millilitres taken today, with the average over the last seven days."},
	focusDiapers: {"Diapers", "Wet and dirty diapers today, with the average over the last seven days."},
}

func childFocus(person people.Person) string {
	if _, ok := focusLabels[person.Focus]; ok {
		return person.Focus
	}
	return focusFeeds
}

func focusOptions(selected string) []FocusOption {
	out := make([]FocusOption, 0, len(people.Focuses))
	for _, value := range people.Focuses {
		out = append(out, FocusOption{Value: value, Label: focusLabels[value][0], Note: focusLabels[value][1], Selected: value == selected})
	}
	return out
}

// Chart data is drawn in the browser at the width it is shown. The server
// does every count and every date label, so the page script only draws.
type chartDay struct {
	Key   string `json:"key"`
	Short string `json:"short"`
	Num   string `json:"num"`
	Long  string `json:"long"`
	Mark  string `json:"mark"`
}

type chartEvent struct {
	Day    int     `json:"d"`
	Minute int     `json:"m"`
	Type   string  `json:"t"`
	ML     float64 `json:"ml,omitempty"`
	Kind   string  `json:"k,omitempty"`
}

type chartWeight struct {
	At    int64   `json:"at"`
	KG    float64 `json:"kg"`
	Label string  `json:"label"`
}

type chartData struct {
	Days      []chartDay    `json:"days,omitempty"`
	Events    []chartEvent  `json:"events,omitempty"`
	Weights   []chartWeight `json:"weights,omitempty"`
	From      int64         `json:"from"`
	To        int64         `json:"to"`
	FromLabel string        `json:"fromLabel"`
	ToLabel   string        `json:"toLabel"`
}

func (c chartData) JSON() string {
	raw, _ := json.Marshal(c)
	return string(raw)
}

// TrendFigure is one figure above a chart: "+32 g" over "a day on average".
type TrendFigure struct{ Value, Label string }

// PatternChart is one chart block on the patterns page.
type PatternChart struct {
	ID, Title, Kind, Empty, Note string
	Figures                      []TrendFigure
	Data                         string
	Legend                       []string
}

// PatternsView is the patterns page for one child.
type PatternsView struct {
	Navigation []NavigationItem
	CSRF       string
	Person     FamilyPersonView
	PageURL    string
	Ranges     []FamilyOptionView
	Days       int
	Caption    string
	// Lead is the child's chosen measure, shown in a colour panel; Facts
	// are the other figures, in a hairline row under it.
	Lead    *BabyPatternView
	Facts   []BabyPatternView
	Charts  []PatternChart
	Heading string
	Empty   bool
}

var patternRanges = []int{7, 14, 30, 90}

func patternDays(raw string) int {
	days, _ := strconv.Atoi(raw)
	for _, allowed := range patternRanges {
		if days == allowed {
			return allowed
		}
	}
	return 7
}

// maxPatternDays bounds every per-day slice, whatever the caller passes.
const maxPatternDays = 90

func dayKeys(now time.Time, days int) []time.Time {
	if days < 1 || days > maxPatternDays {
		days = 7
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	out := make([]time.Time, days)
	for i := range out {
		out[i] = midnight.AddDate(0, 0, i-days+1)
	}
	return out
}

func dayIndex(days []time.Time, at time.Time) int {
	at = at.In(days[0].Location())
	day := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, at.Location())
	for i, d := range days {
		if d.Equal(day) {
			return i
		}
	}
	return -1
}

func bottleML(e carelog.Event) (float64, bool) {
	if e.Type != carelog.Feed {
		return 0, false
	}
	ml, err := strconv.ParseFloat(firstNonEmpty(e.Details["consumed_ml"], e.Details["offered_ml"]), 64)
	return ml, err == nil
}

func diaperIs(e carelog.Event, kind string) bool {
	k := e.Details["kind"]
	return e.Type == carelog.Diaper && (k == kind || k == "both")
}

// weightChartData plots weights against a time axis from..to.
func weightChartData(weights []carelog.Event, from, to time.Time) chartData {
	data := chartData{From: from.UnixMilli(), To: to.UnixMilli(), FromLabel: shortDate(from), ToLabel: shortDate(to)}
	for _, w := range weights {
		if w.OccurredAt.Before(from) || w.OccurredAt.After(to) {
			continue
		}
		kg, err := strconv.ParseFloat(w.Details["kg"], 64)
		if err != nil {
			continue
		}
		at := w.OccurredAt.In(from.Location())
		data.Weights = append(data.Weights, chartWeight{At: at.UnixMilli(), KG: kg, Label: at.Format("Mon 2 Jan 2006")})
	}
	return data
}

// slopePerDay is the least-squares change in kg a day, or false when the
// weights are too close together to say.
func slopePerDay(weights []chartWeight) (float64, bool) {
	if len(weights) < 2 {
		return 0, false
	}
	var mx, my float64
	for _, w := range weights {
		mx += float64(w.At) / 864e5
		my += w.KG
	}
	mx /= float64(len(weights))
	my /= float64(len(weights))
	var num, den float64
	for _, w := range weights {
		dx := float64(w.At)/864e5 - mx
		num += dx * (w.KG - my)
		den += dx * dx
	}
	if den < 0.01 {
		return 0, false
	}
	return num / den, true
}

func signed(value float64, digits int) string {
	rounded := math.Round(value*math.Pow(10, float64(digits))) / math.Pow(10, float64(digits))
	if rounded == 0 {
		return strconv.FormatFloat(0, 'f', digits, 64)
	}
	sign := "+"
	if rounded < 0 {
		sign = "−"
	}
	return sign + strconv.FormatFloat(math.Abs(rounded), 'f', digits, 64)
}

// weightChange states a change in grams under a kilo and kilograms above.
func weightChange(kg float64) string {
	if math.Abs(kg) >= 1 {
		return signed(kg, 2) + " kg"
	}
	return signed(kg*1000, 0) + " g"
}

func weightFigures(data chartData) ([]TrendFigure, string) {
	w := data.Weights
	switch len(w) {
	case 0:
		return nil, ""
	case 1:
		return []TrendFigure{{formatKG(w[0].KG) + " kg", "on " + w[0].Label}}, "Log another weight to see the change between them."
	}
	var figures []TrendFigure
	if slope, ok := slopePerDay(w); ok {
		figures = append(figures, TrendFigure{signed(slope*1000, 0) + " g", "a day on average"})
	}
	first := time.UnixMilli(w[0].At)
	figures = append(figures, TrendFigure{weightChange(w[len(w)-1].KG - w[0].KG), "since " + shortDate(first)}, TrendFigure{formatKG(w[len(w)-1].KG) + " kg", "latest"})
	return figures, ""
}

func formatKG(kg float64) string {
	return strconv.FormatFloat(kg, 'f', -1, 64)
}

// patternsView works out the facts and charts for the last `days` days.
func patternsView(person people.Person, events, allWeights []carelog.Event, now time.Time, days int) PatternsView {
	location := now.Location()
	keys := dayKeys(now, days)
	days = len(keys)
	view := PatternsView{Days: days, PageURL: "/family/" + url.PathEscape(person.ID) + "/patterns"}
	for _, d := range patternRanges {
		view.Ranges = append(view.Ranges, FamilyOptionView{Value: view.PageURL + "?days=" + strconv.Itoa(d), Label: strconv.Itoa(d) + " days", Selected: d == days})
	}
	view.Heading = "Last " + plural(days, "day", "days")
	view.Caption = shortDate(keys[0]) + " to " + shortDate(keys[len(keys)-1])

	data := chartData{From: keys[0].UnixMilli(), To: now.UnixMilli()}
	for _, k := range keys {
		data.Days = append(data.Days, chartDay{Key: k.Format("2006-01-02"), Short: k.Format("Mon"), Num: k.Format("2"), Long: k.Format("Mon 2 Jan"), Mark: k.Format("2 Jan")})
	}
	feedsPer := make([]int, days)
	wetPer, dirtyPer, spitPer := make([]int, days), make([]int, days), make([]int, days)
	mlPer := make([]float64, days)
	bottles := 0
	first := -1
	var feedTimes []time.Time
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		index := dayIndex(keys, e.OccurredAt)
		if index < 0 || e.Type == carelog.Weight || e.Type == carelog.Milestone {
			continue
		}
		if first < 0 || index < first {
			first = index
		}
		at := e.OccurredAt.In(location)
		point := chartEvent{Day: index, Minute: at.Hour()*60 + at.Minute(), Type: e.Type}
		switch e.Type {
		case carelog.Feed:
			feedsPer[index]++
			feedTimes = append(feedTimes, e.OccurredAt)
			if ml, ok := bottleML(e); ok {
				mlPer[index] += ml
				bottles++
				point.ML = ml
			}
		case carelog.Diaper:
			point.Kind = e.Details["kind"]
			if diaperIs(e, "wet") {
				wetPer[index]++
			}
			if diaperIs(e, "dirty") {
				dirtyPer[index]++
			}
		case carelog.SpitUp:
			spitPer[index]++
		}
		data.Events = append(data.Events, point)
	}
	sort.Slice(feedTimes, func(i, j int) bool { return feedTimes[i].Before(feedTimes[j]) })

	// Averages count only days since logging began, so a 30-day view of a
	// week-old log is not diluted by empty days.
	logged := 0
	if first >= 0 {
		logged = days - first
	}
	sum := func(values []int) (total int) {
		for _, v := range values[max(first, 0):] {
			total += v
		}
		return total
	}
	perDay := func(total float64) string { return formatAmount(total / float64(max(logged, 1))) }
	var mlTotal float64
	for _, v := range mlPer {
		mlTotal += v
	}

	// Gaps between feed starts, ignoring overnight breaks over 12 hours.
	var gaps []time.Duration
	var longest time.Duration
	for i := 1; i < len(feedTimes); i++ {
		gap := feedTimes[i].Sub(feedTimes[i-1])
		longest = max(longest, gap)
		if gap > 0 && gap < 12*time.Hour {
			gaps = append(gaps, gap)
		}
	}
	sort.Slice(gaps, func(i, j int) bool { return gaps[i] < gaps[j] })
	hm := func(d time.Duration) string {
		d = d.Round(time.Minute)
		return strconv.Itoa(int(d/time.Hour)) + "h " + strconv.Itoa(int(d%time.Hour/time.Minute)) + "m"
	}

	if logged > 0 {
		view.Facts = append(view.Facts, BabyPatternView{Key: focusFeeds, Label: "Feeds a day", Value: perDay(float64(sum(feedsPer))), Note: plural(sum(feedsPer), "feed", "feeds") + " in " + plural(logged, "day", "days")})
		if len(gaps) > 0 {
			view.Facts = append(view.Facts, BabyPatternView{Label: "Gap between feeds", Value: hm(gaps[len(gaps)/2]), Note: "Median of " + plural(len(gaps), "gap", "gaps")})
		}
		if bottles > 0 {
			view.Facts = append(view.Facts, BabyPatternView{Key: focusBottle, Label: "Bottle intake", Value: strconv.FormatFloat(mlTotal/float64(logged), 'f', 0, 64), Unit: "ml a day", Note: strconv.FormatFloat(mlTotal/float64(bottles), 'f', 0, 64) + " ml a bottle"})
		}
		view.Facts = append(view.Facts, BabyPatternView{Key: focusDiapers, Label: "Wet diapers", Value: perDay(float64(sum(wetPer))), Unit: "a day", Note: "Dirty " + perDay(float64(sum(dirtyPer))) + " a day"})
	}

	rhythm := PatternChart{ID: "rhythm", Title: "Daily rhythm", Kind: "rhythm", Legend: []string{"feed", "diaper", "spitup"}, Empty: "No feeds, diapers or spit-ups in these days."}
	if logged > 0 {
		if longest > 0 {
			rhythm.Figures = append(rhythm.Figures, TrendFigure{hm(longest), "longest stretch between feeds"})
		}
		rhythm.Figures = append(rhythm.Figures, TrendFigure{perDay(float64(sum(spitPer))), "spit-ups a day"})
	}
	bottle := PatternChart{ID: "bottle", Title: "Bottle intake, ml", Kind: "bottle", Empty: "No bottles in these days."}
	if bottles > 0 {
		best := 0
		for i, v := range mlPer {
			if v > mlPer[best] {
				best = i
			}
		}
		bottle.Figures = []TrendFigure{{strconv.FormatFloat(mlTotal/float64(logged), 'f', 0, 64) + " ml", "a day on average"}, {strconv.FormatFloat(mlTotal/float64(bottles), 'f', 0, 64) + " ml", "a bottle on average"}, {strconv.FormatFloat(mlPer[best], 'f', 0, 64) + " ml", "most, " + data.Days[best].Long}}
	}
	diapers := PatternChart{ID: "diapers", Title: "Diapers", Kind: "diapers", Legend: []string{"wet", "dirty"}, Empty: "No diapers in these days."}
	if sum(wetPer)+sum(dirtyPer) > 0 {
		diapers.Figures = []TrendFigure{{perDay(float64(sum(wetPer))), "wet a day"}, {perDay(float64(sum(dirtyPer))), "dirty a day"}}
	}
	weightData := weightChartData(allWeights, keys[0], now)
	weight := PatternChart{ID: "weight", Title: "Weight, kg", Kind: "weight", Data: weightData.JSON()}
	weight.Figures, weight.Note = weightFigures(weightData)
	if len(weightData.Weights) == 0 {
		weight.Empty = "No weights in these days."
		if n := len(allWeights); n > 0 {
			last := allWeights[n-1]
			weight.Empty += " The last was " + last.Details["kg"] + " kg on " + shortDate(last.OccurredAt.In(location)) + "."
		}
	}
	shared := data.JSON()
	for _, c := range []*PatternChart{&rhythm, &bottle, &diapers} {
		c.Data = shared
	}
	if bottles == 0 {
		bottle.Data = ""
	}
	if logged == 0 {
		rhythm.Data, diapers.Data = "", ""
	}
	if len(weightData.Weights) == 0 {
		weight.Data = ""
	}
	order := map[string][]PatternChart{
		focusFeeds:   {rhythm, bottle, diapers, weight},
		focusWeight:  {weight, rhythm, bottle, diapers},
		focusBottle:  {bottle, rhythm, diapers, weight},
		focusDiapers: {diapers, rhythm, bottle, weight},
	}
	view.Charts = order[childFocus(person)]
	focus := childFocus(person)
	var weightLead *BabyPatternView
	if len(weightData.Weights) > 0 {
		last := weightData.Weights[len(weightData.Weights)-1]
		lead := BabyPatternView{Key: focusWeight, Label: "Weight", Value: formatKG(last.KG), Unit: "kg", Note: "Latest, " + last.Label}
		if len(weight.Figures) == 3 {
			lead.Note = weight.Figures[0].Value + " " + weight.Figures[0].Label + ", " + weight.Figures[1].Value + " " + weight.Figures[1].Label
		} else if len(weight.Figures) == 2 {
			lead.Note = weight.Figures[0].Value + " " + weight.Figures[0].Label
		}
		weightLead = &lead
	}
	if focus == focusWeight {
		view.Lead = weightLead
	}
	for i := 0; view.Lead == nil && i < len(view.Facts); i++ {
		if view.Facts[i].Key == focus {
			lead := view.Facts[i]
			view.Lead = &lead
			view.Facts = append(view.Facts[:i:i], view.Facts[i+1:]...)
		}
	}
	if view.Lead == nil && len(view.Facts) > 0 {
		lead := view.Facts[0]
		view.Lead, view.Facts = &lead, view.Facts[1:]
	}
	// A log with only weights in these days (an import of past weights, say)
	// leads with weight whatever the chosen focus.
	if view.Lead == nil {
		view.Lead = weightLead
	}
	if view.Lead == nil {
		view.Empty = true
		return view
	}
	// The lead panel already states its chart's figures; keep only what it
	// does not say.
	for i := range view.Charts {
		c := &view.Charts[i]
		switch {
		case view.Lead.Key == focusWeight && c.ID == "weight", view.Lead.Key == focusDiapers && c.ID == "diapers":
			c.Figures = nil
		case view.Lead.Key == focusBottle && c.ID == "bottle" && len(c.Figures) == 3:
			c.Figures = c.Figures[2:]
		}
	}
	view.Empty = logged == 0 && len(weightData.Weights) == 0
	return view
}
