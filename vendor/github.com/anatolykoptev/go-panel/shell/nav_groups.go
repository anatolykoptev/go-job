package shell

// NavGroup is the exported form of a sidebar navigation group: a label and
// the nav items that belong to it. An empty Label represents the anonymous
// pre-group bucket (items registered before any group header).
//
// Exported so consumers and tests can inspect the grouping that Layout's
// sidebar renderer produces, without reimplementing the grouping rule.
type NavGroup struct {
	Label     string
	Collapsed bool
	Items     []NavItem
}

// NavGroups converts a flat []NavItem into grouped form using the same rule
// Layout's sidebar renderer applies (toNavGroups). Exported so tests can
// verify grouping against the real logic rather than a reimplementation.
//
// collapsedGroups is ChromeState.CollapsedGroups; nil map is safe (treated
// as empty).
func NavGroups(nav []NavItem, collapsedGroups map[string]bool) []NavGroup {
	internal := toNavGroups(nav, collapsedGroups)
	out := make([]NavGroup, len(internal))
	for i, g := range internal {
		out[i] = NavGroup(g)
	}
	return out
}
