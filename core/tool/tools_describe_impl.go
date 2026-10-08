package tool

import (
	"context"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
)

func describeFields(ctx context.Context, tc Context, e entity.Entity) ([]string, bool, error) {
	actions := entity.ActionsFor(e.Kind)
	allowedEntity := false
	fields := make(map[string]bool)
	for _, action := range actions {
		dec, err := authorize(ctx, tc, rbac.Request{
			Role: tc.Role, Subject: tc.Subject, Entity: e.Name, Action: action,
		})
		if err != nil {
			return nil, false, err
		}
		if !dec.Reachable() {
			continue
		}
		allowedEntity = true
		if err := collectDescribeFields(ctx, tc, e, action, fields); err != nil {
			return nil, false, err
		}
	}
	return e.OrderedNames(fields), allowedEntity, nil
}

func collectDescribeFields(
	ctx context.Context,
	tc Context,
	e entity.Entity,
	action entity.Action,
	fields map[string]bool,
) error {
	for _, attr := range e.Attributes {
		if attr.Excluded {
			continue
		}
		for _, request := range describeFieldRequests(tc, e.Name, action, attr.Name) {
			fieldDecision, err := authorize(ctx, tc, request)
			if err != nil {
				return err
			}
			if fieldDecision.Allowed {
				fields[attr.Name] = true
				break
			}
		}
	}
	return nil
}

func describeFieldRequests(tc Context, entityName string, action entity.Action, attrName string) []rbac.Request {
	request := rbac.Request{
		Role: tc.Role, Subject: tc.Subject, Entity: entityName, Action: action,
		Fields: []string{attrName},
	}
	switch action {
	case entity.ActionCreate:
		request.Fields = nil
		request.WriteFields = []string{attrName}
		return []rbac.Request{request}
	case entity.ActionUpdate:
		request.Fields = nil
		request.ReadFields = []string{attrName}
		return []rbac.Request{request, {
			Role: tc.Role, Subject: tc.Subject, Entity: entityName, Action: action,
			WriteFields: []string{attrName},
		}}
	case entity.ActionDelete:
		request.Fields = nil
		request.ReadFields = []string{attrName}
		return []rbac.Request{request}
	default:
		return []rbac.Request{request}
	}
}
