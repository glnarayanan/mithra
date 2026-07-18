package finance

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/policy"
)

type Kind string

const (
	Income     Kind = "income"
	Spending   Kind = "spending"
	Asset      Kind = "asset"
	Liability  Kind = "liability"
	Budget     Kind = "budget"
	Obligation Kind = "obligation"
)

var (
	ErrInvalidRecord    = errors.New("finance record is invalid")
	ErrCurrencyContexts = errors.New("multiple currency contexts are unsupported")
)

type ScopeFilter string

const (
	AllRecords      ScopeFilter = "all"
	SharedRecords   ScopeFilter = "shared"
	PersonalRecords ScopeFilter = "personal"
)

type Provenance struct {
	SourceID      string
	SourceFamily  string
	SourceVersion int64
	LocatorKind   string
	LocatorValue  string
	GeneratedBy   string
	Model         string
	PromptVersion string
	SchemaVersion string
}

type Draft struct {
	Kind            Kind
	Visibility      policy.Visibility
	Label           string
	Category        string
	Date            string
	EndDate         string
	Status          string
	AmountText      string
	IncompleteNote  string
	CurrencyContext string // validation-only; never persisted.
	Provenance      Provenance
}

type Record struct {
	ID               string
	Kind             Kind
	HouseholdID      string
	OwnerID          string
	Visibility       policy.Visibility
	Label            string
	Category         string
	Date             string
	EndDate          string
	Status           string
	Amount           *Decimal
	OriginalAmount   string
	IncompleteReason string
	SourceID         string
	SourceFamily     string
	SourceVersion    int64
	LocatorKind      string
	LocatorValue     string
	GeneratedBy      string
	Model            string
	PromptVersion    string
	SchemaVersion    string
	DataRevision     int64
	SupersedesID     string
	Version          int64
	CreatedAt        time.Time
}

type Issue struct {
	RecordID     string
	Kind         Kind
	Label        string
	Reason       string
	SourceID     string
	LocatorKind  string
	LocatorValue string
}

type CategoryTrend struct {
	Category       string
	Previous       Decimal
	Current        Decimal
	Change         Decimal
	PreviousCount  int
	CurrentCount   int
	PreviousPeriod string
	CurrentPeriod  string
}

type Summary struct {
	Records    []Record
	Totals     map[Kind]Decimal
	Counts     map[Kind]int
	Issues     []Issue
	Trends     []CategoryTrend
	Upcoming   []Record
	Complete   int
	Incomplete int
}

type Service struct {
	db  *sql.DB
	now func() time.Time
}

func New(db *sql.DB) *Service {
	return &Service{db: db, now: time.Now}
}

func ValidateCurrencyContexts(contexts []string) error {
	seen := make(map[string]struct{})
	for _, raw := range contexts {
		context := strings.ToUpper(strings.TrimSpace(raw))
		if context == "" {
			continue
		}
		seen[context] = struct{}{}
		if len(seen) > 1 {
			return ErrCurrencyContexts
		}
	}
	return nil
}

func (s *Service) Create(ctx context.Context, actor policy.ActorScope, draft Draft) (Record, error) {
	if !actor.Valid() {
		return Record{}, policy.ErrUnauthorized
	}
	if err := authorizeActor(ctx, s.db, actor); err != nil {
		return Record{}, err
	}
	draft.Visibility = policy.PersonalDefault(draft.Visibility)
	if err := validateDraft(draft); err != nil {
		return Record{}, err
	}
	if err := ValidateCurrencyContexts([]string{draft.CurrencyContext}); err != nil {
		return Record{}, err
	}
	record, err := prepareRecord(actor, draft, s.now().UTC())
	if err != nil {
		return Record{}, err
	}
	record.ID, err = randomID()
	if err != nil {
		return Record{}, errors.New("create finance record identifier")
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("begin finance record: %w", err)
	}
	defer tx.Rollback()
	if err := setDataRevision(ctx, tx, &record); err != nil {
		return Record{}, err
	}
	if err := insertRecord(ctx, tx, record); err != nil {
		return Record{}, err
	}
	if err := linkRecord(ctx, tx, record); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("commit finance record: %w", err)
	}
	return record, nil
}

