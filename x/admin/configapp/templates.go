package graph

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

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

func (r *Resolver) planTemplates(ctx context.Context) ([]PlanTemplate, error) {
	if _, err := auth.Require(ctx, auth.PermRead); err != nil {
		return nil, err
	}
	store, err := r.plans()
	if err != nil {
		return nil, err
	}
	all, err := store.PlanTemplates(ctx)
	if err != nil {
		return nil, err
	}
	return toPlanTemplates(all)
}

func (r *Resolver) planTemplateVersions(ctx context.Context, name string) ([]PlanTemplate, error) {
	if _, err := auth.Require(ctx, auth.PermRead); err != nil {
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

func (r *Resolver) savePlanTemplate(ctx context.Context, in PlanTemplateInput) (*PlanTemplate, error) {
	who, err := auth.Require(ctx, auth.PermWrite)
	if err != nil {
		return nil, err
	}
	store, err := r.plans()
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(in.Name)
	if name == "" || utf8.RuneCountInString(name) > 64 {
		return nil, errors.New("a template name has 1 to 64 characters")
	}
	p, err := decodePlan(in.Plan)
	if err != nil {
		return nil, err
	}
	data, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	saved, err := store.PutPlanTemplate(ctx, configstore.PlanTemplate{
		Name: name, Plan: data, Recurring: in.Recurring, Author: who.Username,
	}, derefInt(in.ExpectedVersion, 0))
	if err != nil {
		return nil, apiError(err)
	}
	out, err := toPlanTemplate(saved)
	return &out, err
}

func (r *Resolver) deletePlanTemplate(ctx context.Context, name string) (bool, error) {
	if _, err := auth.Require(ctx, auth.PermWrite); err != nil {
		return false, err
	}
	store, err := r.plans()
	if err != nil {
		return false, err
	}
	return true, store.DeletePlanTemplate(ctx, name)
}
