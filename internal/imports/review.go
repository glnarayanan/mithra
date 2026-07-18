package imports

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/glnarayanan/mithra/internal/finance"
	"github.com/glnarayanan/mithra/internal/health"
	"github.com/glnarayanan/mithra/internal/planning"
	"github.com/glnarayanan/mithra/internal/policy"
	"github.com/glnarayanan/mithra/internal/storage"
)

var (
	ErrBlocked   = errors.New("import has unresolved blockers")
	ErrDuplicate = errors.New("document was already imported in this scope")
	ErrStale     = errors.New("import review is stale")
)

type ProposalSet struct {
	Records []ProposedRecord `json:"records"`
}

type ProposedRecord struct {
	Family      string            `json:"family"`
	Locator     Locator           `json:"locator"`
	Finance     *FinanceProposal  `json:"finance"`
	Health      *HealthProposal   `json:"health"`
	Planning    *PlanningProposal `json:"planning"`
	GeneratedBy string            `json:"generated_by,omitempty"`
}

type FinanceProposal struct {
	Kind     string `json:"kind"`
	Label    string `json:"label"`
	Category string `json:"category"`
	Date     string `json:"date"`
	EndDate  string `json:"end_date"`
	Status   string `json:"status"`
	Amount   string `json:"amount"`
}

type HealthProposal struct {
	Subject          string `json:"subject"`
	Analyte          string `json:"analyte"`
	Specimen         string `json:"specimen"`
	Method           string `json:"method"`
	ReferenceContext string `json:"reference_context"`
	ObservedOn       string `json:"observed_on"`
	Value            string `json:"value"`
	Unit             string `json:"unit"`
	ReferenceLow     string `json:"reference_low"`
	ReferenceHigh    string `json:"reference_high"`
	ReferenceUnit    string `json:"reference_unit"`
}

type PlanningProposal struct {
	Title       string `json:"title"`
	Description string `json:"description"`
	Location    string `json:"location"`
	AllDay      bool   `json:"all_day"`
	StartsOn    string `json:"starts_on"`
	EndsOn      string `json:"ends_on"`
	StartsAt    string `json:"starts_at"`
	EndsAt      string `json:"ends_at"`
	Timezone    string `json:"timezone"`
	Status      string `json:"status"`
}

type Issue struct {
	Record  int
	Field   string
	Message string
	Locator string
	Warning bool
}

type Review struct {
	ID, FileName, Kind, SourceID, Digest, State string
	Visibility                                  policy.Visibility
	Version                                     int64
	Proposals                                   ProposalSet
	Issues                                      []Issue
	CreatedAt                                   time.Time
	SupersedesImportID                          string
}

type Recent struct {
	ID, FileName, SourceID, State string
	Visibility                    policy.Visibility
	Records                       int
	CreatedAt                     time.Time
}

type VisualConsent struct {
	ID, FileName, SourceID, Token string
	Visibility                    policy.Visibility
	Version                       int64
	ExpiresAt                     time.Time
}

type Service struct {
	db       *sql.DB
	sources  *storage.Service
	finance  *finance.Service
	health   *health.Service
	planning *planning.Service
	journal  *DeletionJournal
	now      func() time.Time
}

func NewService(db *sql.DB, sources *storage.Service, financeRecords *finance.Service, healthRecords *health.Service, planningRecords *planning.Service, journal *DeletionJournal) *Service {
	return &Service{db: db, sources: sources, finance: financeRecords, health: healthRecords, planning: planningRecords, journal: journal, now: time.Now}
}