func (s *Service) Correct(ctx context.Context, actor policy.ActorScope, kind Kind, id string, expectedVersion int64, draft Draft) (Record, error) {
	if !actor.Valid() {
		return Record{}, policy.ErrUnauthorized
	}
	draft.Kind = kind
	draft.Visibility = policy.PersonalDefault(draft.Visibility)
	if err := validateDraft(draft); err != nil {
		return Record{}, err
	}
	if err := ValidateCurrencyContexts([]string{draft.CurrencyContext}); err != nil {
		return Record{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Record{}, fmt.Errorf("begin finance correction: %w", err)
	}
	defer tx.Rollback()
	if err := authorizeActor(ctx, tx, actor); err != nil {
		return Record{}, err
	}
	current, err := findRecord(ctx, tx, actor, kind, id)
	if err != nil {
		return Record{}, err
	}
	if err := actor.CanMutate(policy.Resource{HouseholdID: current.HouseholdID, OwnerID: current.OwnerID, Visibility: current.Visibility, Version: current.Version}, expectedVersion); err != nil {
		return Record{}, err
	}
	if draft.Visibility != current.Visibility {
		return Record{}, ErrInvalidRecord
	}
	result, err := tx.ExecContext(ctx, "UPDATE "+tableFor(kind)+" SET active=0,version=version+1,updated_at=? WHERE id=? AND active=1 AND version=?", s.now().UTC().Format(time.RFC3339Nano), id, expectedVersion)
	if err != nil {
		return Record{}, fmt.Errorf("supersede finance record: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Record{}, policy.ErrConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM search_entries WHERE record_family='finance' AND record_id=?`, id); err != nil {
		return Record{}, fmt.Errorf("remove superseded finance search entry: %w", err)
	}
	replacement, err := prepareRecord(actor, draft, s.now().UTC())
	if err != nil {
		return Record{}, err
	}
	replacement.ID, err = randomID()
	if err != nil {
		return Record{}, errors.New("create correction identifier")
	}
	replacement.SupersedesID = id
	if err := setDataRevision(ctx, tx, &replacement); err != nil {
		return Record{}, err
	}
	if err := insertRecord(ctx, tx, replacement); err != nil {
		return Record{}, err
	}
	if err := linkRecord(ctx, tx, replacement); err != nil {
		return Record{}, err
	}
	if err := tx.Commit(); err != nil {
		return Record{}, fmt.Errorf("commit finance correction: %w", err)
	}
	return replacement, nil
}

func (s *Service) List(ctx context.Context, actor policy.ActorScope, filter ScopeFilter) ([]Record, error) {
	if !actor.Valid() {
		return nil, policy.ErrUnauthorized
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin finance read: %w", err)
	}
	defer tx.Rollback()
	if err := authorizeActor(ctx, tx, actor); err != nil {
		return nil, err
	}
	if filter != SharedRecords && filter != PersonalRecords {
		filter = AllRecords
	}
	query, args := unionQuery(actor, filter)
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list finance records: %w", err)
	}
	defer rows.Close()
	var records []Record
	for rows.Next() {
		record, err := scanRecord(rows)
		if err != nil {
			return nil, fmt.Errorf("read finance record: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list finance records: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("finish finance read: %w", err)
	}
	return records, nil
}

func (s *Service) Summarize(ctx context.Context, actor policy.ActorScope, filter ScopeFilter, asOf time.Time) (Summary, error) {
	records, err := s.List(ctx, actor, filter)
	if err != nil {
		return Summary{}, err
	}
	return summarize(records, asOf.UTC())
}

func prepareRecord(actor policy.ActorScope, draft Draft, now time.Time) (Record, error) {
	record := Record{
		Kind: draft.Kind, HouseholdID: actor.HouseholdID, OwnerID: actor.ActorID, Visibility: draft.Visibility,
		Label: strings.TrimSpace(draft.Label), Category: strings.TrimSpace(draft.Category), Date: strings.TrimSpace(draft.Date), EndDate: strings.TrimSpace(draft.EndDate),
		Status: strings.TrimSpace(draft.Status), OriginalAmount: strings.TrimSpace(draft.AmountText), SourceID: strings.TrimSpace(draft.Provenance.SourceID),
		SourceFamily: strings.TrimSpace(draft.Provenance.SourceFamily), SourceVersion: draft.Provenance.SourceVersion, LocatorKind: strings.TrimSpace(draft.Provenance.LocatorKind),
		LocatorValue: strings.TrimSpace(draft.Provenance.LocatorValue), GeneratedBy: strings.TrimSpace(draft.Provenance.GeneratedBy), Model: strings.TrimSpace(draft.Provenance.Model),
		PromptVersion: strings.TrimSpace(draft.Provenance.PromptVersion), SchemaVersion: strings.TrimSpace(draft.Provenance.SchemaVersion), Version: 1, CreatedAt: now,
	}
	if record.Category == "" {
		record.Category = "Uncategorized"
	}
	if record.GeneratedBy == "" {
		record.GeneratedBy = "application"
	}
	if record.SchemaVersion == "" {
		record.SchemaVersion = "finance-v1"
	}
	if record.Kind == Obligation && record.Status == "" {
		record.Status = "pending"
	}
	reasons := make([]string, 0, 3)
	amount, err := ParseAmount(record.OriginalAmount)
	if err != nil {
		if errors.Is(err, ErrBlank) {
			reasons = append(reasons, "amount is missing")
		} else {
			reasons = append(reasons, "amount needs correction")
		}
	} else {
		record.Amount = &amount
	}
	if !validISODate(record.Date) {
		if record.Date == "" {
			reasons = append(reasons, "date is missing")
		} else {
			reasons = append(reasons, "date needs correction")
		}
	}
	if record.Kind == Budget && !validISODate(record.EndDate) {
		reasons = append(reasons, "budget end date needs correction")
	}
	if note := strings.TrimSpace(draft.IncompleteNote); note != "" {
		reasons = append(reasons, note)
	}
	record.IncompleteReason = strings.Join(reasons, "; ")
	return record, nil
}

func validateDraft(draft Draft) error {
	if tableFor(draft.Kind) == "" || strings.TrimSpace(draft.Label) == "" || len(strings.TrimSpace(draft.Label)) > 256 || len(strings.TrimSpace(draft.Category)) > 128 {
		return ErrInvalidRecord
	}
	p := draft.Provenance
	if p.SourceID == "" || p.SourceVersion < 1 || p.LocatorKind == "" || p.LocatorValue == "" {
		return ErrInvalidRecord
	}
	switch p.SourceFamily {
	case "text", "voice", "csv", "xlsx", "pdf":
	default:
		return ErrInvalidRecord
	}
	if draft.Kind == Obligation && draft.Status != "" && draft.Status != "pending" && draft.Status != "paid" && draft.Status != "cancelled" {
		return ErrInvalidRecord
	}
	return nil
}

func validISODate(value string) bool {
	if len(value) != len("2006-01-02") {
		return false
	}
	_, err := time.Parse("2006-01-02", value)
	return err == nil
}

func randomID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func tableFor(kind Kind) string {
	switch kind {
	case Income:
		return "finance_income"
	case Spending:
		return "finance_spending"
	case Asset:
		return "finance_assets"
	case Liability:
		return "finance_liabilities"
	case Budget:
		return "finance_budgets"
	case Obligation:
		return "finance_obligations"
	default:
		return ""
	}
}

func dateColumn(kind Kind) string {
	switch kind {
	case Income:
		return "received_on"
	case Spending:
		return "spent_on"
	case Asset, Liability:
		return "observed_on"
	case Budget:
		return "starts_on"
	case Obligation:
		return "due_on"
	default:
		return ""
	}
}

func setDataRevision(ctx context.Context, tx *sql.Tx, record *Record) error {
	var query string
	var key string
	if record.Visibility == policy.Shared {
		query, key = "SELECT shared_revision FROM household_revisions WHERE household_id=?", record.HouseholdID
	} else {
		query, key = "SELECT personal_revision FROM user_revisions WHERE user_id=?", record.OwnerID
	}
	if err := tx.QueryRowContext(ctx, query, key).Scan(&record.DataRevision); err != nil {
		return fmt.Errorf("read finance data revision: %w", err)
	}
	return nil
}

func insertRecord(ctx context.Context, tx *sql.Tx, record Record) error {
	table := tableFor(record.Kind)
	date := dateColumn(record.Kind)
	columns := "id,household_id,owner_user_id,visibility,source_id,source_family,source_version,label,category," + date
	placeholders := "?,?,?,?,?,?,?,?,?,?"
	args := []any{record.ID, record.HouseholdID, record.OwnerID, record.Visibility, record.SourceID, record.SourceFamily, record.SourceVersion, record.Label, record.Category, record.Date}
	if record.Kind == Budget {
		columns += ",ends_on"
		placeholders += ",?"
		args = append(args, record.EndDate)
	}
	if record.Kind == Obligation {
		columns += ",status"
		placeholders += ",?"
		args = append(args, record.Status)
	}
	columns += ",amount_coefficient,amount_scale,amount_original,incomplete_reason,generated_by,model,prompt_version,schema_version,data_revision,supersedes_id,version,created_at,updated_at"
	placeholders += ",?,?,?,?,?,?,?,?,?,?,?,?,?"
	var coefficient, scale any
	if record.Amount != nil {
		coefficient, scale = record.Amount.Coefficient, int64(record.Amount.Scale)
	}
	stamp := record.CreatedAt.Format(time.RFC3339Nano)
	args = append(args, coefficient, scale, record.OriginalAmount, record.IncompleteReason, record.GeneratedBy, record.Model, record.PromptVersion, record.SchemaVersion, record.DataRevision, nullableString(record.SupersedesID), record.Version, stamp, stamp)
	if _, err := tx.ExecContext(ctx, "INSERT INTO "+table+"("+columns+") VALUES("+placeholders+")", args...); err != nil {
		return fmt.Errorf("insert %s finance record: %w", record.Kind, err)
	}
	return nil
}

func linkRecord(ctx context.Context, tx *sql.Tx, record Record) error {
	evidenceID, err := randomID()
	if err != nil {
		return errors.New("create finance evidence identifier")
	}
	stamp := record.CreatedAt.Format(time.RFC3339Nano)
	_, err = tx.ExecContext(ctx, `INSERT INTO evidence_links(id,record_family,record_id,household_id,owner_user_id,visibility,source_id,source_family,source_version,locator_kind,locator_value,created_at) VALUES(?,'finance',?,?,?,?,?,?,?,?,?,?)`, evidenceID, record.ID, record.HouseholdID, record.OwnerID, record.Visibility, record.SourceID, record.SourceFamily, record.SourceVersion, record.LocatorKind, record.LocatorValue, stamp)
	if err != nil {
		return fmt.Errorf("link finance evidence: %w", err)
	}
	content := strings.TrimSpace(record.Label + " " + record.Category)
	_, err = tx.ExecContext(ctx, `INSERT INTO search_entries(record_family,record_id,household_id,owner_user_id,visibility,content) VALUES('finance',?,?,?,?,?)`, record.ID, record.HouseholdID, record.OwnerID, record.Visibility, content)
	if err != nil {
		return fmt.Errorf("index finance record: %w", err)
	}
	return nil
}

type queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func authorizeActor(ctx context.Context, query queryer, actor policy.ActorScope) error {
	var allowed int
	err := query.QueryRowContext(ctx, `SELECT 1 FROM household_members m JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE m.household_id=? AND m.user_id=? AND u.status='active' AND h.status='active'`, actor.HouseholdID, actor.ActorID).Scan(&allowed)
	if err != nil || allowed != 1 {
		return policy.ErrUnauthorized
	}
	return nil
}

func findRecord(ctx context.Context, query queryer, actor policy.ActorScope, kind Kind, id string) (Record, error) {
	table := tableFor(kind)
	date := dateColumn(kind)
	if table == "" || strings.TrimSpace(id) == "" {
		return Record{}, ErrInvalidRecord
	}
	extra := "''"
	status := "''"
	if kind == Budget {
		extra = "ends_on"
	}
	if kind == Obligation {
		status = "status"
	}
	row := query.QueryRowContext(ctx, `SELECT id,? AS kind,household_id,owner_user_id,visibility,label,category,`+date+`,`+extra+`,`+status+`,amount_coefficient,amount_scale,amount_original,incomplete_reason,source_id,source_family,source_version,'','',generated_by,model,prompt_version,schema_version,data_revision,COALESCE(supersedes_id,''),version,created_at FROM `+table+` WHERE id=? AND active=1 AND household_id=? AND (visibility='shared' OR owner_user_id=?)`, string(kind), id, actor.HouseholdID, actor.ActorID)
	record, err := scanRecord(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Record{}, policy.ErrUnauthorized
	}
	return record, err
}

type scanner interface {
	Scan(...any) error
}

func scanRecord(row scanner) (Record, error) {
	var record Record
	var kind, visibility string
	var coefficient, scale sql.NullInt64
	var created string
	if err := row.Scan(&record.ID, &kind, &record.HouseholdID, &record.OwnerID, &visibility, &record.Label, &record.Category, &record.Date, &record.EndDate, &record.Status, &coefficient, &scale, &record.OriginalAmount, &record.IncompleteReason, &record.SourceID, &record.SourceFamily, &record.SourceVersion, &record.LocatorKind, &record.LocatorValue, &record.GeneratedBy, &record.Model, &record.PromptVersion, &record.SchemaVersion, &record.DataRevision, &record.SupersedesID, &record.Version, &created); err != nil {
		return Record{}, err
	}
	record.Kind, record.Visibility = Kind(kind), policy.Visibility(visibility)
	if coefficient.Valid && scale.Valid {
		amount := Decimal{Coefficient: coefficient.Int64, Scale: uint8(scale.Int64)}
		record.Amount = &amount
	}
	record.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	return record, nil
}

func unionQuery(actor policy.ActorScope, filter ScopeFilter) (string, []any) {
	parts := make([]string, 0, 6)
	args := make([]any, 0, 18)
	for _, kind := range []Kind{Income, Spending, Asset, Liability, Budget, Obligation} {
		extra, status := "''", "''"
		if kind == Budget {
			extra = "ends_on"
		}
		if kind == Obligation {
			status = "status"
		}
		where := "household_id=? AND active=1 AND (visibility='shared' OR owner_user_id=?)"
		kindArgs := []any{string(kind), actor.HouseholdID, actor.ActorID}
		if filter == SharedRecords {
			where += " AND visibility='shared'"
		} else if filter == PersonalRecords {
			where += " AND visibility='personal' AND owner_user_id=?"
			kindArgs = append(kindArgs, actor.ActorID)
		}
		parts = append(parts, `SELECT id,? AS kind,household_id,owner_user_id,visibility,label,category,`+dateColumn(kind)+`,`+extra+`,`+status+`,amount_coefficient,amount_scale,amount_original,incomplete_reason,source_id,source_family,source_version,COALESCE((SELECT locator_kind FROM evidence_links e WHERE e.record_family='finance' AND e.record_id=`+tableFor(kind)+`.id AND e.source_id=`+tableFor(kind)+`.source_id AND e.household_id=`+tableFor(kind)+`.household_id LIMIT 1),''),COALESCE((SELECT locator_value FROM evidence_links e WHERE e.record_family='finance' AND e.record_id=`+tableFor(kind)+`.id AND e.source_id=`+tableFor(kind)+`.source_id AND e.household_id=`+tableFor(kind)+`.household_id LIMIT 1),''),generated_by,model,prompt_version,schema_version,data_revision,COALESCE(supersedes_id,''),version,created_at FROM `+tableFor(kind)+` WHERE `+where)
		args = append(args, kindArgs...)
	}
	return strings.Join(parts, " UNION ALL ") + " ORDER BY created_at DESC,id", args
}

func summarize(records []Record, asOf time.Time) (Summary, error) {
	summary := Summary{Records: records, Totals: make(map[Kind]Decimal), Counts: make(map[Kind]int)}
	currentStart := time.Date(asOf.Year(), asOf.Month(), 1, 0, 0, 0, 0, time.UTC)
	previousStart := currentStart.AddDate(0, -1, 0)
	type pair struct {
		previous, current           []Decimal
		previousCount, currentCount int
	}
	categoryValues := make(map[string]pair)
	for _, record := range records {
		if record.IncompleteReason != "" {
			summary.Incomplete++
			summary.Issues = append(summary.Issues, Issue{RecordID: record.ID, Kind: record.Kind, Label: record.Label, Reason: record.IncompleteReason, SourceID: record.SourceID, LocatorKind: record.LocatorKind, LocatorValue: record.LocatorValue})
		} else {
			summary.Complete++
		}
		if record.Amount == nil {
			continue
		}
		total, err := Add(summary.Totals[record.Kind], *record.Amount)
		if err != nil {
			return Summary{}, err
		}
		summary.Totals[record.Kind], summary.Counts[record.Kind] = total, summary.Counts[record.Kind]+1
		date, dateErr := time.Parse("2006-01-02", record.Date)
		if record.Kind == Spending && dateErr == nil {
			values := categoryValues[record.Category]
			if !date.Before(currentStart) && date.Before(currentStart.AddDate(0, 1, 0)) {
				values.current = append(values.current, *record.Amount)
				values.currentCount++
			} else if !date.Before(previousStart) && date.Before(currentStart) {
				values.previous = append(values.previous, *record.Amount)
				values.previousCount++
			}
			categoryValues[record.Category] = values
		}
		if record.Kind == Obligation && record.Status == "pending" && dateErr == nil {
			if !date.Before(time.Date(asOf.Year(), asOf.Month(), asOf.Day(), 0, 0, 0, 0, time.UTC)) && date.Before(asOf.AddDate(0, 0, 31)) {
				summary.Upcoming = append(summary.Upcoming, record)
			}
		}
	}
	for category, values := range categoryValues {
		previous, err := Sum(values.previous)
		if err != nil {
			return Summary{}, err
		}
		current, err := Sum(values.current)
		if err != nil {
			return Summary{}, err
		}
		change, err := Add(current, Decimal{Coefficient: -previous.Coefficient, Scale: previous.Scale})
		if err != nil {
			return Summary{}, err
		}
		if values.previousCount+values.currentCount > 0 {
			summary.Trends = append(summary.Trends, CategoryTrend{Category: category, Previous: previous, Current: current, Change: change, PreviousCount: values.previousCount, CurrentCount: values.currentCount, PreviousPeriod: previousStart.Format("January 2006"), CurrentPeriod: currentStart.Format("January 2006")})
		}
	}
	sort.Slice(summary.Trends, func(i, j int) bool { return summary.Trends[i].Category < summary.Trends[j].Category })
	sort.Slice(summary.Upcoming, func(i, j int) bool {
		if summary.Upcoming[i].Date == summary.Upcoming[j].Date {
			return summary.Upcoming[i].ID < summary.Upcoming[j].ID
		}
		return summary.Upcoming[i].Date < summary.Upcoming[j].Date
	})
	return summary, nil
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}
