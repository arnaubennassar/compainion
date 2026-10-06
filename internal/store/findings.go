package store

// Findings repository: Upsert with fingerprint dedupe (domain.Fingerprint),
// Get, List (query/status filters), Resolve, MarkSurfaced.

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/arnaubennassar/compainion/internal/domain"
	"github.com/arnaubennassar/compainion/internal/ids"
)

const findingCols = `id, reported_by_agent_id, plan_id, step_id, category, severity, location,
	title, details, fingerprint, occurrences, evidence, status, resolution, resolution_ref,
	interruption_id, created_at, updated_at`

func scanFinding(r interface{ Scan(...any) error }) (Finding, error) {
	var f Finding
	var rep sql.NullString
	var plan, step, resolution, ref, inter sql.NullString
	var occ int
	if err := r.Scan(&f.ID, &rep, &plan, &step, &f.Category, &f.Severity, &f.Location,
		&f.Title, &f.Details, &f.Fingerprint, &occ, &f.Evidence, &f.Status,
		&resolution, &ref, &inter, &f.CreatedAt, &f.UpdatedAt); err != nil {
		return Finding{}, err
	}
	f.ReportedByAgentID = rep.String
	f.PlanID, f.StepID, f.Resolution, f.ResolutionRef, f.InterruptionID = strPtr(plan), strPtr(step), strPtr(resolution), strPtr(ref), strPtr(inter)
	f.Occurrences = occ
	return f, nil
}

