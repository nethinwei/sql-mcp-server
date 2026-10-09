package bootstrap

import (
	"fmt"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/relalg"
)

// configToEntities converts config entities to the core entity model, named
// by their IDs, with the entities they refer to resolved to IDs.
func configToEntities(ecs []config.EntityConfig) ([]entity.Entity, error) {
	refs := config.NewEntityRefs(ecs)
	out := make([]entity.Entity, 0, len(ecs))
	for _, ec := range ecs {
		e, err := configToEntity(ec, refs)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, nil
}

// entityID is the ID of the entity ref names; ref itself when it names none
// or several, which validation rejects.
func entityID(refs config.EntityRefs, ref string) string {
	if e, err := refs.Resolve(ref); err == nil {
		return e.ID()
	}
	return ref
}

func configToEntity(ec config.EntityConfig, refs config.EntityRefs) (entity.Entity, error) {
	source, dataSource := ec.PhysicalSource(), ec.DatasourceName()
	attrs := entityAttributesFromConfig(ec.Fields)
	role := entityRoleFromConfig(ec.Roles)
	fieldAccess := entityFieldAccessFromConfig(ec.FieldACL)
	rowPolicies, err := entityRowPoliciesFromConfig(ec.RowPolicies)
	if err != nil {
		return entity.Entity{}, err
	}
	tenantPolicy, err := filterConfigToPredicate(ec.TenantPolicy)
	if err != nil {
		return entity.Entity{}, fmt.Errorf("tenant policy for entity %q: %w", ec.Name, err)
	}
	keys := entityKeysFromConfig(ec.PrimaryKey, ec.UniqueKeys)
	relations := entityRelationsFromConfig(ec.Relationships, refs)
	var affects []string
	for _, ref := range ec.Affects {
		affects = append(affects, entityID(refs, ref))
	}
	return entity.Entity{
		Name: ec.ID(), Local: ec.Name, Source: source, DataSource: dataSource, Schema: ec.Schema, Description: ec.Description,
		Kind: parseKind(ec.Kind), Attributes: attrs, Keys: keys, Role: role, FieldAccess: fieldAccess,
		MCP: entity.MCPFlags{
			DMLTools: ec.MCP.DMLTools, CustomTool: ec.MCP.CustomTool,
			TrustedProcedure: ec.MCP.TrustedProcedure,
		},
		RowPolicies:  rowPolicies,
		AllowCascade: ec.AllowCascade,
		TenantPolicy: tenantPolicy,
		Relations:    relations,
		Params:       ec.Params,
		Affects:      affects,
	}, nil
}

func entityAttributesFromConfig(fields []config.FieldConfig) []entity.Attribute {
	attrs := make([]entity.Attribute, 0, len(fields))
	for _, f := range fields {
		attrs = append(attrs, entity.Attribute{
			Name: f.Name, Alias: f.Alias, Description: f.Description,
			Mask: f.Mask, Excluded: f.Exclude,
		})
	}
	return attrs
}

func entityRoleFromConfig(roles config.RoleConfig) entity.RoleAccess {
	return entity.RoleAccess{
		entity.ActionRead:      roles.Read,
		entity.ActionCreate:    roles.Create,
		entity.ActionUpdate:    roles.Update,
		entity.ActionDelete:    roles.Delete,
		entity.ActionExecute:   roles.Execute,
		entity.ActionAggregate: roles.Aggregate,
	}
}

func entityFieldAccessFromConfig(fieldACL map[string]config.FieldACLConfig) entity.FieldAccess {
	fieldAccess := make(entity.FieldAccess, len(fieldACL))
	for role, acl := range fieldACL {
		fieldAccess[role] = entity.FieldPermissions{Read: acl.Read, Write: acl.Write}
	}
	return fieldAccess
}

func entityRowPoliciesFromConfig(policies config.RowPolicies) (entity.RowPolicies, error) {
	rowPolicies := entity.RowPolicies{}
	for r, fc := range policies {
		p, err := filterConfigToPredicate(fc)
		if err != nil {
			return nil, fmt.Errorf("row policy for role %q: %w", r, err)
		}
		rowPolicies[r] = p
	}
	return rowPolicies, nil
}

func entityKeysFromConfig(primaryKey []string, uniqueKeys [][]string) []entity.Key {
	var keys []entity.Key
	if len(primaryKey) > 0 {
		keys = append(keys, entity.Key{Name: "pk", Columns: primaryKey, Primary: true})
	}
	for i, columns := range uniqueKeys {
		keys = append(keys, entity.Key{Name: fmt.Sprintf("uk%d", i+1), Columns: columns})
	}
	return keys
}

func entityRelationsFromConfig(relations []config.RelationshipConfig, refs config.EntityRefs) []entity.Relationship {
	out := make([]entity.Relationship, 0, len(relations))
	for _, relation := range relations {
		out = append(out, entity.Relationship{
			Name: relation.Name, Target: entityID(refs, relation.Target),
			Cardinality: relation.Cardinality, JoinOn: relation.JoinOn,
		})
	}
	return out
}

func parseKind(s string) entity.Kind {
	switch s {
	case "view":
		return entity.KindView
	case "procedure":
		return entity.KindProcedure
	}
	return entity.KindTable
}

// filterConfigToPredicate converts a declarative filter config to a relalg
// predicate. Supported shapes: {op,field,value}, {and:[...]}, {or:[...]}.
func filterConfigToPredicate(fc config.FilterConfig) (relalg.Predicate, error) {
	if fc == nil {
		return nil, nil
	}
	if op, ok := fc["op"].(string); ok {
		field, _ := fc["field"].(string)
		return relalg.Condition{Field: field, Op: relalg.Op(op), Value: fc["value"]}, nil
	}
	if and, ok := fc["and"].([]any); ok {
		return combineFilters(and, relalg.And{})
	}
	if or, ok := fc["or"].([]any); ok {
		return combineFilters(or, relalg.Or{})
	}
	return nil, fmt.Errorf("invalid filter config: missing op/and/or")
}

func combineFilters(items []any, wrap relalg.Predicate) (relalg.Predicate, error) {
	preds := make([]relalg.Predicate, 0, len(items))
	for _, item := range items {
		m, ok := item.(config.FilterConfig)
		if !ok {
			return nil, fmt.Errorf("filter item is not an object")
		}
		p, err := filterConfigToPredicate(m)
		if err != nil {
			return nil, err
		}
		preds = append(preds, p)
	}
	switch w := wrap.(type) {
	case relalg.And:
		w.Preds = preds
		return w, nil
	case relalg.Or:
		w.Preds = preds
		return w, nil
	}
	return nil, fmt.Errorf("unsupported combiner")
}
