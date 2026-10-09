package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/nethinwei/sql-mcp-server/core/entity"
	"github.com/nethinwei/sql-mcp-server/core/rbac"
)

// AmbiguousEntityError reports an entity reference naming more than one
// entity the caller can use. Candidates name each of them unambiguously;
// entities the caller cannot use are neither counted nor listed.
type AmbiguousEntityError struct {
	Ref        string
	Candidates []string
}

func (e *AmbiguousEntityError) Error() string {
	return fmt.Sprintf("%s: %q may name %s", ErrInvalidInput, e.Ref, strings.Join(e.Candidates, ", "))
}

func (e *AmbiguousEntityError) Unwrap() error { return ErrInvalidInput }

// entitySelector marks the tools whose "entity" input names the entity they
// act on. Other tools' inputs are their own: a procedure tool's parameter
// named entity is a value.
type entitySelector interface{ selectsEntity() }

func (ReadTool) selectsEntity()      {}
func (AggregateTool) selectsEntity() {}
func (CreateTool) selectsEntity()    {}
func (UpdateTool) selectsEntity()    {}
func (DeleteTool) selectsEntity()    {}
func (ExecuteTool) selectsEntity()   {}
func (DescribeTool) selectsEntity()  {}

// canonicalEntity rewrites the entity a tool call selects to the Name of the one
// entity the caller can use that it refers to, so tools and audit see the
// canonical identity. A reference matching no entity is left for the tool to
// reject.
func canonicalEntity(ctx context.Context, t Tool, input json.RawMessage, tc Context) (json.RawMessage, error) {
	var envelope struct {
		Entity string `json:"entity"`
	}
	if _, ok := t.(entitySelector); !ok || tc.Registry == nil {
		return input, nil
	}
	if decodeEnvelope(input, &envelope) != nil || envelope.Entity == "" {
		return input, nil
	}
	name, err := resolveEntityRef(ctx, tc, envelope.Entity)
	if err != nil || name == envelope.Entity {
		return input, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil {
		return input, nil
	}
	fields["entity"], _ = json.Marshal(name)
	return json.Marshal(fields)
}

// resolveEntityRef is the Name of the entity ref names among those the caller
// can use; ref itself when it names none.
func resolveEntityRef(ctx context.Context, tc Context, ref string) (string, error) {
	matches := tc.Registry.Match(ref)
	if len(matches) == 1 {
		return matches[0].Name, nil
	}
	var usable []entity.Entity
	for _, e := range matches {
		if canUse(ctx, tc, e) {
			usable = append(usable, e)
		}
	}
	switch len(usable) {
	case 0:
		return ref, nil
	case 1:
		return usable[0].Name, nil
	}
	visible := UsableBy(ctx, tc.Authorizer, tc.Role, tc.Subject)
	candidates := make([]string, 0, len(usable))
	for _, e := range usable {
		candidates = append(candidates, tc.Registry.ShortName(e, visible))
	}
	return "", &AmbiguousEntityError{Ref: ref, Candidates: candidates}
}

// canUse reports that the caller can reach e for some action (see UsableBy).
func canUse(ctx context.Context, tc Context, e entity.Entity) bool {
	return UsableBy(ctx, tc.Authorizer, tc.Role, tc.Subject)(e)
}

// UsableBy reports the entities a caller can reach for some action, as
// entity discovery does (see rbac.Decision.Reachable); every entity when
// authz is nil.
func UsableBy(
	ctx context.Context, authz rbac.Authorizer, role string, subject map[string]any,
) func(entity.Entity) bool {
	return func(e entity.Entity) bool {
		if authz == nil {
			return true
		}
		for _, action := range entity.ActionsFor(e.Kind) {
			dec, err := authz.Authorize(ctx, rbac.Request{Role: role, Subject: subject, Entity: e.Name, Action: action})
			if err == nil && dec.Reachable() {
				return true
			}
		}
		return false
	}
}

// entityName is how the caller refers to e: the shortest reference naming it
// alone among the entities the caller can use.
func entityName(ctx context.Context, tc Context, e entity.Entity) string {
	if tc.Registry == nil {
		return e.Name
	}
	return tc.Registry.ShortName(e, UsableBy(ctx, tc.Authorizer, tc.Role, tc.Subject))
}
