package routing

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

//go:embed ladder.json
var ladderJSON []byte

// TierSeat is one routable base on a rung. Provider is the model family
// (openai, anthropic, …), which is how a same-tier handoff tells a GPT seat
// from any other house. Model and Thinking are the live seat config the rung
// was aligned to. A direction-specialised seat is not a separate row; it
// inherits this base.
type TierSeat struct {
	Base     string `json:"base"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Thinking string `json:"thinking,omitempty"`
}

// Tier is one rung of the seat ladder. Base is the untagged name-convention
// seat. Seats is every routable base that sits on the rung.
type Tier struct {
	Key   string     `json:"key"`
	Base  string     `json:"base"`
	Label string     `json:"label"`
	Seats []TierSeat `json:"seats"`
}

// Ladder is the candidate source: the ordered tiers, the known directions, and
// the project -> direction table. It is pure data loaded from ladder.json.
type Ladder struct {
	Tiers      []Tier            `json:"tiers"`
	Directions []string          `json:"directions"`
	Projects   map[string]string `json:"projects"`
	// Fallback names the rung an unconfident verdict lands on. Routing always
	// dispatches, so "the judge was not sure" has to resolve to a seat; this
	// is that seat, chosen once as data rather than per call.
	Fallback string `json:"fallback_tier"`
}

// DefaultLadder is the shipped ladder. Parsed once at init; a malformed
// ladder.json is a build-time-visible programming error, not a runtime state
// this package has to model, so it panics.
var DefaultLadder = mustLoadLadder()

func mustLoadLadder() Ladder {
	var l Ladder
	if err := json.Unmarshal(ladderJSON, &l); err != nil {
		panic(fmt.Sprintf("routing: ladder.json is malformed: %v", err))
	}
	if len(l.Tiers) == 0 {
		panic("routing: ladder.json declares no tiers")
	}
	if l.Fallback != "" {
		if _, ok := l.TierByKey(l.Fallback); !ok {
			panic("routing: ladder.json names fallback_tier " + l.Fallback + ", which is not a declared tier")
		}
	}
	seenTier := make(map[string]struct{}, len(l.Tiers))
	seenSeat := make(map[string]string, len(l.Tiers))
	for _, t := range l.Tiers {
		if t.Key == "" || t.Label == "" || t.Base == "" {
			panic("routing: ladder.json has a tier missing key, label, or base")
		}
		if _, dup := seenTier[t.Key]; dup {
			panic("routing: ladder.json repeats tier " + t.Key)
		}
		seenTier[t.Key] = struct{}{}
		if len(t.Seats) == 0 {
			panic("routing: ladder.json tier " + t.Key + " lists no seats")
		}
		foundBase := false
		for _, s := range t.Seats {
			if s.Base == "" || s.Provider == "" || s.Model == "" {
				panic("routing: ladder.json tier " + t.Key + " has a seat missing base, provider, or model")
			}
			if prev, dup := seenSeat[s.Base]; dup {
				panic("routing: ladder.json lists " + s.Base + " on both " + prev + " and " + t.Key)
			}
			seenSeat[s.Base] = t.Key
			if s.Base == t.Base {
				foundBase = true
			}
		}
		if !foundBase {
			panic("routing: ladder.json tier " + t.Key + " base " + t.Base + " is not one of its seats")
		}
	}
	return l
}

// GenericDirection is the table value for "this project is known, and it has
// no domain": the generic rung, on purpose. It is a value rather than an
// absence so the decision comment can tell a project nobody classified from
// one somebody classified as general-purpose.
const GenericDirection = "通用"

// DirectionMatch is how a project resolved against the project table.
type DirectionMatch struct {
	// Direction is the resolved direction, empty for the generic rung.
	Direction string
	// Known reports that the table has a usable row for this project. A known
	// project with an empty Direction was deliberately mapped to generic.
	Known bool
	// Invalid carries a table value that names no declared direction. Such a
	// row is ignored rather than followed: base+typo names no seat, and the
	// silent fallback would hide the typo forever.
	Invalid string
}

// WithProjects returns the ladder with a workspace's own project rows laid
// over the shipped ones. The workspace rows win, which is what makes the table
// editable without a release: ladder.json is only the default.
func (l Ladder) WithProjects(overrides map[string]string) Ladder {
	if len(overrides) == 0 {
		return l
	}
	merged := make(map[string]string, len(l.Projects)+len(overrides))
	for k, v := range l.Projects {
		merged[normalizeProjectKey(k)] = v
	}
	for k, v := range overrides {
		merged[normalizeProjectKey(k)] = v
	}
	l.Projects = merged
	return l
}

func normalizeProjectKey(name string) string {
	return strings.ToLower(strings.TrimSpace(name))
}

// Direction resolves an issue's direction from its project name. An unknown or
// empty project yields "", which means the generic rung — routing never guesses
// a direction, because guessing one silently sends work to a seat carrying the
// wrong domain pack.
func (l Ladder) Direction(projectName string) string {
	return l.ResolveDirection(projectName).Direction
}

// ResolveDirection looks a project up in the table: an exact row first, then
// the longest `prefix*` row, both case-insensitive. Rows are data typed by a
// person, so a family of projects (game-*) is one row rather than one per
// project that will ever exist.
func (l Ladder) ResolveDirection(projectName string) DirectionMatch {
	name := normalizeProjectKey(projectName)
	if name == "" {
		return DirectionMatch{}
	}
	value, found, bestLen := "", false, -1
	for rawKey, v := range l.Projects {
		key := normalizeProjectKey(rawKey)
		if key == name {
			value, found = v, true
			break
		}
		if prefix, ok := strings.CutSuffix(key, "*"); ok && strings.HasPrefix(name, prefix) && len(prefix) > bestLen {
			value, found, bestLen = v, true, len(prefix)
		}
	}
	if !found {
		return DirectionMatch{}
	}
	value = strings.TrimSpace(value)
	if value == "" || value == GenericDirection {
		return DirectionMatch{Known: true}
	}
	for _, d := range l.Directions {
		if d == value {
			return DirectionMatch{Direction: d, Known: true}
		}
	}
	return DirectionMatch{Invalid: value}
}

// Seat is one routable agent.
type Seat struct {
	ID        string
	Name      string
	TierKey   string
	TierLabel string
	Direction string
}

// SeatName is the naming convention that links a tier to its
// direction-specialised seat: base name concatenated with the direction.
func SeatName(base, direction string) string {
	return base + direction
}

// Candidates narrows the workspace roster to the seats routing may pick for
// this direction: one seat per tier, in ladder order.
//
// The rung a seat sits on comes from the tag a human put on that seat, not
// from its name. That is the whole point of the tag: strength is a judgement
// about model AND thinking level AND prompt, so it is something the person who
// configured the seat knows and nothing the server can infer. A tier nobody
// tagged falls back to the base-name convention in ladder.json, which is how a
// workspace that has tagged nothing yet still routes.
//
// This is the whole of today's filter chain. The chain is where a future
// execution-only model would be inserted — a second stage appended here leaves
// Route's shape and both call sites untouched.
func (l Ladder) Candidates(direction string, roster map[string]Agent) []Seat {
	tagged := l.taggedByTier(roster)
	seats := make([]Seat, 0, len(l.Tiers))
	for _, t := range l.Tiers {
		if a, dir, ok := l.pickTagged(tagged[t.Key], direction); ok {
			seats = append(seats, Seat{ID: a.ID, Name: a.Name, TierKey: t.Key, TierLabel: t.Label, Direction: dir})
			continue
		}
		if seat, ok := l.pickByName(t, direction, roster); ok {
			seats = append(seats, seat)
		}
	}
	return seats
}

// taggedByTier groups the roster by the tier key each seat was tagged with.
// Untagged seats and seats carrying a tier this ladder does not declare are
// dropped: an unknown rung is not a rung.
func (l Ladder) taggedByTier(roster map[string]Agent) map[string][]Agent {
	out := make(map[string][]Agent, len(l.Tiers))
	for _, a := range roster {
		key, ok := l.NormalizeTier(a.Tier)
		if !ok || key == "" {
			continue
		}
		out[key] = append(out[key], a)
	}
	for key := range out {
		sort.Slice(out[key], func(i, j int) bool { return out[key][i].Name < out[key][j].Name })
	}
	return out
}

// pickTagged chooses one seat out of the tagged seats on a rung: the one
// specialised for this direction when there is one, otherwise the undirected
// seat, otherwise the first by name so the choice is stable across calls.
func (l Ladder) pickTagged(seats []Agent, direction string) (Agent, string, bool) {
	if len(seats) == 0 {
		return Agent{}, "", false
	}
	if direction != "" {
		for _, a := range seats {
			if l.seatDirection(a.Name) == direction {
				return a, direction, true
			}
		}
	}
	for _, a := range seats {
		if l.seatDirection(a.Name) == "" {
			return a, "", true
		}
	}
	return seats[0], l.seatDirection(seats[0].Name), true
}

// seatDirection reads the direction off a seat name by the naming convention
// (base + direction). A name matching no known direction is undirected.
func (l Ladder) seatDirection(name string) string {
	for _, d := range l.Directions {
		if d != "" && strings.HasSuffix(name, d) {
			return d
		}
	}
	return ""
}

// pickByName is the untagged fallback: the original base-name convention.
//
// A seat that carries a tag is never placed by its name, on any rung. The tag
// is the seat's own statement about its strength, and a name convention that
// could still drag it onto another rung would make tagging advisory.
func (l Ladder) pickByName(t Tier, direction string, roster map[string]Agent) (Seat, bool) {
	if t.Base == "" {
		return Seat{}, false
	}
	lookup := func(name string) (Agent, bool) {
		a, ok := roster[name]
		if !ok {
			return Agent{}, false
		}
		if key, valid := l.NormalizeTier(a.Tier); valid && key != "" {
			return Agent{}, false
		}
		return a, true
	}
	name := SeatName(t.Base, direction)
	a, ok := lookup(name)
	if !ok && direction != "" {
		// A direction with no specialised seat on this rung falls back to the
		// generic seat rather than dropping the rung: losing a rung silently
		// narrows the ladder the judge is choosing from.
		if a, ok = lookup(t.Base); ok {
			return Seat{ID: a.ID, Name: a.Name, TierKey: t.Key, TierLabel: t.Label}, true
		}
	}
	if !ok {
		return Seat{}, false
	}
	return Seat{ID: a.ID, Name: a.Name, TierKey: t.Key, TierLabel: t.Label, Direction: direction}, true
}

// Agent is the slice of an agent record routing needs.
type Agent struct {
	ID   string
	Name string
	// Tier is the tier key a human tagged this seat with, empty when the seat
	// carries no tag. It is the authoritative rung: strength is not derivable
	// from the model id, because the same model at a different thinking level
	// is a different rung and two seats may sit on one model deliberately.
	Tier string
}

// SeatByTier finds the candidate on a named rung.
func SeatByTier(seats []Seat, tierKey string) (Seat, bool) {
	for _, s := range seats {
		if strings.EqualFold(s.TierKey, tierKey) {
			return s, true
		}
	}
	return Seat{}, false
}

// StrongerThan returns the candidate one rung above the given seat, if the
// ladder has one. Used to keep a reviewer from being the seat that did the
// work: reviewing your own output is not review.
// SeatIndex reports where a seat sits in the candidate list, and whether it is
// there at all. "Not on the ladder" and "on the top rung" both make
// StrongerThan return false, and the two call for opposite fallbacks, so the
// difference has to be askable.
func SeatIndex(seats []Seat, seat Seat) (int, bool) {
	for i, s := range seats {
		if s.ID == seat.ID {
			return i, true
		}
	}
	return 0, false
}

func StrongerThan(seats []Seat, seat Seat) (Seat, bool) {
	for i, s := range seats {
		if s.ID == seat.ID {
			if i == 0 {
				return Seat{}, false
			}
			return seats[i-1], true
		}
	}
	return Seat{}, false
}

// WeakerThan returns the candidate one rung below the given seat, if the
// ladder has one. It is the reviewer of last resort for work done by the top
// rung: a reviewer checks, merges and closes — it does not redo the work — so
// a rung below is a real check, and it is the only remaining way to keep
// acceptance on a seat now that the slot may never name a person.
func WeakerThan(seats []Seat, seat Seat) (Seat, bool) {
	for i, s := range seats {
		if s.ID == seat.ID {
			if i == len(seats)-1 {
				return Seat{}, false
			}
			return seats[i+1], true
		}
	}
	return Seat{}, false
}

// TierKeys lists the rung keys in ladder order — the exact value range the
// judge is allowed to answer with.
func (l Ladder) TierKeys() []string {
	keys := make([]string, 0, len(l.Tiers))
	for _, t := range l.Tiers {
		keys = append(keys, t.Key)
	}
	return keys
}

// Tier lookup helpers. The API stores a tier KEY; a person types a LABEL.
// Both name the same rung, so both are accepted at the boundary and only the
// key is ever persisted — one vocabulary in the database, two spellings for
// whoever is typing.

// SeatByName finds a routable seat. A direction-specialised name inherits its
// base, so 孙悟饭游戏 reports the same provider and rung as 孙悟饭.
func (l Ladder) SeatByName(name string) (TierSeat, string, bool) {
	if seat, tier, ok := l.seatByBase(name); ok {
		return seat, tier, true
	}
	if dir := l.seatDirection(name); dir != "" {
		if seat, tier, ok := l.seatByBase(strings.TrimSuffix(name, dir)); ok {
			return seat, tier, true
		}
	}
	return TierSeat{}, "", false
}

// ProviderOf reports the provider family of a routable seat, including a
// direction specialisation of a listed base. Empty means the name is not on
// this ladder.
func (l Ladder) ProviderOf(name string) (string, bool) {
	seat, _, ok := l.SeatByName(name)
	if !ok || seat.Provider == "" {
		return "", false
	}
	return seat.Provider, true
}

// TierOf reports the rung key a routable seat sits on.
func (l Ladder) TierOf(name string) (string, bool) {
	_, tier, ok := l.SeatByName(name)
	return tier, ok
}

func (l Ladder) seatByBase(base string) (TierSeat, string, bool) {
	for _, t := range l.Tiers {
		for _, s := range t.Seats {
			if s.Base == base {
				return s, t.Key, true
			}
		}
	}
	return TierSeat{}, "", false
}

// TierByKey finds a rung by its key.
func (l Ladder) TierByKey(key string) (Tier, bool) {
	for _, t := range l.Tiers {
		if strings.EqualFold(t.Key, key) {
			return t, true
		}
	}
	return Tier{}, false
}

// NormalizeTier resolves a key or a label to the canonical tier key. An empty
// input normalises to empty with ok=true: "no tier" is a legal value, it is
// how a seat says it is not on the ladder.
func (l Ladder) NormalizeTier(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", true
	}
	for _, t := range l.Tiers {
		if strings.EqualFold(t.Key, raw) || strings.EqualFold(t.Label, raw) {
			return t.Key, true
		}
	}
	return "", false
}

// TierLabels lists the human labels in ladder order, strongest first.
func (l Ladder) TierLabels() []string {
	out := make([]string, 0, len(l.Tiers))
	for _, t := range l.Tiers {
		out = append(out, t.Label)
	}
	return out
}

// RequestedTier reads the rung a human asked for off the issue's labels.
//
// A label naming a tier is authoritative: the person writing the ticket
// already knows how hard it is, so asking a model to re-derive that is a guess
// layered on top of an answer. Routing only judges what nobody told it.
//
// Two labels naming different rungs is a contradiction, not a vote: nothing is
// requested and the judge decides, because picking one of two conflicting
// instructions silently is worse than asking.
func (l Ladder) RequestedTier(labels []string) (string, bool) {
	found := ""
	for _, raw := range labels {
		key, ok := l.NormalizeTier(raw)
		if !ok || key == "" {
			continue
		}
		if found != "" && found != key {
			return "", false
		}
		found = key
	}
	return found, found != ""
}

// FallbackSeat is the seat routing dispatches to when the judge's answer is
// unusable — under the threshold, or naming a rung this workspace has no seat
// on. It prefers the ladder's declared fallback rung and otherwise takes the
// strongest candidate present, because a ticket parked in todo costs more than
// a seat a person has to change.
func (l Ladder) FallbackSeat(candidates []Seat) (Seat, bool) {
	if len(candidates) == 0 {
		return Seat{}, false
	}
	if l.Fallback != "" {
		if seat, ok := SeatByTier(candidates, l.Fallback); ok {
			return seat, true
		}
	}
	return candidates[0], true
}
