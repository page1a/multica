package routing

import "strings"

// Domain fit (DENE-1477) is the one answer to "does this agent suit this
// work". Every seat pick — executor, reviewer, quota relay, blocked
// re-dispatch, escalate, suggest — and the quoted base-name swap read it, so
// two entry points can never disagree about who fits.
//
// The rule, in full:
//
//   - the scene is the issue's own domain, else its project's domains (one or
//     more), else generic;
//   - fit: a specialisation whose domain is in the scene; in a generic scene,
//     a base role;
//   - generic: a base role, in a scene that has domains (the fallback);
//   - other: a specialisation for some other domain. Still pickable, last.
//
// The case table packages/core/agents/domain-fit.cases.json pins it for the
// server and the clients alike.

// Fit is how an agent sits against a scene.
type Fit string

const (
	FitMatch   Fit = "match"
	FitGeneric Fit = "generic"
	FitOther   Fit = "other"
)

// Rank orders the groups: fit first, other last.
func (f Fit) Rank() int {
	switch f {
	case FitMatch:
		return 0
	case FitGeneric:
		return 1
	}
	return 2
}

// Label is the group's word in the product.
func (f Fit) Label() string {
	switch f {
	case FitMatch:
		return "对口"
	case FitGeneric:
		return GenericDirection
	}
	return "其他"
}

// Scene is the domains a piece of work sits in; none means generic. Domains
// are names on the routing path and ids on the handler path — the rule only
// compares them.
type Scene struct {
	Domains []string
	// Source is where the domains came from; empty for a generic scene.
	Source DirectionSource
}

// GenericScene is the scene of work nobody put in a domain.
var GenericScene = Scene{}

// ResolveScene is the scene rule: the issue's own domain, else the project's
// domains, else generic.
func ResolveScene(issueDomain string, projectDomains []string) Scene {
	if d := strings.TrimSpace(issueDomain); d != "" {
		return Scene{Domains: []string{d}, Source: DirectionFromIssue}
	}
	var out []string
	for _, d := range projectDomains {
		if d = strings.TrimSpace(d); d != "" && !containsString(out, d) {
			out = append(out, d)
		}
	}
	if len(out) == 0 {
		return GenericScene
	}
	return Scene{Domains: out, Source: DirectionFromProject}
}

// SceneOf is a scene of exactly one domain, or generic for "".
func SceneOf(domain string) Scene {
	if domain == "" {
		return GenericScene
	}
	return Scene{Domains: []string{domain}}
}

// Generic reports a scene with no domain.
func (s Scene) Generic() bool { return len(s.Domains) == 0 }

// Has reports whether d is one of the scene's domains.
func (s Scene) Has(d string) bool { return d != "" && containsString(s.Domains, d) }

// Primary is the scene's first domain, "" when generic. It is what a single
// direction slot (the judge's state, the 接着做 cell) carries.
func (s Scene) Primary() string {
	if len(s.Domains) == 0 {
		return ""
	}
	return s.Domains[0]
}

// Label is the scene as the decision comment and the judge read it.
func (s Scene) Label() string {
	if s.Generic() {
		return GenericDirection
	}
	return strings.Join(s.Domains, "、")
}

// DomainFit is the rule. agentDomain is the domain the agent is a
// specialisation for, "" for a base role.
func DomainFit(scene Scene, agentDomain string) Fit {
	if agentDomain == "" {
		if scene.Generic() {
			return FitMatch
		}
		return FitGeneric
	}
	if scene.Has(agentDomain) {
		return FitMatch
	}
	return FitOther
}

// AgentFit is DomainFit for a routing seat: its recorded domain, and only for
// an agent with none recorded, the legacy base + direction name suffix.
func (l Ladder) AgentFit(scene Scene, a Agent) Fit {
	return DomainFit(scene, l.agentDirection(a))
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