func (s *Service) Stage(ctx context.Context, actor policy.ActorScope, source storage.Source, fileName string, proposals ProposalSet, supersedesImportID string) (Review, error) {
	if s == nil || s.db == nil || !actor.Valid() || source.HouseholdID != actor.HouseholdID || source.OwnerID != actor.ActorID || source.State != "live" || source.Version < 1 || strings.TrimSpace(fileName) == "" || len(fileName) > 255 {
		return Review{}, ErrInvalidInput
	}
	visibility := policy.PersonalDefault(source.Visibility)
	if source.Family != "csv" && source.Family != "xlsx" && source.Family != "pdf" {
		return Review{}, ErrInvalidInput
	}
	for index := range proposals.Records {
		proposals.Records[index].GeneratedBy = "ai"
	}
	issues := Validate(proposals)
	encoded, err := json.Marshal(proposals)
	if err != nil || len(encoded) > 512<<10 {
		return Review{}, ErrInvalidInput
	}
	id, err := reviewID()
	if err != nil {
		return Review{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Review{}, err
	}
	defer tx.Rollback()
	shared, personal, err := revisions(ctx, tx, actor)
	if err != nil {
		return Review{}, policy.ErrUnauthorized
	}
	stamp := s.now().UTC()
	var predecessor any
	if strings.TrimSpace(supersedesImportID) != "" {
		var priorDigest string
		err = tx.QueryRowContext(ctx, `SELECT source_digest FROM document_imports WHERE id=? AND household_id=? AND owner_user_id=? AND visibility=? AND state='committed'`, supersedesImportID, actor.HouseholdID, actor.ActorID, visibility).Scan(&priorDigest)
		if err != nil || priorDigest == source.PlaintextDigest {
			return Review{}, ErrInvalidInput
		}
		predecessor = supersedesImportID
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO document_imports(id,household_id,owner_user_id,visibility,source_id,file_name,document_kind,source_digest,state,proposal_json,expected_shared_revision,expected_personal_revision,supersedes_import_id,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,'review',?,?,?,?,?,?)`, id, actor.HouseholdID, actor.ActorID, visibility, source.ID, strings.TrimSpace(fileName), source.Family, source.PlaintextDigest, string(encoded), shared, personal, predecessor, stamp.Format(time.RFC3339Nano), stamp.Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return Review{}, ErrDuplicate
		}
		return Review{}, err
	}
	if err := tx.Commit(); err != nil {
		return Review{}, err
	}
	return Review{ID: id, FileName: strings.TrimSpace(fileName), Kind: source.Family, SourceID: source.ID, Digest: source.PlaintextDigest, State: "review", Visibility: visibility, Version: 1, Proposals: proposals, Issues: issues, CreatedAt: stamp, SupersedesImportID: supersedesImportID}, nil
}

func (s *Service) ExactExists(ctx context.Context, actor policy.ActorScope, visibility policy.Visibility, digest string) bool {
	if s == nil || !actor.Valid() || (visibility != policy.Personal && visibility != policy.Shared) || len(digest) != 64 {
		return false
	}
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM document_imports WHERE household_id=? AND owner_user_id=? AND visibility=? AND source_digest=? AND state IN ('review','awaiting_visual_consent','committed','superseded')`, actor.HouseholdID, actor.ActorID, visibility, strings.ToLower(digest)).Scan(&one)
	return err == nil && one == 1
}

func (s *Service) StageVisualConsent(ctx context.Context, actor policy.ActorScope, source storage.Source, fileName string) (VisualConsent, error) {
	if s == nil || !actor.Valid() || source.HouseholdID != actor.HouseholdID || source.OwnerID != actor.ActorID || source.Family != "pdf" || source.State != "live" || strings.TrimSpace(fileName) == "" || len(fileName) > 255 {
		return VisualConsent{}, ErrInvalidInput
	}
	id, err := reviewID()
	if err != nil {
		return VisualConsent{}, err
	}
	token, tokenHash, err := consentToken()
	if err != nil {
		return VisualConsent{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return VisualConsent{}, err
	}
	defer tx.Rollback()
	shared, personal, err := revisions(ctx, tx, actor)
	if err != nil {
		return VisualConsent{}, policy.ErrUnauthorized
	}
	stamp := s.now().UTC()
	expires := stamp.Add(10 * time.Minute)
	_, err = tx.ExecContext(ctx, `INSERT INTO document_imports(id,household_id,owner_user_id,visibility,source_id,file_name,document_kind,source_digest,state,proposal_json,expected_shared_revision,expected_personal_revision,consent_token_hash,consent_expires_at,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,'awaiting_visual_consent','',?,?,?,?,?,?)`, id, actor.HouseholdID, actor.ActorID, source.Visibility, source.ID, strings.TrimSpace(fileName), source.Family, source.PlaintextDigest, shared, personal, tokenHash, expires.Format(time.RFC3339Nano), stamp.Format(time.RFC3339Nano), stamp.Format(time.RFC3339Nano))
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return VisualConsent{}, ErrDuplicate
		}
		return VisualConsent{}, err
	}
	if err := tx.Commit(); err != nil {
		return VisualConsent{}, err
	}
	return VisualConsent{ID: id, FileName: strings.TrimSpace(fileName), SourceID: source.ID, Token: token, Visibility: source.Visibility, Version: 1, ExpiresAt: expires}, nil
}

func (s *Service) GetVisualConsent(ctx context.Context, actor policy.ActorScope, id string) (VisualConsent, error) {
	var consent VisualConsent
	var visibility, expires string
	err := s.db.QueryRowContext(ctx, `SELECT i.id,i.file_name,i.source_id,i.visibility,i.version,i.consent_expires_at FROM document_imports i JOIN sources s ON s.id=i.source_id AND s.state='live' AND s.household_id=i.household_id AND s.owner_user_id=i.owner_user_id AND s.visibility=i.visibility AND s.plaintext_digest=i.source_digest JOIN household_members m ON m.household_id=i.household_id AND m.user_id=i.owner_user_id JOIN users u ON u.id=m.user_id AND u.status='active' JOIN households h ON h.id=m.household_id AND h.status='active' WHERE i.id=? AND i.household_id=? AND i.owner_user_id=? AND i.state='awaiting_visual_consent'`, id, actor.HouseholdID, actor.ActorID).Scan(&consent.ID, &consent.FileName, &consent.SourceID, &visibility, &consent.Version, &expires)
	if err != nil {
		return VisualConsent{}, policy.ErrUnauthorized
	}
	consent.Visibility = policy.Visibility(visibility)
	consent.ExpiresAt, _ = time.Parse(time.RFC3339Nano, expires)
	if !consent.ExpiresAt.After(s.now().UTC()) {
		return VisualConsent{}, ErrStale
	}
	return consent, nil
}

