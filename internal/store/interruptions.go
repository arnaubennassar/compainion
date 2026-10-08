package store

// Interruptions repository: Create (with question validation + suggestion
// insert), Get (expanded), List, Next (presentation order + batch), Answer
// (mode/author rules), Dismiss, Reopen. Lifecycle events are emitted in the
// same transaction as the state change.

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"

	"github.com/arnaubennassar/compainion/internal/domain"
	"github.com/arnaubennassar/compainion/internal/ids"
)

// rowQuerier is satisfied by both *sql.DB and *sql.Tx, letting one loader
// serve reads inside and outside transactions.
type rowQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

const interruptionCols = `id, workstream_id, raised_by_agent_id, plan_id, step_id, task_id,
	topic, kind, priority, digest, blocking, status, answered_at, expires_at, default_action, created_at, updated_at`

func scanInterruption(r interface{ Scan(...any) error }) (Interruption, error) {
	var it Interruption
	var ws, raised, plan, step, task, answered sql.NullString
	var blocking int
	if err := r.Scan(&it.ID, &ws, &raised, &plan, &step, &task,
		&it.Topic, &it.Kind, &it.Priority, &it.Digest, &blocking, &it.Status,
		&answered, &it.ExpiresAt, &it.DefaultAction, &it.CreatedAt, &it.UpdatedAt); err != nil {
		return Interruption{}, err
	}
	it.WorkstreamID, it.RaisedByAgentID, it.PlanID, it.StepID, it.TaskID = ws.String, raised.String, strPtr(plan), strPtr(step), strPtr(task)
	it.Blocking = blocking != 0
	if answered.Valid {
		s := answered.String
		it.AnsweredAt = &s
	}
	return it, nil
}

func strPtr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	s := n.String
	return &s
}

func scanQuestion(r interface{ Scan(...any) error }) (Question, error) {
	var q Question
	var grp sql.NullString
	if err := r.Scan(&q.ID, &q.Position, &q.Text, &q.AnswerType, &grp, &q.Status); err != nil {
		return Question{}, err
	}
	q.Group = strPtr(grp)
	return q, nil
}

func scanSuggestion(r interface{ Scan(...any) error }) (Suggestion, error) {
	var s Suggestion
	var rec int
	if err := r.Scan(&s.ID, &s.Label, &s.Rationale, &rec); err != nil {
		return Suggestion{}, err
	}
	s.Recommended = rec != 0
	return s, nil
}

func scanAnswer(r interface{ Scan(...any) error }) (Answer, error) {
	var a Answer
	var sugg, text sql.NullString
	var fin int
	if err := r.Scan(&a.ID, &a.QuestionID, &a.Author, &a.Mode, &sugg, &text, &fin, &a.CreatedAt); err != nil {
		return Answer{}, err
	}
	a.SuggestionID, a.Text = strPtr(sugg), strPtr(text)
	a.Final = fin != 0
	return a, nil
}

// loadInterruption returns the interruption expanded with questions,
// suggestions and answers, reading through q (DB or tx).
func loadInterruption(ctx context.Context, q rowQuerier, id string) (Interruption, error) {
	it, err := scanInterruption(q.QueryRowContext(ctx,
		`SELECT `+interruptionCols+` FROM interruptions WHERE id = ?`, id))
	if err == sql.ErrNoRows {
		return Interruption{}, domain.Errf(domain.NotFound, "interruption %s not found", id)
	}
	if err != nil {
		return Interruption{}, fmt.Errorf("store: get interruption: %w", err)
	}
	qrows, err := q.QueryContext(ctx,
		`SELECT id, position, text, answer_type, grp, status FROM questions
		 WHERE interruption_id = ? ORDER BY position`, id)
	if err != nil {
		return Interruption{}, fmt.Errorf("store: list questions: %w", err)
	}
	defer qrows.Close()
	for qrows.Next() {
		question, err := scanQuestion(qrows)
		if err != nil {
			return Interruption{}, err
		}
		it.Questions = append(it.Questions, question)
	}
	if err := qrows.Err(); err != nil {
		return Interruption{}, err
	}
	for i := range it.Questions {
		sugg, err := listSuggestions(ctx, q, it.Questions[i].ID)
		if err != nil {
			return Interruption{}, err
		}
		it.Questions[i].Suggestions = sugg
		ans, err := listAnswers(ctx, q, it.Questions[i].ID)
		if err != nil {
			return Interruption{}, err
		}
		it.Questions[i].Answers = ans
	}
	return it, nil
}

