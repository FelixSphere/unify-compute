package billingtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/FelixSphere/unify-compute/compute-api/internal/billing"
	"github.com/FelixSphere/unify-compute/compute-api/internal/console/consoletest"
	"github.com/FelixSphere/unify-compute/compute-api/internal/store"
)

// TestsCalling lists the top-level Test functions in dir's _test.go files
// whose bodies call helper, so a parity list can be checked for tests added
// after it was written.
func TestsCalling(t testing.TB, dir, helper string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	fset := token.NewFileSet()
	for _, path := range files {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, path, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !strings.HasPrefix(fn.Name.Name, "Test") || fn.Body == nil {
				continue
			}
			calls := false
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				if c, ok := n.(*ast.CallExpr); ok {
					if id, ok := c.Fun.(*ast.Ident); ok && id.Name == helper {
						calls = true
					}
				}
				return !calls
			})
			if calls {
				out = append(out, fn.Name.Name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// UseLocalBooks makes every console fake created until t ends keep its
// balances and holds in a local biller (consoletest.SetBookFactory), each in
// a schema of its own, and checks the balance invariant of every one of
// them when t ends. Call it from a sequential top-level test and run the
// tests to repeat as its subtests: their helpers build the fake as usual
// and need no change.
func UseLocalBooks(t *testing.T) {
	t.Helper()
	raw := os.Getenv("COMPUTE_TEST_DATABASE_URL")
	if raw == "" {
		t.Skip("COMPUTE_TEST_DATABASE_URL not set; skipping Postgres-backed test")
	}
	var mu sync.Mutex
	var locals []*billing.Local
	var cleanups []func()
	consoletest.SetBookFactory(func(s *consoletest.Server) consoletest.Book {
		pool, drop := schema(raw)
		l := billing.New(pool, s.Now)
		mu.Lock()
		locals = append(locals, l)
		cleanups = append(cleanups, drop)
		mu.Unlock()
		return &book{Local: l, t: panicTB{}, db: pool}
	})
	t.Cleanup(func() {
		consoletest.SetBookFactory(nil)
		mu.Lock()
		defer mu.Unlock()
		for _, l := range locals {
			AssertInvariant(t, l)
		}
		for _, drop := range cleanups {
			drop()
		}
		if len(locals) == 0 {
			t.Error("no console fake used the local biller")
		}
	})
}

// schema opens a migrated store in a fresh schema; drop removes it.
func schema(raw string) (*pgxpool.Pool, func()) {
	ctx := context.Background()
	var suffix [6]byte
	_, _ = rand.Read(suffix[:])
	name := "billbook_" + hex.EncodeToString(suffix[:])
	admin, err := pgx.Connect(ctx, raw)
	if err != nil {
		panic("billingtest: " + err.Error())
	}
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+name); err != nil {
		panic("billingtest: " + err.Error())
	}
	u, err := url.Parse(raw)
	if err != nil {
		panic("billingtest: " + err.Error())
	}
	q := u.Query()
	q.Set("search_path", name)
	u.RawQuery = q.Encode()
	st, err := store.Open(ctx, u.String())
	if err != nil {
		panic("billingtest: " + err.Error())
	}
	if err := st.Migrate(ctx); err != nil {
		panic("billingtest: " + err.Error())
	}
	return st.Pool(), func() {
		st.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+name+" CASCADE")
		_ = admin.Close(context.Background())
	}
}

// panicTB is the testing.TB of a book made by the factory, which has no test
// of its own: an unexpected database error fails the test that hit it by
// panicking there.
type panicTB struct{ testing.TB }

func (panicTB) Helper()                        {}
func (panicTB) Fatal(args ...any)              { panic(fmt.Sprint(args...)) }
func (panicTB) Fatalf(format string, a ...any) { panic(fmt.Sprintf(format, a...)) }
func (panicTB) Errorf(format string, a ...any) { panic(fmt.Sprintf(format, a...)) }
