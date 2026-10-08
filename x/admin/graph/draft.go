package graph

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/pmezard/go-difflib/difflib"
	"gopkg.in/yaml.v3"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configyaml"
)

func parseID(id string) (int64, error) {
	n, err := strconv.ParseInt(id, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid revision id %q", id)
	}
	return n, nil
}

func (r *Resolver) revisionByID(ctx context.Context, id string) (revision.Revision, error) {
	n, err := parseID(id)
	if err != nil {
		return revision.Revision{}, err
	}
	return r.Store.Get(ctx, n)
}

// draft is a merged, validated and normalized configuration.
type draft struct {
	base    revision.Revision
	cfg     *config.Config
	payload []byte
}

// buildDraft merges in into its base revision and runs the same chain as
// `store import`: decode, defaults, validation, store secret rule and the
// deterministic encoding.
func (r *Resolver) buildDraft(ctx context.Context, in DraftInput) (draft, error) {
	base, err := r.revisionByID(ctx, in.Base)
	if err != nil {
		return draft{}, err
	}
	baseCfg, err := bootstrap.LoadRevision(base)
	if err != nil {
		return draft{}, fmt.Errorf("base revision %d: %w", base.ID, err)
	}
	doc, err := yamlDocument(base.Payload)
	if err != nil {
		return draft{}, err
	}
	if err := applyDraftSections(doc, in, baseCfg); err != nil {
		return draft{}, err
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		return draft{}, err
	}
	cfg, err := bootstrap.LoadBytes(data)
	if err != nil {
		return draft{}, err
	}
	if err := bootstrap.ValidateStorePayload(cfg); err != nil {
		return draft{}, err
	}
	payload, err := configyaml.Encode(cfg)
	if err != nil {
		return draft{}, err
	}
	return draft{base: base, cfg: cfg, payload: payload}, nil
}

func applyDraftSections(doc map[string]any, in DraftInput, baseCfg *config.Config) error {
	if in.Entities != nil {
		entities := make([]any, 0, len(in.Entities))
		for _, e := range in.Entities {
			d, err := entityDoc(e)
			if err != nil {
				return err
			}
			entities = append(entities, d)
		}
		doc["entities"] = entities
	}
	if in.Roles != nil {
		roles, err := roleDocs(in.Roles)
		if err != nil {
			return err
		}
		doc["roles"] = roles
	}
	if in.Users != nil {
		users, err := userDocs(in.Users, baseCfg.Users)
		if err != nil {
			return err
		}
		doc["users"] = users
	}
	if in.Settings != nil {
		settings, ok := normalizeJSON(in.Settings).(map[string]any)
		if !ok {
			return errors.New("settings must be an object of top-level sections")
		}
		for key, val := range settings {
			if key == "datasources" || key == "database" || key == "databases" ||
				key == "entities" || key == "roles" || key == "users" {
				return fmt.Errorf("settings cannot change %q; datasources are managed by the CLI and "+
					"entities, roles and users have their own draft fields", key)
			}
			doc[key] = val
		}
	}
	return nil
}

// restartChanges compares a configuration with the published one.
func (r *Resolver) restartChanges(ctx context.Context, next *config.Config) ([]string, error) {
	current, err := r.Store.Published(ctx)
	if errors.Is(err, revision.ErrNoPublished) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	cur, err := bootstrap.LoadRevision(current)
	if err != nil {
		return nil, err
	}
	return orEmpty(bootstrap.RestartChanges(cur, next)), nil
}

// unifiedDiff compares two payloads in the current encoding, so revisions
// stored by an older encoder differ only where their content does.
func unifiedDiff(fromName string, from []byte, toName string, to []byte) (string, error) {
	return difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: difflib.SplitLines(string(reencode(from))), B: difflib.SplitLines(string(reencode(to))),
		FromFile: fromName, ToFile: toName, Context: 3,
	})
}

// reencode returns payload in the current encoding, or unchanged when it no
// longer decodes.
func reencode(payload []byte) []byte {
	cfg, err := configyaml.Decode(payload)
	if err != nil {
		return payload
	}
	out, err := configyaml.Encode(cfg)
	if err != nil {
		return payload
	}
	return out
}
