package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// PlanTemplate is a saved plan: a scope and rules the console and the
// configuration MCP endpoint evaluate again on demand. Recurring marks one
// whose new matches are proposed for review after each scan. Every save is a
// new version; earlier ones are kept.
type PlanTemplate struct {
	Name      string
	Plan      []byte
	Recurring bool
	Version   int
	Author    string
	UpdatedAt time.Time
}

// Plan template errors.
var (
	ErrTemplateNotFound = errors.New("configstore: plan template not found")
	ErrTemplateExists   = errors.New("configstore: a plan template with this name exists")
	// ErrTemplateConflict: the template changed since the version the caller
	// edited.
	ErrTemplateConflict = errors.New("configstore: plan template changed since it was read")
)

const templateColumns = "name, payload, recurring, version, author, updated_us"

func scanTemplate(row scanner) (PlanTemplate, error) {
	var t PlanTemplate
	var payload string
	var recurring int
	var updated int64
	if err := row.Scan(&t.Name, &payload, &recurring, &t.Version, &t.Author, &updated); err != nil {
		return PlanTemplate{}, err
	}
	t.Plan, t.Recurring, t.UpdatedAt = []byte(payload), recurring != 0, fromMicros(updated)
	return t, nil
}

func (s *SQLStore) templates(ctx context.Context, query string, args ...any) ([]PlanTemplate, error) {
	rows, err := s.db.QueryContext(ctx, s.dialect.rebind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("configstore: list plan templates: %w", err)
	}
	defer func() { _ = rows.Close() }()
	out := []PlanTemplate{}
	for rows.Next() {
		t, err := scanTemplate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// PlanTemplates returns the current version of every template, by name.
func (s *SQLStore) PlanTemplates(ctx context.Context) ([]PlanTemplate, error) {
	return s.templates(ctx, "SELECT "+templateColumns+" FROM smcp_plan_templates ORDER BY name")
}

// PlanTemplateVersions returns every version of a template, newest first.
func (s *SQLStore) PlanTemplateVersions(ctx context.Context, name string) ([]PlanTemplate, error) {
	return s.templates(ctx, "SELECT "+templateColumns+
		" FROM smcp_plan_template_versions WHERE name = ? ORDER BY version DESC", name)
}

// PutPlanTemplate saves a new version of a template. expected is the version
// the caller edited: 0 creates the template and fails with ErrTemplateExists
// when the name is taken; otherwise the current version must still be
// expected, or it fails with ErrTemplateConflict.
func (s *SQLStore) PutPlanTemplate(ctx context.Context, t PlanTemplate, expected int) (PlanTemplate, error) {
	t.UpdatedAt = s.timestamp()
	err := s.withTx(ctx, func(tx *sql.Tx) error {
		if err := s.lock(ctx, tx); err != nil {
			return err
		}
		current, err := scanTemplate(s.queryRow(ctx, tx,
			"SELECT "+templateColumns+" FROM smcp_plan_templates WHERE name = ?", t.Name))
		exists := err == nil
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		switch {
		case expected == 0 && exists:
			return fmt.Errorf("%w: %q", ErrTemplateExists, t.Name)
		case expected != 0 && (!exists || current.Version != expected):
			return fmt.Errorf("%w: %q is at version %d, not %d", ErrTemplateConflict, t.Name, current.Version, expected)
		}
		t.Version = current.Version + 1
		return s.writeTemplate(ctx, tx, t)
	})
	if err != nil {
		return PlanTemplate{}, fmt.Errorf("configstore: save plan template: %w", err)
	}
	return t, nil
}

func (s *SQLStore) writeTemplate(ctx context.Context, tx *sql.Tx, t PlanTemplate) error {
	if _, err := s.exec(ctx, tx, "DELETE FROM smcp_plan_templates WHERE name = ?", t.Name); err != nil {
		return err
	}
	args := []any{t.Name, string(t.Plan), boolInt(t.Recurring), t.Version, t.Author, toMicros(t.UpdatedAt)}
	if _, err := s.exec(ctx, tx, "INSERT INTO smcp_plan_templates ("+templateColumns+
		") VALUES (?, ?, ?, ?, ?, ?)", args...); err != nil {
		return err
	}
	_, err := s.exec(ctx, tx, "INSERT INTO smcp_plan_template_versions ("+templateColumns+
		") VALUES (?, ?, ?, ?, ?, ?)", args...)
	return err
}

// DeletePlanTemplate removes a template and its versions.
func (s *SQLStore) DeletePlanTemplate(ctx context.Context, name string) error {
	return s.withTx(ctx, func(tx *sql.Tx) error {
		res, err := s.exec(ctx, tx, "DELETE FROM smcp_plan_templates WHERE name = ?", name)
		if err != nil {
			return fmt.Errorf("configstore: delete plan template: %w", err)
		}
		if n, err := res.RowsAffected(); err == nil && n == 0 {
			return fmt.Errorf("%w: %q", ErrTemplateNotFound, name)
		}
		_, err = s.exec(ctx, tx, "DELETE FROM smcp_plan_template_versions WHERE name = ?", name)
		return err
	})
}
