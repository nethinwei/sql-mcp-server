package workspace

// Conflicts arise when changes are re-applied onto another side (Replay):
// a newer published revision (an update), a draft taken in, or a write of
// the same workspace made meanwhile. They stand until resolved, or until the
// workspace no longer differs from the other side there.

// View is c as it stands in the workspace now, against base (its base
// revision's items): its path (a path through a list element that has since
// become a group stops at the group), the workspace's value and the other
// side's; false when there is nothing left to choose, because the workspace
// holds the other side's value already (for instance typed in by hand) or
// removed what holds it (the item, a list element or object on the way).
func (s Schema) View(now, base Snapshot, c Conflict) (Open, bool) {
	item := now.Get(c.Ref)
	theirsItem := Missing
	theirs := c.Theirs
	if c.TheirsGone {
		theirs = Missing
	}
	path := c.Path
	if !c.Pinned {
		theirsItem = base.Get(c.Ref)
		path = s.addressable(c.Path, item, theirsItem)
	}
	for i := range path {
		if IsMissing(s.GetAt(item, path[:i])) {
			return Open{}, false
		}
	}
	mine := s.GetAt(item, path)
	if !c.Pinned {
		theirs = s.GetAt(theirsItem, path)
	}
	if s.sameAt(path, mine, theirs) {
		return Open{}, false
	}
	c.Path = path
	return Open{Conflict: c, Mine: mine, TheirsValue: theirs}, true
}

// OpenConflicts are the conflicts that stand in now.
func (s Schema) OpenConflicts(now, base Snapshot, cs []Conflict) []Open {
	var out []Open
	for _, c := range cs {
		if o, ok := s.View(now, base, c); ok {
			out = append(out, o)
		}
	}
	return out
}

// within reports whether c is on outer's value or one under it.
func within(c, outer Conflict) bool { return c.Ref == outer.Ref && c.Path.within(outer.Path) }

// Replay re-applies the changes from old to local onto next: an item only
// local changed takes local's value, others next's; a value both changed
// differently keeps local's and is returned as a conflict against source
// (pinned: next's value held in the conflict).
func (s Schema) Replay(old, local, next Snapshot, source Source, pinned bool) (Snapshot, []Conflict) {
	changed := s.Changes(old, local)
	oldAt, localAt, nextAt := old.Index(), local.Index(), next.Index()
	at := func(snap Snapshot, index map[Ref]int, ref Ref) any {
		if i, ok := index[ref]; ok {
			return snap[i].Value
		}
		return Missing
	}
	values := make(map[Ref]any, len(changed))
	var found []Conflict
	for _, ref := range changed {
		v, conflicts := s.Merge(at(old, oldAt, ref), at(local, localAt, ref), at(next, nextAt, ref), nil)
		values[ref] = v
		for _, vc := range conflicts {
			c := Conflict{ID: NewID(), Ref: ref, Path: vc.Path, Source: source, Pinned: pinned}
			if pinned {
				c.Theirs, c.TheirsGone = vc.Theirs, IsMissing(vc.Theirs)
				if c.TheirsGone {
					c.Theirs = nil
				}
			}
			found = append(found, c)
		}
	}
	return next.Apply(values, changed), found
}

// Carry keeps the conflicts not resolved before (pending) next to those
// just found: a new one replaces those on or under its value (a removed
// item replaces the conflicts on its values); one on or under a pending
// conflict still standing is left to it. A pending conflict against the
// base (unpinned) stands, after an update, against the newest base (source).
func (s Schema) Carry(pending, found []Conflict, now, base Snapshot, source *Source) []Conflict {
	var carried []Conflict
	for _, p := range pending {
		replaced := false
		for _, f := range found {
			replaced = replaced || within(p, f)
		}
		if !p.Pinned && source != nil {
			p.Source = *source
		}
		if o, ok := s.View(now, base, p); !replaced && ok {
			carried = append(carried, o.Conflict)
		}
	}
	out := carried
	for _, f := range found {
		under := false
		for _, p := range carried {
			under = under || within(f, p)
		}
		if !under {
			out = append(out, f)
		}
	}
	return out
}

// Resolve resolves conflict id: mine keeps the workspace's value, theirs
// takes the other side's (removing it when removed there). Its choice
// decides the values under it on its side too; what now matches the other
// side needs no choice. It returns the workspace and the conflicts left, or
// false when id is not an open conflict.
func (s Schema) Resolve(now, base Snapshot, cs []Conflict, id string, theirs bool) (Snapshot, []Conflict, bool) {
	var chosen Open
	found := false
	for _, c := range cs {
		if c.ID == id {
			chosen, found = s.View(now, base, c)
		}
	}
	if !found {
		return now, cs, false
	}
	if theirs {
		item := s.SetAt(now.Get(chosen.Ref), chosen.Path, chosen.TheirsValue)
		now = now.With(chosen.Ref, item)
	}
	var left []Conflict
	for _, c := range cs {
		decided := c.ID == id ||
			within(c, chosen.Conflict) && len(c.Path) > len(chosen.Path) && c.Source == chosen.Source
		if _, ok := s.View(now, base, c); !decided && ok {
			left = append(left, c)
		}
	}
	return now, left, true
}
