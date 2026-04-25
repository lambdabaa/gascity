package session

import (
	"fmt"
	"strings"

	"github.com/gastownhall/gascity/internal/beads"
)

// AssignmentTargets returns the concrete assignee values that uniquely route
// work to a specific session. Template names are intentionally excluded: they
// express queue routing, not ownership by one session instance.
func AssignmentTargets(sessionID string, metadata map[string]string) []string {
	seen := make(map[string]struct{}, 4)
	targets := make([]string, 0, 4)
	add := func(value string) {
		value = strings.TrimSpace(value)
		if value == "" {
			return
		}
		if _, ok := seen[value]; ok {
			return
		}
		seen[value] = struct{}{}
		targets = append(targets, value)
	}

	add(sessionID)
	if metadata != nil {
		add(metadata["session_name"])
		add(metadata["alias"])
		add(metadata["configured_named_identity"])
	}
	return targets
}

// AssignedOpenWork lists non-closed work beads assigned directly to the
// session across the provided stores.
func AssignedOpenWork(stores []beads.Store, sessionID string, metadata map[string]string) ([]beads.Bead, error) {
	targets := AssignmentTargets(sessionID, metadata)
	if len(targets) == 0 {
		return nil, nil
	}

	work := make([]beads.Bead, 0)
	seen := make(map[string]struct{})
	for _, store := range stores {
		if store == nil {
			continue
		}
		for _, target := range targets {
			items, err := store.List(beads.ListQuery{
				Assignee: target,
				Limit:    1,
				Sort:     beads.SortCreatedDesc,
			})
			if err != nil {
				return nil, fmt.Errorf("listing assigned work for session %s via %s: %w", sessionID, target, err)
			}
			for _, bead := range items {
				if bead.Status == "closed" || bead.Type == BeadType {
					continue
				}
				if _, ok := seen[bead.ID]; ok {
					continue
				}
				seen[bead.ID] = struct{}{}
				work = append(work, bead)
			}
		}
	}
	return work, nil
}