// ConsumeVisualConsent is the final authorization check immediately before the
// provider transfer. It binds the one-time token to actor, household, source,
// digest, visibility, version, membership, and expiry.
func (s *Service) ConsumeVisualConsent(ctx context.Context, actor policy.ActorScope, id, token string, expected int64) error {
	hash := sha256.Sum256([]byte(token))
	result, err := s.db.ExecContext(ctx, `UPDATE document_imports SET state='visual_processing',consent_token_hash=NULL,consent_expires_at=NULL,version=version+1,updated_at=? WHERE id=? AND household_id=? AND owner_user_id=? AND state='awaiting_visual_consent' AND version=? AND consent_token_hash=? AND consent_expires_at>? AND EXISTS (SELECT 1 FROM sources s WHERE s.id=document_imports.source_id AND s.state='live' AND s.household_id=document_imports.household_id AND s.owner_user_id=document_imports.owner_user_id AND s.visibility=document_imports.visibility AND s.plaintext_digest=document_imports.source_digest) AND EXISTS (SELECT 1 FROM household_members m JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE m.household_id=document_imports.household_id AND m.user_id=document_imports.owner_user_id AND u.status='active' AND h.status='active')`, s.now().UTC().Format(time.RFC3339Nano), id, actor.HouseholdID, actor.ActorID, expected, hex.EncodeToString(hash[:]), s.now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrStale
	}
	return nil
}