// GetFinding returns one finding or NotFound.
func (d *DB) GetFinding(ctx context.Context, id string) (Finding, error) {
	f, err := scanFinding(d.QueryRowContext(ctx,
		`SELECT `+findingCols+` FROM findings WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Finding{}, domain.Errf(domain.NotFound, "finding %s not found", id)
	}
	return f, err
}

// FindingFilter narrows ListFindings. Query is a case-insensitive substring
// match on title, details and fingerprint.
type FindingFilter struct {
	Query  string
	Status string
}

// ListFindings returns findings matching the filter in creation order.
func (d *DB) ListFindings(ctx context.Context, f FindingFilter) ([]Finding, error) {
	q := `SELECT ` + findingCols + ` FROM findings WHERE 1=1`
	var args []any
	if f.Query != "" {
		pat := "%" + f.Query + "%"
		q += ` AND (title LIKE ? OR details LIKE ? OR fingerprint LIKE ?)`
		args = append(args, pat, pat, pat)
	}
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	q += ` ORDER BY id`
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list findings: %w", err)
	}
	defer rows.Close()
	var out []Finding
	for rows.Next() {
		f, err := scanFinding(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// UpsertFinding reports a finding. The fingerprint is computed with
// domain.Fingerprint(category, location, title) when not supplied. Dedupe
// semantics:
//   - a matching non-resolved finding (new/surfaced): occurrences+1, evidence
//     JSON arrays merged, same row returned with created=false;
//   - a matching resolved finding with resolution issue_opened|ignored:
//     occurrences bumped only, never re-surfaced, created=false;
//   - a resolved finding with resolution step_added is excluded from dedupe
//     (the work was done but the issue reappeared): a NEW finding is created
//     with created=true.
//
// created lets the API layer answer 201 (new) vs 200 (dedupe).
func (d *DB) UpsertFinding(ctx context.Context, f Finding) (Finding, bool, error) {
	if f.Category == "" {
		return Finding{}, false, domain.Errf(domain.Invalid, "category is required")
	}
	if f.Severity == "" {
		f.Severity = "low"
	}
	if f.Title == "" {
		return Finding{}, false, domain.Errf(domain.Invalid, "title is required")
	}
	if f.Fingerprint == "" {
		f.Fingerprint = domain.Fingerprint(f.Category, f.Location, f.Title)
	}
	if f.Evidence == "" {
		f.Evidence = "[]"
	}
	var out Finding
	var created bool
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		// 1. matching non-resolved finding -> bump and merge (resolved
		// issue_opened|ignored rows are matched below, never here; resolved
		// step_added rows fall through to create)
		var id string
		var occ int
		var ev string
		err := tx.QueryRowContext(ctx,
			`SELECT id, occurrences, evidence FROM findings WHERE fingerprint = ? AND status != 'resolved'`,
			f.Fingerprint).Scan(&id, &occ, &ev)
		switch {
		case err == nil:
			merged := mergeEvidence(ev, f.Evidence)
			if _, err := tx.Exec(`UPDATE findings SET occurrences = occurrences + 1, evidence = ?, updated_at = ? WHERE id = ?`,
				merged, now(), id); err != nil {
				return fmt.Errorf("store: bump finding: %w", err)
			}
			out, err = loadFinding(ctx, tx, id)
			return err
		case err != sql.ErrNoRows:
			return fmt.Errorf("store: find finding: %w", err)
		}
		// 2. resolved issue_opened|ignored -> bump only, never re-surface
		err = tx.QueryRowContext(ctx,
			`SELECT id, occurrences FROM findings
			 WHERE fingerprint = ? AND status = 'resolved' AND resolution IN ('issue_opened','ignored')`,
			f.Fingerprint).Scan(&id, &occ)
		switch {
		case err == nil:
			if _, err := tx.Exec(`UPDATE findings SET occurrences = occurrences + 1, updated_at = ? WHERE id = ?`,
				now(), id); err != nil {
				return fmt.Errorf("store: bump resolved finding: %w", err)
			}
			out, err = loadFinding(ctx, tx, id)
			return err
		case err != sql.ErrNoRows:
			return fmt.Errorf("store: find resolved finding: %w", err)
		}
		// 3. nothing matched -> create
		f.ID = ids.New()
		n := now()
		if _, err := tx.Exec(`INSERT INTO findings (id, reported_by_agent_id, plan_id, step_id, category, severity, location,
			title, details, fingerprint, occurrences, evidence, status, resolution, resolution_ref, interruption_id, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1, ?, 'new', NULL, NULL, NULL, ?, ?)`,
			f.ID, nullStr(f.ReportedByAgentID), nullStr(ptrStr(f.PlanID)), nullStr(ptrStr(f.StepID)),
			f.Category, f.Severity, f.Location, f.Title, f.Details, f.Fingerprint, f.Evidence, n, n); err != nil {
			return fmt.Errorf("store: insert finding: %w", err)
		}
		created = true
		out, err = loadFinding(ctx, tx, f.ID)
		return err
	})
	if err != nil {
		return Finding{}, false, err
	}
	return out, created, nil
}

func loadFinding(ctx context.Context, q rowQuerier, id string) (Finding, error) {
	f, err := scanFinding(q.QueryRowContext(ctx,
		`SELECT `+findingCols+` FROM findings WHERE id = ?`, id))
	if err != nil {
		return Finding{}, fmt.Errorf("store: reload finding: %w", err)
	}
	return f, nil
}

// mergeEvidence appends incoming JSON-array items to the existing array,
// skipping items already present (exact JSON match).
func mergeEvidence(existing, incoming string) string {
	var cur, add []any
	_ = json.Unmarshal([]byte(existing), &cur)
	_ = json.Unmarshal([]byte(incoming), &add)
	seen := map[string]bool{}
	for _, it := range cur {
		seen[itemKey(it)] = true
	}
	for _, it := range add {
		if !seen[itemKey(it)] {
			cur = append(cur, it)
			seen[itemKey(it)] = true
		}
	}
	if len(cur) == 0 {
		return "[]"
	}
	out, err := json.Marshal(cur)
	if err != nil {
		return existing
	}
	return string(out)
}

// itemKey is a comparable key for one evidence item (strings directly, any
// other JSON value via re-marshalled canonical text).
func itemKey(v any) string {
	if s, ok := v.(string); ok {
		return "s:" + s
	}
	b, _ := json.Marshal(v)
	return string(b)
}

// ResolveFinding marks a finding resolved. resolution must be issue_opened
// (resolution_ref required), step_added (resolution_ref must be an existing
// step id) or ignored (ref optional). Resolving an already-resolved finding
// is a Conflict.
func (d *DB) ResolveFinding(ctx context.Context, id, resolution, resolutionRef string) (Finding, error) {
	f, err := d.GetFinding(ctx, id)
	if err != nil {
		return Finding{}, err
	}
	switch resolution {
	case "":
		return Finding{}, domain.Errf(domain.Invalid, "resolution is required")
	case "issue_opened", "step_added", "ignored":
		// ok
	default:
		return Finding{}, domain.Errf(domain.Invalid, "unknown resolution %q", resolution)
	}
	if f.Status == "resolved" {
		return Finding{}, domain.Errf(domain.Conflict, "finding %s is already resolved", id)
	}
	switch resolution {
	case "issue_opened", "step_added":
		if resolutionRef == "" {
			return Finding{}, domain.Errf(domain.Unprocessable, "resolution %s requires resolution_ref", resolution)
		}
	}
	if resolution == "step_added" {
		var sid string
		if err := d.QueryRowContext(ctx, `SELECT id FROM steps WHERE id = ?`, resolutionRef).Scan(&sid); err != nil {
			if err == sql.ErrNoRows {
				return Finding{}, domain.Errf(domain.NotFound, "step %s not found", resolutionRef)
			}
			return Finding{}, fmt.Errorf("store: check step: %w", err)
		}
	}
	n := now()
	if _, err := d.ExecContext(ctx, `UPDATE findings SET status = 'resolved', resolution = ?, resolution_ref = ?, updated_at = ? WHERE id = ?`,
		resolution, nullStr(resolutionRef), n, id); err != nil {
		return Finding{}, fmt.Errorf("store: resolve finding: %w", err)
	}
	return loadFinding(ctx, d, id)
}

// MarkFindingSurfaced moves a new (or already surfaced) finding to surfaced
// and links the interruption it was presented in.
func (d *DB) MarkFindingSurfaced(ctx context.Context, id, interruptionID string) (Finding, error) {
	f, err := d.GetFinding(ctx, id)
	if err != nil {
		return Finding{}, err
	}
	if f.Status == "resolved" {
		return Finding{}, domain.Errf(domain.Conflict, "finding %s is resolved and cannot be surfaced", id)
	}
	var iid string
	if err := d.QueryRowContext(ctx, `SELECT id FROM interruptions WHERE id = ?`, interruptionID).Scan(&iid); err != nil {
		if err == sql.ErrNoRows {
			return Finding{}, domain.Errf(domain.NotFound, "interruption %s not found", interruptionID)
		}
		return Finding{}, fmt.Errorf("store: check interruption: %w", err)
	}
	if _, err := d.ExecContext(ctx, `UPDATE findings SET status = 'surfaced', interruption_id = ?, updated_at = ? WHERE id = ?`,
		nullStr(interruptionID), now(), id); err != nil {
		return Finding{}, fmt.Errorf("store: surface finding: %w", err)
	}
	return loadFinding(ctx, d, id)
}