func listSuggestions(ctx context.Context, q rowQuerier, questionID string) ([]Suggestion, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, label, rationale, recommended FROM suggestions WHERE question_id = ? ORDER BY id`, questionID)
	if err != nil {
		return nil, fmt.Errorf("store: list suggestions: %w", err)
	}
	defer rows.Close()
	var out []Suggestion
	for rows.Next() {
		s, err := scanSuggestion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func listAnswers(ctx context.Context, q rowQuerier, questionID string) ([]Answer, error) {
	rows, err := q.QueryContext(ctx,
		`SELECT id, question_id, author, mode, suggestion_id, text, final, created_at
		 FROM answers WHERE question_id = ? ORDER BY id`, questionID)
	if err != nil {
		return nil, fmt.Errorf("store: list answers: %w", err)
	}
	defer rows.Close()
	var out []Answer
	for rows.Next() {
		a, err := scanAnswer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// CreateInterruption validates the questions (domain.ValidateQuestions) and
// inserts interruption + questions + suggestions + the interruption_created
// note event in one transaction. Status defaults to open.
func (d *DB) CreateInterruption(ctx context.Context, it Interruption) (Interruption, error) {
	if it.Topic == "" {
		return Interruption{}, domain.Errf(domain.Invalid, "topic is required")
	}
	if it.Kind == "" {
		return Interruption{}, domain.Errf(domain.Invalid, "kind is required")
	}
	if it.Priority == "" {
		it.Priority = "normal"
	}
	if it.Status == "" {
		it.Status = "open"
	}
	// Timeout policy: user-facing interruptions wait until answered. The
	// default_action metadata is fixed to escalate — expiry can only raise
	// urgency, never close or decide the interruption.
	if it.DefaultAction == "" {
		it.DefaultAction = domain.ExpiryEscalate
	}
	if it.DefaultAction != domain.ExpiryEscalate {
		return Interruption{}, domain.Errf(domain.Unprocessable,
			"default_action must be %q: expiry only escalates urgency, it never auto-closes a decision", domain.ExpiryEscalate)
	}
	if it.ExpiresAt != "" {
		if _, err := time.Parse(time.RFC3339, it.ExpiresAt); err != nil {
			return Interruption{}, domain.Errf(domain.Invalid, "expires_at must be an RFC3339 timestamp")
		}
		// UTC RFC3339 timestamps compare lexically, which is how the
		// expiry query matches lapsed rows.
	}
	// Validate through the domain rules; they auto-assign positions 0..n-1.
	dqs := make([]domain.Question, len(it.Questions))
	for i, q := range it.Questions {
		dqs[i] = domain.Question{Position: q.Position, Text: q.Text, AnswerType: q.AnswerType, Group: ptrStr(q.Group), Suggestions: make([]domain.Suggestion, len(q.Suggestions))}
		for j, s := range q.Suggestions {
			dqs[i].Suggestions[j] = domain.Suggestion{Label: s.Label, Rationale: s.Rationale, Recommended: s.Recommended}
		}
	}
	if err := domain.ValidateQuestions(dqs); err != nil {
		return Interruption{}, err
	}
	for i := range it.Questions {
		it.Questions[i].Position = dqs[i].Position
	}
	it.ID = ids.New()
	n := now()
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO interruptions (id, workstream_id, raised_by_agent_id, plan_id, step_id, task_id, topic, kind, priority, digest, blocking, status, answered_at, expires_at, default_action, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, ?)`,
			it.ID, nullStr(it.WorkstreamID), nullStr(it.RaisedByAgentID), nullStr(ptrStr(it.PlanID)), nullStr(ptrStr(it.StepID)), nullStr(ptrStr(it.TaskID)),
			it.Topic, it.Kind, it.Priority, it.Digest, boolInt(it.Blocking), it.Status, it.ExpiresAt, it.DefaultAction, n, n); err != nil {
			return fmt.Errorf("store: create interruption: %w", err)
		}
		for _, q := range it.Questions {
			q.ID = ids.New()
			if _, err := tx.Exec(`INSERT INTO questions (id, interruption_id, position, text, answer_type, grp, status, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, 'open', ?, ?)`,
				q.ID, it.ID, q.Position, q.Text, q.AnswerType, nullStr(ptrStr(q.Group)), n, n); err != nil {
				return fmt.Errorf("store: insert question: %w", err)
			}
			for _, s := range q.Suggestions {
				s.ID = ids.New()
				if _, err := tx.Exec(`INSERT INTO suggestions (id, question_id, label, rationale, recommended, created_at, updated_at)
					VALUES (?, ?, ?, ?, ?, ?, ?)`,
					s.ID, q.ID, s.Label, s.Rationale, boolInt(s.Recommended), n, n); err != nil {
					return fmt.Errorf("store: insert suggestion: %w", err)
				}
			}
		}
		payload := fmt.Sprintf(`{"kind":"interruption_created","interruption_id":%q}`, it.ID)
		if _, err := AppendEventTx(tx, Event{AgentID: it.RaisedByAgentID, PlanID: ptrStr(it.PlanID), StepID: ptrStr(it.StepID), TaskID: ptrStr(it.TaskID), Type: "note", Payload: payload}); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return Interruption{}, err
	}
	return loadInterruption(ctx, d, it.ID)
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// GetInterruption returns the interruption expanded with questions,
// suggestions and answers, or NotFound.
func (d *DB) GetInterruption(ctx context.Context, id string) (Interruption, error) {
	return loadInterruption(ctx, d, id)
}

