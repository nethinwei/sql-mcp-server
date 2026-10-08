// Package revisionops holds the configuration store operations shared by the
// `store`/`migrate` CLI and the admin API: normalizing payloads, creating
// drafts, publishing, rolling back and diffing. Entry points parse
// arguments, check identity and format output; the rules (publishable
// checks, optimistic concurrency, audit) live here so both behave alike.
package revisionops

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/pmezard/go-difflib/difflib"

	"github.com/nethinwei/sql-mcp-server/core/config"
	"github.com/nethinwei/sql-mcp-server/core/revision"
	"github.com/nethinwei/sql-mcp-server/x/bootstrap"
	"github.com/nethinwei/sql-mcp-server/x/configyaml"
)

// Service runs revision operations on a store. Via names the entry point in
// audit records ("cli", "admin-api").
type Service struct {
	Store revision.Store
	Via   string
}

// ConflictError reports that another revision was published after the one
// the caller based its work on. It wraps revision.ErrConflict.
type ConflictError struct {
	Expected, Published int64
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%v: revision %d was published after revision %d", revision.ErrConflict, e.Published, e.Expected)
}

func (e *ConflictError) Unwrap() error { return revision.ErrConflict }

// Normalize applies the store rules to a loaded configuration and returns
// its payload in the deterministic encoding.
func Normalize(cfg *config.Config) ([]byte, error) {
	if err := bootstrap.ValidateStorePayload(cfg); err != nil {
		return nil, err
	}
	return configyaml.Encode(cfg)
}

func (s Service) audit(op string, rev revision.Revision, author string) {
	slog.Info("config store "+op, "op", op, "revision", rev.ID, "contentHash", rev.ContentHash,
		"author", author, "via", s.Via)
}

// published returns the published revision, or the zero revision when the
// store has none.
func (s Service) published(ctx context.Context) (revision.Revision, error) {
	current, err := s.Store.Published(ctx)
	if err != nil && !errors.Is(err, revision.ErrNoPublished) {
		return revision.Revision{}, err
	}
	return current, nil
}

// DraftRequest creates a draft revision from a normalized payload.
type DraftRequest struct {
	Payload []byte
	// Parent is the revision the draft builds on; 0 means the published one.
	Parent          int64
	Author, Comment string
	// SkipIfPublished returns the published revision instead of a new draft
	// when the payload equals it.
	SkipIfPublished bool
}

// Draft stores a draft. unchanged reports that SkipIfPublished applied and
// rev is the published revision.
func (s Service) Draft(ctx context.Context, req DraftRequest) (rev revision.Revision, unchanged bool, err error) {
	current, err := s.published(ctx)
	if err != nil {
		return revision.Revision{}, false, err
	}
	if req.SkipIfPublished && current.ID != 0 && current.ContentHash == revision.Hash(req.Payload) {
		return current, true, nil
	}
	parent := req.Parent
	if parent == 0 {
		parent = current.ID
	}
	rev, err = s.Store.Create(ctx, revision.Draft{
		ParentID: parent, Payload: req.Payload, Author: req.Author, Comment: req.Comment,
	})
	if err != nil {
		return revision.Revision{}, false, err
	}
	s.audit("draft", rev, req.Author)
	return rev, false, nil
}

// PublishRequest publishes a draft.
type PublishRequest struct {
	ID int64
	// RestartRequired allows changes that apply only after a restart.
	RestartRequired bool
	// ExpectedPublished, when set, is the published revision the caller
	// based its work on; another one being published fails with
	// *ConflictError.
	ExpectedPublished *int64
	Author            string
}

// Publish checks and publishes a draft.
func (s Service) Publish(ctx context.Context, req PublishRequest) (revision.Revision, error) {
	target, err := s.Store.Get(ctx, req.ID)
	if err != nil {
		return revision.Revision{}, err
	}
	current, err := s.published(ctx)
	if err != nil {
		return revision.Revision{}, err
	}
	if req.ExpectedPublished != nil && *req.ExpectedPublished != current.ID {
		return revision.Revision{}, &ConflictError{Expected: *req.ExpectedPublished, Published: current.ID}
	}
	if err := bootstrap.CheckPublishable(current, target, req.RestartRequired); err != nil {
		return revision.Revision{}, err
	}
	rev, err := s.Store.Publish(ctx, target.ID, current.ID, revision.Meta{Author: req.Author})
	if err != nil {
		return revision.Revision{}, err
	}
	s.audit("publish", rev, req.Author)
	return rev, nil
}

// RollbackRequest restores earlier published content as a new revision.
type RollbackRequest struct {
	// To is the revision to restore; 0 means the newest earlier published
	// content.
	To              int64
	RestartRequired bool
	Author, Comment string
}

// RollbackResult describes a rollback.
type RollbackResult struct {
	// Published is the new revision; From was published before it and
	// Target holds the restored content.
	Published    revision.Revision
	From, Target int64
}

// Rollback checks and publishes the content of an earlier revision.
func (s Service) Rollback(ctx context.Context, req RollbackRequest) (RollbackResult, error) {
	current, err := s.Store.Published(ctx)
	if err != nil {
		return RollbackResult{}, err
	}
	all, err := s.Store.List(ctx, 0)
	if err != nil {
		return RollbackResult{}, err
	}
	chosen, err := revision.RollbackTarget(all, current, req.To)
	if err != nil {
		return RollbackResult{}, err
	}
	target, err := s.Store.Get(ctx, chosen.ID)
	if err != nil {
		return RollbackResult{}, err
	}
	if err := bootstrap.CheckPublishable(current, target, req.RestartRequired); err != nil {
		return RollbackResult{}, err
	}
	rev, err := s.Store.Rollback(ctx, current.ID, target.ID, revision.Meta{Author: req.Author, Comment: req.Comment})
	if err != nil {
		return RollbackResult{}, err
	}
	s.audit("rollback", rev, req.Author)
	return RollbackResult{Published: rev, From: current.ID, Target: target.ID}, nil
}

// Diff returns a unified diff of two payloads in the current encoding, so
// revisions stored by an older encoder differ only where their content does.
func Diff(fromName string, from []byte, toName string, to []byte) (string, error) {
	return RawDiff(fromName, Reencode(from), toName, Reencode(to))
}

// RawDiff compares stored bytes as they are.
func RawDiff(fromName string, from []byte, toName string, to []byte) (string, error) {
	return difflib.GetUnifiedDiffString(difflib.UnifiedDiff{
		A: difflib.SplitLines(string(from)), B: difflib.SplitLines(string(to)),
		FromFile: fromName, ToFile: toName, Context: 3,
	})
}

// Reencode returns payload in the current encoding, or unchanged when it no
// longer decodes.
func Reencode(payload []byte) []byte {
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