func (s *Service) FinishVisual(ctx context.Context, actor policy.ActorScope, id string, expected int64, proposals ProposalSet) (Review, error) {
	for index := range proposals.Records {
		proposals.Records[index].GeneratedBy = "ai"
	}
	encoded, err := json.Marshal(proposals)
	if err != nil || len(encoded) > 512<<10 {
		return Review{}, ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Review{}, err
	}
	defer tx.Rollback()
	var expectedShared, expectedPersonal int64
	err = tx.QueryRowContext(ctx, `SELECT expected_shared_revision,expected_personal_revision FROM document_imports i WHERE i.id=? AND i.household_id=? AND i.owner_user_id=? AND i.state='visual_processing' AND i.version=? AND EXISTS (SELECT 1 FROM sources s WHERE s.id=i.source_id AND s.state='live' AND s.household_id=i.household_id AND s.owner_user_id=i.owner_user_id AND s.visibility=i.visibility AND s.plaintext_digest=i.source_digest) AND EXISTS (SELECT 1 FROM household_members m JOIN users u ON u.id=m.user_id JOIN households h ON h.id=m.household_id WHERE m.household_id=i.household_id AND m.user_id=i.owner_user_id AND u.status='active' AND h.status='active')`, id, actor.HouseholdID, actor.ActorID, expected).Scan(&expectedShared, &expectedPersonal)
	if err != nil {
		return Review{}, ErrStale
	}
	shared, personal, err := revisions(ctx, tx, actor)
	if err != nil {
		return Review{}, policy.ErrUnauthorized
	}
	if shared != expectedShared || personal != expectedPersonal {
		return Review{}, ErrStale
	}
	result, err := tx.ExecContext(ctx, `UPDATE document_imports SET state='review',proposal_json=?,version=version+1,updated_at=? WHERE id=? AND household_id=? AND owner_user_id=? AND state='visual_processing' AND version=?`, string(encoded), s.now().UTC().Format(time.RFC3339Nano), id, actor.HouseholdID, actor.ActorID, expected)
	if err != nil {
		return Review{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Review{}, ErrStale
	}
	if err := tx.Commit(); err != nil {
		return Review{}, err
	}
	return s.Get(ctx, actor, id)
}

func (s *Service) Get(ctx context.Context, actor policy.ActorScope, id string) (Review, error) {
	if s == nil || !actor.Valid() || strings.TrimSpace(id) == "" {
		return Review{}, policy.ErrUnauthorized
	}
	var review Review
	var visibility, encoded, created string
	err := s.db.QueryRowContext(ctx, `SELECT id,file_name,document_kind,source_id,source_digest,state,visibility,version,proposal_json,created_at,COALESCE(supersedes_import_id,'') FROM document_imports WHERE id=? AND household_id=? AND owner_user_id=? AND state='review'`, id, actor.HouseholdID, actor.ActorID).Scan(&review.ID, &review.FileName, &review.Kind, &review.SourceID, &review.Digest, &review.State, &visibility, &review.Version, &encoded, &created, &review.SupersedesImportID)
	if err != nil || json.Unmarshal([]byte(encoded), &review.Proposals) != nil {
		return Review{}, policy.ErrUnauthorized
	}
	review.Visibility = policy.Visibility(visibility)
	review.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	review.Issues = Validate(review.Proposals)
	return review, nil
}

// CurrentReview returns the newest unfinished review so a refresh or a later
// sign-in never strands staged encrypted data outside the user's workflow.
func (s *Service) CurrentReview(ctx context.Context, actor policy.ActorScope) (Review, error) {
	if s == nil || !actor.Valid() {
		return Review{}, policy.ErrUnauthorized
	}
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT id FROM document_imports WHERE household_id=? AND owner_user_id=? AND state='review' ORDER BY created_at DESC,id DESC LIMIT 1`, actor.HouseholdID, actor.ActorID).Scan(&id)
	if err != nil {
		return Review{}, policy.ErrUnauthorized
	}
	return s.Get(ctx, actor, id)
}

func (s *Service) Correct(ctx context.Context, actor policy.ActorScope, id string, expected int64, proposals ProposalSet) (Review, error) {
	if expected < 1 {
		return Review{}, ErrInvalidInput
	}
	encoded, err := json.Marshal(proposals)
	if err != nil || len(encoded) > 512<<10 {
		return Review{}, ErrInvalidInput
	}
	result, err := s.db.ExecContext(ctx, `UPDATE document_imports SET proposal_json=?,version=version+1,updated_at=? WHERE id=? AND household_id=? AND owner_user_id=? AND state='review' AND version=?`, string(encoded), s.now().UTC().Format(time.RFC3339Nano), id, actor.HouseholdID, actor.ActorID, expected)
	if err != nil {
		return Review{}, err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return Review{}, ErrStale
	}
	return s.Get(ctx, actor, id)
}

func (s *Service) Commit(ctx context.Context, actor policy.ActorScope, id string, expected int64) error {
	if s == nil || s.db == nil || s.finance == nil || s.health == nil || s.planning == nil || !actor.Valid() || expected < 1 {
		return ErrInvalidInput
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var sourceID, family, digest, encoded, visibility, supersedesImportID string
	var sourceVersion, storedVersion, expectedShared, expectedPersonal int64
	err = tx.QueryRowContext(ctx, `SELECT i.source_id,i.document_kind,i.source_digest,i.proposal_json,i.visibility,i.version,i.expected_shared_revision,i.expected_personal_revision,s.source_version,COALESCE(i.supersedes_import_id,'') FROM document_imports i JOIN sources s ON s.id=i.source_id AND s.state='live' AND s.household_id=i.household_id AND s.owner_user_id=i.owner_user_id AND s.visibility=i.visibility AND s.plaintext_digest=i.source_digest WHERE i.id=? AND i.household_id=? AND i.owner_user_id=? AND i.state='review'`, id, actor.HouseholdID, actor.ActorID).Scan(&sourceID, &family, &digest, &encoded, &visibility, &storedVersion, &expectedShared, &expectedPersonal, &sourceVersion, &supersedesImportID)
	if err != nil || storedVersion != expected {
		return ErrStale
	}
	shared, personal, err := revisions(ctx, tx, actor)
	if err != nil || shared != expectedShared || personal != expectedPersonal {
		return ErrStale
	}
	var proposals ProposalSet
	if json.Unmarshal([]byte(encoded), &proposals) != nil || blockerCount(Validate(proposals)) != 0 {
		return ErrBlocked
	}
	ref := SourceRef{ID: sourceID, Family: family, Version: sourceVersion}
	if supersedesImportID != "" {
		var priorSource string
		if err := tx.QueryRowContext(ctx, `SELECT source_id FROM document_imports WHERE id=? AND household_id=? AND owner_user_id=? AND visibility=? AND state='committed'`, supersedesImportID, actor.HouseholdID, actor.ActorID, visibility).Scan(&priorSource); err != nil {
			return ErrStale
		}
		if err := deactivateSourceRecords(ctx, tx, priorSource, s.now().UTC()); err != nil {
			return err
		}
	}
	for _, proposal := range proposals.Records {
		switch proposal.Family {
		case "finance":
			draft, err := FinanceDraft(ref, proposal.Locator, proposal.Finance.draft(policy.Visibility(visibility)))
			if err != nil {
				return ErrBlocked
			}
			draft.Provenance.GeneratedBy = proposal.generatedBy()
			if _, err = s.finance.CreateInTx(ctx, tx, actor, draft); err != nil {
				return ErrBlocked
			}
		case "health":
			draft, err := ObservationDraft(ref, proposal.Locator, proposal.Health.draft(policy.Visibility(visibility)))
			if err != nil {
				return ErrBlocked
			}
			draft.Provenance.GeneratedBy = proposal.generatedBy()
			if _, err = s.health.CreateObservationInTx(ctx, tx, actor, draft); err != nil {
				return ErrBlocked
			}
		case "planning":
			draft, err := EventDraft(ref, proposal.Locator, proposal.Planning.draft(policy.Visibility(visibility)))
			if err != nil {
				return ErrBlocked
			}
			draft.Provenance.GeneratedBy = proposal.generatedBy()
			if _, err = s.planning.CreateEventInTx(ctx, tx, actor, draft); err != nil {
				return ErrBlocked
			}
		default:
			return ErrBlocked
		}
	}
	result, err := tx.ExecContext(ctx, `UPDATE document_imports SET state='committed',version=version+1,committed_at=?,updated_at=? WHERE id=? AND state='review' AND version=?`, s.now().UTC().Format(time.RFC3339Nano), s.now().UTC().Format(time.RFC3339Nano), id, expected)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrStale
	}
	if supersedesImportID != "" {
		result, err := tx.ExecContext(ctx, `UPDATE document_imports SET state='superseded',version=version+1,updated_at=? WHERE id=? AND state='committed'`, s.now().UTC().Format(time.RFC3339Nano), supersedesImportID)
		if err != nil {
			return err
		}
		changed, _ := result.RowsAffected()
		if changed != 1 {
			return ErrStale
		}
	}
	return tx.Commit()
}

func (s *Service) Replacement(ctx context.Context, actor policy.ActorScope, id string) (Recent, error) {
	var item Recent
	var visibility, encoded, created string
	err := s.db.QueryRowContext(ctx, `SELECT id,file_name,source_id,state,visibility,proposal_json,created_at FROM document_imports WHERE id=? AND household_id=? AND owner_user_id=? AND state='committed'`, id, actor.HouseholdID, actor.ActorID).Scan(&item.ID, &item.FileName, &item.SourceID, &item.State, &visibility, &encoded, &created)
	if err != nil {
		return Recent{}, policy.ErrUnauthorized
	}
	item.Visibility = policy.Visibility(visibility)
	item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
	var proposals ProposalSet
	if json.Unmarshal([]byte(encoded), &proposals) == nil {
		item.Records = len(proposals.Records)
	}
	return item, nil
}

func (s *Service) Discard(ctx context.Context, actor policy.ActorScope, id string) error {
	var sourceID, householdID, ownerID string
	err := s.db.QueryRowContext(ctx, `SELECT source_id,household_id,owner_user_id FROM document_imports WHERE id=? AND household_id=? AND owner_user_id=? AND state IN ('review','awaiting_visual_consent')`, id, actor.HouseholdID, actor.ActorID).Scan(&sourceID, &householdID, &ownerID)
	if err != nil {
		return policy.ErrUnauthorized
	}
	if err := s.sources.DeleteOwnedImport(ctx, householdID, ownerID, sourceID); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE document_imports SET state='discarded',version=version+1,proposal_json='',consent_token_hash=NULL,consent_expires_at=NULL,updated_at=? WHERE id=? AND household_id=? AND owner_user_id=? AND state IN ('review','awaiting_visual_consent')`, s.now().UTC().Format(time.RFC3339Nano), id, actor.HouseholdID, actor.ActorID)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrStale
	}
	return nil
}

