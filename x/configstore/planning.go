package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/nethinwei/sql-mcp-server/internal/kit"
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
	out, err := queryAll(ctx, s, s.db, scanTemplate, query, args...)
	if err != nil {
		return nil, fmt.Errorf("configstore: list plan templates: %w", err)
	}
	return kit.OrEmpty(out), nil
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
	err := s.locked(ctx, func(tx *sql.Tx) error {
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
		return s.writeTemplate(ctx, tx, t, exists)
	})
	if err != nil {
		return PlanTemplate{}, fmt.Errorf("configstore: save plan template: %w", err)
	}
	return t, nil
}

// writeTemplate writes version t.Version of a template: the row in place of
// the version before it (still there: a template deleted meanwhile is not
// brought back), or a new one.
func (s *SQLStore) writeTemplate(ctx context.Context, tx *sql.Tx, t PlanTemplate, exists bool) error {
	args := []any{t.Name, string(t.Plan), boolInt(t.Recurring), t.Version, t.Author, toMicros(t.UpdatedAt)}
	if exists {
		r, err := s.exec(ctx, tx, "UPDATE smcp_plan_templates SET payload = ?, recurring = ?, version = ?, author = ?, "+
			"updated_us = ? WHERE name = ? AND version = ?", string(t.Plan), boolInt(t.Recurring), t.Version, t.Author,
			toMicros(t.UpdatedAt), t.Name, t.Version-1)
		if err != nil {
			return err
		}
		if n, err := r.RowsAffected(); err == nil && n != 1 {
			return fmt.Errorf("%w: %q changed meanwhile", ErrTemplateConflict, t.Name)
		}
	} else if _, err := s.exec(ctx, tx, "INSERT INTO smcp_plan_templates ("+templateColumns+
		") VALUES (?, ?, ?, ?, ?, ?)", args...); err != nil {
		return err
	}
	_, err := s.exec(ctx, tx, "INSERT INTO smcp_plan_template_versions ("+templateColumns+
		") VALUES (?, ?, ?, ?, ?, ?)", args...)
	return err
}

// DeletePlanTemplate removes a template and its versions, under the store
// lock as saving one is.
func (s *SQLStore) DeletePlanTemplate(ctx context.Context, name string) error {
	return s.locked(ctx, func(tx *sql.Tx) error {
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
