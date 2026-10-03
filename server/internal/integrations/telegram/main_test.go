package telegram

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/testutil"
)

// testPool backs every test that exercises reply-delivery ownership.
//
// Those tests run the real queries rather than a hand-written stand-in on
// purpose. The first version of this feature shipped with an in-memory fake of
// the ownership table, and the fake quietly disagreed with the SQL about one
// column — enough for the whole suite to pass while the final answer never
// reached Telegram. A fake of a state machine is a second implementation of
// it, and the two drift.
//
// Without a database the suite exits green rather than red, the same contract
// the other DB-backed packages here follow.
var testPool *pgxpool.Pool

func TestMain(m *testing.M) {
	ctx := context.Background()
	pool, err := testutil.ConnectTestDatabase(ctx)
	if err != nil {
		testutil.ExitIfDatabaseRequired(err)
		os.Exit(m.Run())
	}
	testPool = pool
	code := m.Run()
	pool.Close()
	os.Exit(code)
}