// AbortVisual removes a visual PDF only after its one-time consent has been
// consumed and the provider attempt failed. Keeping this separate from
// Discard prevents a parallel user request from deleting the source between
// the final authorization check and the outbound transfer.
func (s *Service) AbortVisual(ctx context.Context, actor policy.ActorScope, id string, expected int64) error {
	var sourceID, householdID, ownerID string
	err := s.db.QueryRowContext(ctx, `SELECT source_id,household_id,owner_user_id FROM document_imports WHERE id=? AND household_id=? AND owner_user_id=? AND state='visual_processing' AND version=?`, id, actor.HouseholdID, actor.ActorID, expected).Scan(&sourceID, &householdID, &ownerID)
	if err != nil {
		return ErrStale
	}
	if err := s.sources.DeleteOwnedImport(ctx, householdID, ownerID, sourceID); err != nil && !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE document_imports SET state='discarded',version=version+1,proposal_json='',consent_token_hash=NULL,consent_expires_at=NULL,updated_at=? WHERE id=? AND household_id=? AND owner_user_id=? AND state='visual_processing' AND version=?`, s.now().UTC().Format(time.RFC3339Nano), id, actor.HouseholdID, actor.ActorID, expected)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrStale
	}
	return nil
}

func (s *Service) CleanupAbandonedVisual(ctx context.Context) error {
	cutoff := s.now().UTC()
	rows, err := s.db.QueryContext(ctx, `SELECT id,household_id,owner_user_id,source_id FROM document_imports WHERE (state='awaiting_visual_consent' AND consent_expires_at<=?) OR (state='visual_processing' AND updated_at<=?)`, cutoff.Format(time.RFC3339Nano), cutoff.Add(-3*time.Minute).Format(time.RFC3339Nano))
	if err != nil {
		return err
	}
	type item struct{ id, household, owner, source string }
	var items []item
	for rows.Next() {
		var entry item
		if err := rows.Scan(&entry.id, &entry.household, &entry.owner, &entry.source); err != nil {
			_ = rows.Close()
			return err
		}
		items = append(items, entry)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, entry := range items {
		if err := s.sources.DeleteOwnedImport(ctx, entry.household, entry.owner, entry.source); err != nil && !errors.Is(err, storage.ErrNotFound) {
			return err
		}
		if _, err := s.db.ExecContext(ctx, `UPDATE document_imports SET state='discarded',proposal_json='',consent_token_hash=NULL,consent_expires_at=NULL,version=version+1,updated_at=? WHERE id=? AND state IN ('awaiting_visual_consent','visual_processing')`, cutoff.Format(time.RFC3339Nano), entry.id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) ListRecent(ctx context.Context, actor policy.ActorScope, limit int) ([]Recent, error) {
	if limit < 1 || limit > 50 {
		limit = 10
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,file_name,source_id,state,visibility,proposal_json,created_at FROM document_imports WHERE household_id=? AND owner_user_id=? AND state IN ('committed','superseded') ORDER BY created_at DESC LIMIT ?`, actor.HouseholdID, actor.ActorID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Recent
	for rows.Next() {
		var item Recent
		var visibility, encoded, created string
		if err := rows.Scan(&item.ID, &item.FileName, &item.SourceID, &item.State, &visibility, &encoded, &created); err != nil {
			return nil, err
		}
		item.Visibility = policy.Visibility(visibility)
		item.CreatedAt, _ = time.Parse(time.RFC3339Nano, created)
		var proposals ProposalSet
		if json.Unmarshal([]byte(encoded), &proposals) == nil {
			item.Records = len(proposals.Records)
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func Validate(set ProposalSet) []Issue {
	var issues []Issue
	if len(set.Records) == 0 || len(set.Records) > 200 {
		return []Issue{{Record: 0, Field: "records", Message: "No usable records were found in this file."}}
	}
	seen := map[string]int{}
	semantic := map[string]int{}
	healthUnits := map[string]map[string]int{}
	for index, record := range set.Records {
		locator := strings.TrimSpace(record.Locator.Value)
		if record.Locator.Kind == "" || locator == "" {
			issues = append(issues, Issue{Record: index, Field: "locator", Message: "Choose the source row, cell, or page for this record."})
		}
		nonNil := 0
		if record.Finance != nil {
			nonNil++
		}
		if record.Health != nil {
			nonNil++
		}
		if record.Planning != nil {
			nonNil++
		}
		if nonNil != 1 {
			issues = append(issues, Issue{Record: index, Field: "family", Message: "Choose exactly one record type.", Locator: locator})
			continue
		}
		key := record.Family + "\x00" + locator
		if prior, ok := seen[key]; ok {
			issues = append(issues, Issue{Record: index, Field: "locator", Message: fmt.Sprintf("This looks like the same source location as record %d.", prior+1), Locator: locator, Warning: true})
		} else {
			seen[key] = index
		}
		switch record.Family {
		case "finance":
			issues = append(issues, record.Finance.issues(index, locator)...)
		case "health":
			issues = append(issues, record.Health.issues(index, locator)...)
		case "planning":
			issues = append(issues, record.Planning.issues(index, locator)...)
		default:
			issues = append(issues, Issue{Record: index, Field: "family", Message: "Choose finance, health, or planning.", Locator: locator})
		}
		if key := semanticKey(record); key != "" {
			if prior, ok := semantic[key]; ok {
				issues = append(issues, Issue{Record: index, Field: "locator", Message: fmt.Sprintf("This matches the facts proposed for record %d; check for a duplicate.", prior+1), Locator: locator, Warning: true})
			} else {
				semantic[key] = index
			}
		}
		if record.Family == "health" && record.Health != nil {
			series := strings.ToLower(strings.TrimSpace(record.Health.Subject)) + "\x00" + strings.ToLower(strings.TrimSpace(record.Health.Analyte))
			unit := strings.TrimSpace(record.Health.Unit)
			if series != "\x00" && unit != "" {
				if healthUnits[series] == nil {
					healthUnits[series] = map[string]int{}
				}
				healthUnits[series][unit] = index
			}
		}
	}
	for _, units := range healthUnits {
		if len(units) > 1 {
			for unit, index := range units {
				issues = append(issues, Issue{Record: index, Field: "unit", Message: "This measurement uses " + unit + " while another matching measurement uses a different unit. Mithra kept them separate.", Locator: set.Records[index].Locator.Value, Warning: true})
			}
		}
	}
	sort.SliceStable(issues, func(i, j int) bool {
		if issues[i].Warning != issues[j].Warning {
			return !issues[i].Warning
		}
		return issues[i].Record < issues[j].Record
	})
	return issues
}

func semanticKey(record ProposedRecord) string {
	switch record.Family {
	case "finance":
		if record.Finance != nil {
			return "finance\x00" + strings.ToLower(strings.TrimSpace(record.Finance.Kind)) + "\x00" + strings.ToLower(strings.TrimSpace(record.Finance.Label)) + "\x00" + record.Finance.Date + "\x00" + strings.TrimSpace(record.Finance.Amount)
		}
	case "health":
		if record.Health != nil {
			return "health\x00" + strings.ToLower(strings.TrimSpace(record.Health.Subject)) + "\x00" + strings.ToLower(strings.TrimSpace(record.Health.Analyte)) + "\x00" + record.Health.ObservedOn + "\x00" + strings.TrimSpace(record.Health.Value) + "\x00" + strings.TrimSpace(record.Health.Unit)
		}
	case "planning":
		if record.Planning != nil {
			return "planning\x00" + strings.ToLower(strings.TrimSpace(record.Planning.Title)) + "\x00" + record.Planning.StartsOn + "\x00" + record.Planning.StartsAt
		}
	}
	return ""
}

func (p ProposedRecord) generatedBy() string {
	if p.GeneratedBy == "user" {
		return "user"
	}
	return "ai"
}

func blockerCount(issues []Issue) int {
	count := 0
	for _, issue := range issues {
		if !issue.Warning {
			count++
		}
	}
	return count
}

func (p *FinanceProposal) issues(i int, locator string) []Issue {
	if p == nil {
		return []Issue{{Record: i, Field: "family", Message: "Finance details are missing.", Locator: locator}}
	}
	var out []Issue
	if strings.TrimSpace(p.Label) == "" {
		out = append(out, Issue{Record: i, Field: "label", Message: "Enter a finance label.", Locator: locator})
	}
	if len(strings.TrimSpace(p.Label)) > 256 {
		out = append(out, Issue{Record: i, Field: "label", Message: "Shorten the finance label to 256 characters.", Locator: locator})
	}
	if len(strings.TrimSpace(p.Category)) > 128 {
		out = append(out, Issue{Record: i, Field: "category", Message: "Shorten the category to 128 characters.", Locator: locator})
	}
	if _, err := finance.ParseAmount(p.Amount); err != nil {
		out = append(out, Issue{Record: i, Field: "amount", Message: "Enter the correct number.", Locator: locator})
	}
	if !isoDate(p.Date) {
		out = append(out, Issue{Record: i, Field: "date", Message: "Enter the date as YYYY-MM-DD.", Locator: locator})
	}
	if finance.Kind(p.Kind) == finance.Budget && !isoDate(p.EndDate) {
		out = append(out, Issue{Record: i, Field: "end_date", Message: "Enter the budget end date as YYYY-MM-DD.", Locator: locator})
	}
	if finance.Kind(p.Kind) == finance.Obligation && p.Status != "" && p.Status != "pending" && p.Status != "paid" && p.Status != "cancelled" {
		out = append(out, Issue{Record: i, Field: "status", Message: "Use pending, paid, or cancelled for this obligation.", Locator: locator})
	}
	switch finance.Kind(p.Kind) {
	case finance.Income, finance.Spending, finance.Asset, finance.Liability, finance.Budget, finance.Obligation:
	default:
		out = append(out, Issue{Record: i, Field: "kind", Message: "Choose a valid finance kind.", Locator: locator})
	}
	return out
}
func (p *HealthProposal) issues(i int, locator string) []Issue {
	if p == nil {
		return []Issue{{Record: i, Field: "family", Message: "Health details are missing.", Locator: locator}}
	}
	var out []Issue
	if strings.TrimSpace(p.Subject) == "" {
		out = append(out, Issue{Record: i, Field: "subject", Message: "Enter who this observation is about.", Locator: locator})
	}
	if len(strings.TrimSpace(p.Subject)) > 128 {
		out = append(out, Issue{Record: i, Field: "subject", Message: "Shorten the person label to 128 characters.", Locator: locator})
	}
	if strings.TrimSpace(p.Analyte) == "" {
		out = append(out, Issue{Record: i, Field: "analyte", Message: "Enter the reported measurement.", Locator: locator})
	}
	if len(strings.TrimSpace(p.Analyte)) > 128 {
		out = append(out, Issue{Record: i, Field: "analyte", Message: "Shorten the measurement label to 128 characters.", Locator: locator})
	}
	if !isoDate(p.ObservedOn) {
		out = append(out, Issue{Record: i, Field: "observed_on", Message: "Enter the observation date as YYYY-MM-DD.", Locator: locator})
	}
	if _, err := health.ParseValue(p.Value); err != nil {
		out = append(out, Issue{Record: i, Field: "value", Message: "Enter the correct reported number.", Locator: locator})
	}
	if strings.TrimSpace(p.Unit) == "" {
		out = append(out, Issue{Record: i, Field: "unit", Message: "Enter the unit exactly as reported.", Locator: locator})
	}
	if len(strings.TrimSpace(p.Unit)) > 64 {
		out = append(out, Issue{Record: i, Field: "unit", Message: "Shorten the reported unit to 64 characters.", Locator: locator})
	}
	if p.ReferenceLow != "" {
		if _, err := health.ParseValue(p.ReferenceLow); err != nil {
			out = append(out, Issue{Record: i, Field: "reference_low", Message: "Enter the reported reference low as a number.", Locator: locator})
		}
	}
	if p.ReferenceHigh != "" {
		if _, err := health.ParseValue(p.ReferenceHigh); err != nil {
			out = append(out, Issue{Record: i, Field: "reference_high", Message: "Enter the reported reference high as a number.", Locator: locator})
		}
	}
	return out
}
func (p *PlanningProposal) issues(i int, locator string) []Issue {
	if p == nil {
		return []Issue{{Record: i, Field: "family", Message: "Planning details are missing.", Locator: locator}}
	}
	var out []Issue
	if strings.TrimSpace(p.Title) == "" {
		out = append(out, Issue{Record: i, Field: "title", Message: "Enter an event title.", Locator: locator})
	}
	if len(strings.TrimSpace(p.Title)) > 256 {
		out = append(out, Issue{Record: i, Field: "title", Message: "Shorten the event title to 256 characters.", Locator: locator})
	}
	if p.AllDay {
		if !isoDate(p.StartsOn) {
			out = append(out, Issue{Record: i, Field: "starts_on", Message: "Enter the event date as YYYY-MM-DD.", Locator: locator})
		}
		if p.EndsOn != "" && (!isoDate(p.EndsOn) || p.EndsOn < p.StartsOn) {
			out = append(out, Issue{Record: i, Field: "ends_on", Message: "Enter an end date on or after the start date.", Locator: locator})
		}
	} else {
		if !localMinute(p.StartsAt) {
			out = append(out, Issue{Record: i, Field: "starts_at", Message: "Enter the start as YYYY-MM-DDTHH:MM.", Locator: locator})
		}
		if !localMinute(p.EndsAt) {
			out = append(out, Issue{Record: i, Field: "ends_at", Message: "Enter the end as YYYY-MM-DDTHH:MM.", Locator: locator})
		}
		if localMinute(p.StartsAt) && localMinute(p.EndsAt) && p.EndsAt <= p.StartsAt {
			out = append(out, Issue{Record: i, Field: "ends_at", Message: "Enter an end after the start.", Locator: locator})
		}
		if _, err := time.LoadLocation(p.Timezone); err != nil {
			out = append(out, Issue{Record: i, Field: "timezone", Message: "Enter a valid timezone such as Asia/Kolkata.", Locator: locator})
		}
	}
	if p.Status != "" && p.Status != "planned" && p.Status != "completed" && p.Status != "cancelled" {
		out = append(out, Issue{Record: i, Field: "status", Message: "Use planned, completed, or cancelled for this event.", Locator: locator})
	}
	return out
}

func (p *FinanceProposal) draft(v policy.Visibility) finance.Draft {
	return finance.Draft{Kind: finance.Kind(p.Kind), Visibility: v, Label: p.Label, Category: p.Category, Date: p.Date, EndDate: p.EndDate, Status: p.Status, AmountText: p.Amount}
}
func (p *HealthProposal) draft(v policy.Visibility) health.ObservationDraft {
	return health.ObservationDraft{Visibility: v, Subject: p.Subject, Analyte: p.Analyte, Specimen: p.Specimen, Method: p.Method, ReferenceContext: p.ReferenceContext, ObservedOn: p.ObservedOn, Value: p.Value, Unit: p.Unit, ReferenceLow: p.ReferenceLow, ReferenceHigh: p.ReferenceHigh, ReferenceUnit: p.ReferenceUnit}
}
func (p *PlanningProposal) draft(v policy.Visibility) planning.EventDraft {
	return planning.EventDraft{Visibility: v, Title: p.Title, Description: p.Description, Location: p.Location, AllDay: p.AllDay, StartsOn: p.StartsOn, EndsOn: p.EndsOn, StartsAt: p.StartsAt, EndsAt: p.EndsAt, Timezone: p.Timezone, Status: p.Status}
}

func revisions(ctx context.Context, q interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, actor policy.ActorScope) (int64, int64, error) {
	var shared, personal int64
	err := q.QueryRowContext(ctx, `SELECT hr.shared_revision,ur.personal_revision FROM household_revisions hr JOIN user_revisions ur ON ur.household_id=hr.household_id WHERE hr.household_id=? AND ur.user_id=?`, actor.HouseholdID, actor.ActorID).Scan(&shared, &personal)
	return shared, personal, err
}
func reviewID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}
func consentToken() (string, string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", "", err
	}
	token := hex.EncodeToString(raw[:])
	sum := sha256.Sum256([]byte(token))
	return token, hex.EncodeToString(sum[:]), nil
}
func isoDate(value string) bool {
	if len(value) != 10 {
		return false
	}
	_, err := time.Parse("2006-01-02", value)
	return err == nil
}
func localMinute(value string) bool {
	if len(value) != 16 {
		return false
	}
	_, err := time.Parse("2006-01-02T15:04", value)
	return err == nil
}