// InterruptionFilter narrows ListInterruptions.
type InterruptionFilter struct {
	Status       string
	WorkstreamID string
	Topic        string
}

// ListInterruptions returns base rows (no questions) matching the filter.
func (d *DB) ListInterruptions(ctx context.Context, f InterruptionFilter) ([]Interruption, error) {
	q := `SELECT ` + interruptionCols + ` FROM interruptions WHERE 1=1`
	var args []any
	if f.Status != "" {
		q += ` AND status = ?`
		args = append(args, f.Status)
	}
	if f.WorkstreamID != "" {
		q += ` AND workstream_id = ?`
		args = append(args, f.WorkstreamID)
	}
	if f.Topic != "" {
		q += ` AND topic = ?`
		args = append(args, f.Topic)
	}
	q += ` ORDER BY id`
	rows, err := d.QueryContext(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("store: list interruptions: %w", err)
	}
	defer rows.Close()
	var out []Interruption
	for rows.Next() {
		it, err := scanInterruption(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// NextInterruption picks the next open interruption in presentation order —
// priority urgent<high<normal<low (domain.Less), ties blocking first, then
// oldest (ULID id) — marks it presented and returns it together with `batch`,
// the ids of other still-open interruptions sharing its topic. When nothing
// is open it returns the zero Interruption with a nil error.
func (d *DB) NextInterruption(ctx context.Context) (Interruption, error) {
	var picked Interruption
	var batch []string
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(ctx, `SELECT `+interruptionCols+` FROM interruptions WHERE status = 'open'`)
		if err != nil {
			return fmt.Errorf("store: next interruption: %w", err)
		}
		defer rows.Close()
		var open []Interruption
		for rows.Next() {
			it, err := scanInterruption(rows)
			if err != nil {
				return err
			}
			open = append(open, it)
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if len(open) == 0 {
			return nil
		}
		sort.Slice(open, func(i, j int) bool {
			return domain.Less(toDomainInterruption(open[i]), toDomainInterruption(open[j]))
		})
		picked = open[0]
		if _, err := tx.Exec(`UPDATE interruptions SET status = 'presented', updated_at = ? WHERE id = ?`, now(), picked.ID); err != nil {
			return fmt.Errorf("store: mark presented: %w", err)
		}
		brows, err := tx.QueryContext(ctx,
			`SELECT id FROM interruptions WHERE status = 'open' AND topic = ? AND id != ? ORDER BY id`, picked.Topic, picked.ID)
		if err != nil {
			return fmt.Errorf("store: batch query: %w", err)
		}
		defer brows.Close()
		for brows.Next() {
			var id string
			if err := brows.Scan(&id); err != nil {
				return err
			}
			batch = append(batch, id)
		}
		return brows.Err()
	})
	if err != nil {
		return Interruption{}, err
	}
	if picked.ID == "" {
		return Interruption{}, nil
	}
	out, err := loadInterruption(ctx, d, picked.ID)
	if err != nil {
		return Interruption{}, err
	}
	out.Batch = batch
	return out, nil
}

func toDomainInterruption(it Interruption) domain.Interruption {
	return domain.Interruption{Priority: it.Priority, Blocking: it.Blocking, CreatedAt: it.CreatedAt}
}

// AnswerInterruption records an answer to one question of an interruption and
// applies the author/mode rules:
//   - user: suggestion | suggestion_with_comment | free | needs_details
//     (suggestion* require a suggestion_id belonging to that question; free
//     requires text; needs_details allows empty text)
//   - companion: pushback | free, both requiring text
//
// final=true marks the question answered. When every question is answered or
// skipped the interruption becomes answered (answered_at set) and a note
// event {kind: interruption_answered, interruption_id} is emitted targeted at
// the raising agent, atomically in the same transaction.
func (d *DB) AnswerInterruption(ctx context.Context, interruptionID string, a Answer) (Interruption, error) {
	// author/mode validation before touching the DB.
	switch a.Author {
	case "user":
		switch a.Mode {
		case "suggestion", "suggestion_with_comment":
			if a.SuggestionID == nil || *a.SuggestionID == "" {
				return Interruption{}, domain.Errf(domain.Unprocessable, "mode %s requires suggestion_id", a.Mode)
			}
		case "free":
			if a.Text == nil || *a.Text == "" {
				return Interruption{}, domain.Errf(domain.Unprocessable, "mode free requires text")
			}
		case "needs_details":
			// text optional
		default:
			return Interruption{}, domain.Errf(domain.Unprocessable, "author user cannot use mode %q", a.Mode)
		}
	case "companion":
		switch a.Mode {
		case "pushback", "free":
			if a.Text == nil || *a.Text == "" {
				return Interruption{}, domain.Errf(domain.Unprocessable, "mode %s requires text", a.Mode)
			}
		default:
			return Interruption{}, domain.Errf(domain.Unprocessable, "author companion cannot use mode %q", a.Mode)
		}
	default:
		return Interruption{}, domain.Errf(domain.Unprocessable, "author must be user or companion")
	}

	var raisedBy string
	var closed bool
	err := d.WithTx(ctx, func(tx *sql.Tx) error {
		// the interruption must still be presentable
		var status string
		if err := tx.QueryRowContext(ctx, `SELECT status FROM interruptions WHERE id = ?`, interruptionID).Scan(&status); err != nil {
			if err == sql.ErrNoRows {
				return domain.Errf(domain.NotFound, "interruption %s not found", interruptionID)
			}
			return fmt.Errorf("store: get interruption: %w", err)
		}
		if status != "open" && status != "presented" {
			return domain.Errf(domain.Conflict, "interruption %s is %s and cannot be answered", interruptionID, status)
		}
		// question must exist and belong to this interruption
		var qid string
		if err := tx.QueryRowContext(ctx, `SELECT id FROM questions WHERE id = ? AND interruption_id = ?`, a.QuestionID, interruptionID).Scan(&qid); err != nil {
			if err == sql.ErrNoRows {
				return domain.Errf(domain.NotFound, "question %s not found in interruption %s", a.QuestionID, interruptionID)
			}
			return fmt.Errorf("store: find question: %w", err)
		}
		// suggestion must belong to the same question
		if a.SuggestionID != nil && *a.SuggestionID != "" {
			var sid string
			if err := tx.QueryRowContext(ctx, `SELECT id FROM suggestions WHERE id = ? AND question_id = ?`, *a.SuggestionID, a.QuestionID).Scan(&sid); err != nil {
				if err == sql.ErrNoRows {
					return domain.Errf(domain.Unprocessable, "suggestion %s does not belong to question %s", *a.SuggestionID, a.QuestionID)
				}
				return fmt.Errorf("store: find suggestion: %w", err)
			}
		}
		a.ID = ids.New()
		n := now()
		if _, err := tx.Exec(`INSERT INTO answers (id, question_id, author, mode, suggestion_id, text, final, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			a.ID, a.QuestionID, a.Author, a.Mode, nullStr(ptrStr(a.SuggestionID)), nullStr(ptrStr(a.Text)), boolInt(a.Final), n, n); err != nil {
			return fmt.Errorf("store: insert answer: %w", err)
		}
		if a.Final {
			if _, err := tx.Exec(`UPDATE questions SET status = 'answered', updated_at = ? WHERE id = ?`, n, a.QuestionID); err != nil {
				return fmt.Errorf("store: mark question answered: %w", err)
			}
		}
		// close the interruption when every question is answered or skipped
		var remaining int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM questions WHERE interruption_id = ? AND status NOT IN ('answered','skipped')`, interruptionID).Scan(&remaining); err != nil {
			return fmt.Errorf("store: count open questions: %w", err)
		}
		if remaining == 0 {
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(raised_by_agent_id, '') FROM interruptions WHERE id = ?`, interruptionID).Scan(&raisedBy); err != nil {
				if err == sql.ErrNoRows {
					return domain.Errf(domain.NotFound, "interruption %s not found", interruptionID)
				}
				return fmt.Errorf("store: get interruption: %w", err)
			}
			if _, err := tx.Exec(`UPDATE interruptions SET status = 'answered', answered_at = ?, updated_at = ? WHERE id = ?`, n, n, interruptionID); err != nil {
				return fmt.Errorf("store: mark interruption answered: %w", err)
			}
			payload := fmt.Sprintf(`{"kind":"interruption_answered","interruption_id":%q}`, interruptionID)
			if _, err := AppendEventTx(tx, Event{AgentID: raisedBy, Type: "note", Payload: payload}); err != nil {
				return err
			}
			closed = true
		}
		return nil
	})
	if err != nil {
		return Interruption{}, err
	}
	out, err := loadInterruption(ctx, d, interruptionID)
	if err != nil {
		return Interruption{}, err
	}
	if !closed {
		return out, nil
	}
	return out, nil
}

// DismissInterruption moves an open or presented interruption to dismissed;
// anything else is Conflict.
func (d *DB) DismissInterruption(ctx context.Context, id string) error {
	return transitionInterruption(ctx, d, id, []string{"open", "presented"}, "dismissed")
}

// ReopenInterruption moves a presented interruption back to open; anything
// else is Conflict.
func (d *DB) ReopenInterruption(ctx context.Context, id string) error {
	return transitionInterruption(ctx, d, id, []string{"presented"}, "open")
}

func transitionInterruption(ctx context.Context, d *DB, id string, from []string, to string) error {
	allowed := map[string]bool{}
	for _, s := range from {
		allowed[s] = true
	}
	var status string
	err := d.QueryRowContext(ctx, `SELECT status FROM interruptions WHERE id = ?`, id).Scan(&status)
	if err == sql.ErrNoRows {
		return domain.Errf(domain.NotFound, "interruption %s not found", id)
	}
	if err != nil {
		return fmt.Errorf("store: get interruption status: %w", err)
	}
	if !allowed[status] {
		return domain.Errf(domain.Conflict, "interruption %s in status %s cannot move to %s", id, status, to)
	}
	if _, err := d.ExecContext(ctx, `UPDATE interruptions SET status = ?, updated_at = ? WHERE id = ?`, to, now(), id); err != nil {
		return fmt.Errorf("store: transition interruption: %w", err)
	}
	return nil
}

// ExpireLapsedInterruptions enforces the timeout policy on every open or
// presented interruption whose expires_at has passed. Expiry can only
// escalate: priority is bumped to urgent and an event notifies the raising
// agent; the interruption is NEVER closed, answered, or decided on the
// user's behalf — it waits until he answers. expires_at is cleared after
// the one-time bump so the sweep does not re-escalate forever.
//
// It returns the ids it touched, in id order. now is an RFC3339 UTC instant
// compared lexically against the stored expires_at.
func (d *DB) ExpireLapsedInterruptions(ctx context.Context, nowRFC3339 string) ([]string, error) {
	rows, err := d.QueryContext(ctx,
		`SELECT id FROM interruptions
	 WHERE expires_at != '' AND expires_at <= ? AND status IN ('open','presented')`, nowRFC3339)
	if err != nil {
		return nil, fmt.Errorf("store: list lapsed interruptions: %w", err)
	}
	defer rows.Close()
	type lapsed struct {
		id string
	}
	var due []lapsed
	for rows.Next() {
		var l lapsed
		if err := rows.Scan(&l.id); err != nil {
			return nil, err
		}
		due = append(due, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	var touched []string
	for _, l := range due {
		// Escalate-urgency-only: bump priority once, notify, keep waiting.
		err := d.WithTx(ctx, func(tx *sql.Tx) error {
			if _, err := tx.Exec(`UPDATE interruptions SET priority = 'urgent', expires_at = '', updated_at = ? WHERE id = ? AND status IN ('open','presented')`, now(), l.id); err != nil {
				return err
			}
			var raisedBy string
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(raised_by_agent_id, '') FROM interruptions WHERE id = ?`, l.id).Scan(&raisedBy); err != nil {
				return err
			}
			payload := fmt.Sprintf(`{"kind":"interruption_escalated","interruption_id":%q}`, l.id)
			_, err := AppendEventTx(tx, Event{AgentID: raisedBy, Type: "note", Payload: payload})
			return err
		})
		if err != nil {
			return touched, fmt.Errorf("store: expire interruption %s: %w", l.id, err)
		}
		touched = append(touched, l.id)
	}
	return touched, nil
}

// DeleteInterruption removes an interruption and its questions, suggestions
// and answers in one transaction; NotFound if unknown.
func (d *DB) DeleteInterruption(ctx context.Context, id string) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM answers WHERE question_id IN (SELECT id FROM questions WHERE interruption_id = ?)`, id); err != nil {
			return fmt.Errorf("store: delete answers: %w", err)
		}
		if _, err := tx.ExecContext(ctx,
			`DELETE FROM suggestions WHERE question_id IN (SELECT id FROM questions WHERE interruption_id = ?)`, id); err != nil {
			return fmt.Errorf("store: delete suggestions: %w", err)
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM questions WHERE interruption_id = ?`, id)
		if err != nil {
			return fmt.Errorf("store: delete questions: %w", err)
		}
		res2, err := tx.ExecContext(ctx, `DELETE FROM interruptions WHERE id = ?`, id)
		if err != nil {
			return fmt.Errorf("store: delete interruption: %w", err)
		}
		n1, _ := res.RowsAffected()
		n2, _ := res2.RowsAffected()
		if n1 == 0 && n2 == 0 {
			return domain.Errf(domain.NotFound, "interruption %s not found", id)
		}
		return nil
	})
}
