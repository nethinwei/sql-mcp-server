package configapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nethinwei/sql-mcp-server/internal/kit"
	"github.com/nethinwei/sql-mcp-server/x/admin/auth"
	"github.com/nethinwei/sql-mcp-server/x/configstore"
)

func toPlanTemplate(t configstore.PlanTemplate) (PlanTemplate, error) {
	var plan any
	if err := json.Unmarshal(t.Plan, &plan); err != nil {
		return PlanTemplate{}, fmt.Errorf("plan template %q: %w", t.Name, err)
	}
	return PlanTemplate{
		Name: t.Name, Plan: plan, Recurring: t.Recurring, Version: t.Version, Author: t.Author, UpdatedAt: t.UpdatedAt,
	}, nil
}

func toPlanTemplates(all []configstore.PlanTemplate) ([]PlanTemplate, error) {
	out := make([]PlanTemplate, 0, len(all))
	for _, t := range all {
		tpl, err := toPlanTemplate(t)
		if err != nil {
			return nil, err
		}
		out = append(out, tpl)
	}
	return out, nil
}

// PlanTemplates lists the plan templates; a principal limited to some
// datasources sees those whose scope names only them.
func (r *Service) PlanTemplates(ctx context.Context) ([]PlanTemplate, error) {
	who, err := auth.Require(ctx, auth.PermRead)
	if err != nil {
		return nil, err
	}
	return r.templates(ctx, who)
}

func (r *Service) templates(ctx context.Context, who auth.Principal) ([]PlanTemplate, error) {
	store, err := r.plans()
	if err != nil {
		return nil, err
	}
	all, err := store.PlanTemplates(ctx)
	if err != nil {
		return nil, err
	}
	out, err := toPlanTemplates(all)
	if err != nil || !who.Limited() {
		return out, err
	}
	return slices.DeleteFunc(out, func(t PlanTemplate) bool {
		p, err := DecodePlan(t.Plan)
		return err != nil || len(p.Scope.Datasources) == 0 ||
			slices.ContainsFunc(p.Scope.Datasources, func(ds string) bool { return !who.Allows(ds) })
	}), nil
}

// PlanTemplateVersions lists the versions of a template, newest first.
func (r *Service) PlanTemplateVersions(ctx context.Context, name string) ([]PlanTemplate, error) {
	if _, err := auth.RequireUnlimited(ctx, auth.PermRead); err != nil {
		return nil, err
	}
	store, err := r.plans()
	if err != nil {
		return nil, err
	}
	all, err := store.PlanTemplateVersions(ctx, name)
	if err != nil {
		return nil, err
	}
	return toPlanTemplates(all)
}

// SavePlanTemplate creates a template, or saves a new version of one
// while it is still at in.ExpectedVersion.
func (r *Service) SavePlanTemplate(ctx context.Context, in PlanTemplateInput) (*PlanTemplate, error) {
	who, store, err := r.planStore(ctx, auth.PermWrite)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > 64 {
		return nil, errors.New("a template name has 1 to 64 characters")
	}
	p, err := DecodePlan(in.Plan)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	saved, err := store.PutPlanTemplate(ctx, configstore.PlanTemplate{
		Name: name, Plan: data, Recurring: in.Recurring, Author: who.Username,
	}, kit.DerefInt(in.ExpectedVersion, 0))
	if err != nil {
		return nil, err
	}
	out, err := toPlanTemplate(saved)
	return &out, err
}

// DeletePlanTemplate deletes a template with its versions.
func (r *Service) DeletePlanTemplate(ctx context.Context, name string) (bool, error) {
	if _, err := auth.RequireUnlimited(ctx, auth.PermWrite); err != nil {
		return false, err
	}
	store, err := r.plans()
	if err != nil {
		return false, err
	}
	return true, store.DeletePlanTemplate(ctx, name)
}
