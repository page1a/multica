package workspacelink

import (
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/permission"
)

// callers are the matrix columns. The agent columns carry the tier of the
// human their task runs as: an owner-backed agent proves agents are refused
// management outright, not merely for lack of tier.
var callers = []struct {
	name  string
	actor Actor
}{
	{"owner", Actor{Role: permission.RoleOwner}},
	{"admin", Actor{Role: permission.RoleAdmin}},
	{"member", Actor{Role: permission.RoleMember}},
	{"guest", Actor{Role: permission.RoleGuest}},
	{"agent(owner)", Actor{Role: permission.RoleOwner, IsAgent: true}},
	{"agent(member)", Actor{Role: permission.RoleMember, IsAgent: true}},
	{"agent(guest)", Actor{Role: permission.RoleGuest, IsAgent: true}},
}

// TestDecideMatrix pins every cell of side × caller × operation. Each row is
// one operation on one side; each character is one caller in the order of
// `callers`: Y allowed, . refused. Changing a cell here is changing the
// product's permission rule; update docs/kun/permission-model.md with it.
func TestDecideMatrix(t *testing.T) {
	//                         owner admin member guest agent(o) agent(m) agent(g)
	want := map[Op]map[Side]string{
		OpCreate: {
			SideSource: "Y......",
			SideViewer: ".......",
			SideNone:   ".......",
		},
		OpUpdateProjects: {
			SideSource: "Y......",
			SideViewer: ".......",
			SideNone:   ".......",
		},
		OpAccept: {
			SideSource: ".......",
			SideViewer: "YY.....",
			SideNone:   ".......",
		},
		OpRevoke: {
			SideSource: "Y......",
			SideViewer: "YY.....",
			SideNone:   ".......",
		},
		OpView: {
			SideSource: ".......",
			SideViewer: "YYY.YY.",
			SideNone:   ".......",
		},
		OpManage: {
			SideSource: "YY.....",
			SideViewer: "YY.....",
			SideNone:   ".......",
		},
		OpAudit: {
			SideSource: "Y......",
			SideViewer: "Y......",
			SideNone:   ".......",
		},
		OpSeePending: {
			SideSource: ".......",
			SideViewer: "YY..Y..",
			SideNone:   ".......",
		},
		OpSetManaged: {
			SideSource: "Y......",
			SideViewer: ".......",
			SideNone:   ".......",
		},
		// The agent columns here carry the originator's tier in the SOURCE
		// (DENE-1663). People never take this path: they act in the source
		// through their own membership.
		OpManageRemote: {
			SideSource: ".......",
			SideViewer: "....YY.",
			SideNone:   ".......",
		},
	}
	if len(want) != len(Ops) {
		t.Fatalf("matrix covers %d ops, package defines %d: add the new op's row", len(want), len(Ops))
	}
	for _, op := range Ops {
		for _, side := range []Side{SideSource, SideViewer, SideNone} {
			row, ok := want[op][side]
			if !ok || len(row) != len(callers) {
				t.Fatalf("matrix row %s/%s missing or wrong width", op, side)
			}
			var got strings.Builder
			for _, c := range callers {
				if Decide(op, side, c.actor) {
					got.WriteByte('Y')
				} else {
					got.WriteByte('.')
				}
			}
			if got.String() != row {
				t.Errorf("Decide(%s, %s) = %q, want %q (columns: owner admin member guest agent(owner) agent(member) agent(guest))",
					op, side, got.String(), row)
			}
		}
	}
}

// An unknown tier is refused everything, so a new role added to member.role
// starts with no link access until someone decides otherwise.
func TestDecideUnknownRoleRefused(t *testing.T) {
	for _, op := range Ops {
		for _, side := range []Side{SideSource, SideViewer} {
			if Decide(op, side, Actor{Role: "visitor"}) {
				t.Errorf("Decide(%s, %s, unknown role) = true", op, side)
			}
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	at, n, err := decodeCursor(encodeCursor(mustTime(t, "2026-10-03T13:06:41.123456Z"), 42))
	if err != nil || n != 42 || !at.Equal(mustTime(t, "2026-10-03T13:06:41.123456Z")) {
		t.Fatalf("round trip = %v %d %v", at, n, err)
	}
	if _, _, err := decodeCursor("not a cursor"); err == nil {
		t.Fatal("garbage cursor decoded")
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339Nano, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
