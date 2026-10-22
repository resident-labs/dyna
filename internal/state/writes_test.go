package state

import (
	"bufio"
	"bytes"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "lower or remove stale lines in testdata/direct-writes.txt (never adds one)")

const allowlistPath = "testdata/direct-writes.txt"

// writeCalls are the package functions that write a file, by import path.
var writeCalls = map[string]map[string]bool{
	"os":                           {"WriteFile": true, "Create": true, "Rename": true, "OpenFile": true, "Remove": true, "RemoveAll": true},
	"dyna-agent/internal/fsutil":   {"WriteFile": true, "WriteExecutable": true},
	"dyna-agent/internal/store":    {"WriteJSON": true, "Update": true},
	"dyna-agent/internal/database": {"Immediate": true},
}

// cheap is a quick filter: a file without any of these cannot match.
var cheap = [][]byte{[]byte("WriteFile"), []byte("Create("), []byte("Rename("), []byte("OpenFile("), []byte("Remove"), []byte("WriteExecutable("), []byte("WriteJSON("), []byte("Update("), []byte("Immediate("), []byte("Begin"), []byte("Conn("), []byte("Exec"), []byte("Query")}

// directWrites returns, for one Go source, the number of direct state
// writes per function, keyed "<file> <func>".
func directWrites(name string, src []byte) (map[string]int, error) {
	found := map[string]int{}
	if !slices.ContainsFunc(cheap, func(b []byte) bool { return bytes.Contains(src, b) }) {
		return found, nil
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	consts := fileStrings(file)
	imports := map[string]string{} // local name -> import path
	for _, imp := range file.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		local := path[strings.LastIndex(path, "/")+1:]
		if imp.Name != nil {
			local = imp.Name.Name
		}
		imports[local] = path
	}
	for _, decl := range file.Decls {
		owner := "<init>"
		if fn, ok := decl.(*ast.FuncDecl); ok {
			owner = fn.Name.Name
			if fn.Recv != nil && len(fn.Recv.List) > 0 {
				owner = receiverName(fn.Recv.List[0].Type) + "." + owner
			}
		}
		// scopes holds, per enclosing function, its *sql.Conn and *sql.Tx
		// parameters. Statements on them run in a transaction or connection
		// that the caller opened, and that opener is what counts.
		var scopes []map[string]bool
		var stack []ast.Node
		isTx := func(name string) bool {
			return slices.ContainsFunc(scopes, func(m map[string]bool) bool { return m[name] })
		}
		ast.Inspect(decl, func(n ast.Node) bool {
			if n == nil {
				if top := stack[len(stack)-1]; isFunc(top) {
					scopes = scopes[:len(scopes)-1]
				}
				stack = stack[:len(stack)-1]
				return true
			}
			stack = append(stack, n)
			if isFunc(n) {
				scopes = append(scopes, txParams(n, imports))
			}
			if call, ok := n.(*ast.CallExpr); ok && isDirectWrite(call, file.Name.Name, imports, consts, isTx) {
				found[name+" "+owner]++
			}
			return true
		})
	}
	return found, nil
}

func isFunc(n ast.Node) bool {
	switch n.(type) {
	case *ast.FuncDecl, *ast.FuncLit:
		return true
	}
	return false
}

// txParams returns the names of a function's parameters of type *sql.Conn
// or *sql.Tx.
func txParams(n ast.Node, imports map[string]string) map[string]bool {
	var ft *ast.FuncType
	switch f := n.(type) {
	case *ast.FuncDecl:
		ft = f.Type
	case *ast.FuncLit:
		ft = f.Type
	}
	names := map[string]bool{}
	for _, field := range ft.Params.List {
		star, ok := field.Type.(*ast.StarExpr)
		if !ok {
			continue
		}
		sel, ok := star.X.(*ast.SelectorExpr)
		if !ok {
			continue
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || imports[pkg.Name] != "database/sql" || (sel.Sel.Name != "Conn" && sel.Sel.Name != "Tx") {
			continue
		}
		for _, name := range field.Names {
			names[name.Name] = true
		}
	}
	return names
}

func receiverName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverName(t.X)
	case *ast.IndexExpr:
		return receiverName(t.X)
	case *ast.IndexListExpr:
		return receiverName(t.X)
	case *ast.Ident:
		return t.Name
	}
	return "?"
}

// fileStrings maps the file's top-level string constants and variables to
// their values, so a statement kept in a constant is still seen.
func fileStrings(file *ast.File) map[string]string {
	values := map[string]string{}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || (gen.Tok != token.CONST && gen.Tok != token.VAR) {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if i < len(vs.Values) {
					if text, ok := stringValue(vs.Values[i], nil); ok {
						values[name.Name] = text
					}
				}
			}
		}
	}
	return values
}

// stringValue reads a string literal, a known constant, or the first part
// of a + concatenation.
func stringValue(expr ast.Expr, consts map[string]string) (string, bool) {
	switch e := expr.(type) {
	case *ast.BasicLit:
		if e.Kind != token.STRING {
			return "", false
		}
		text, err := strconv.Unquote(e.Value)
		return text, err == nil
	case *ast.Ident:
		text, ok := consts[e.Name]
		return text, ok
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			return stringValue(e.X, consts)
		}
	}
	return "", false
}

// isDirectWrite counts a call that writes state. It counts the openers:
// database.Immediate, Begin, BeginTx, and a connection taken with Conn(ctx)
// outside package database, whatever their statements are. An Exec counts
// unless it runs on a *sql.Conn or *sql.Tx parameter of an enclosing
// function (its opener counts instead), or its statement provably only
// changes the schema or controls a transaction.
func isDirectWrite(call *ast.CallExpr, pkgName string, imports, consts map[string]string, isTx func(string) bool) bool {
	if id, ok := call.Fun.(*ast.Ident); ok {
		return pkgName == "database" && id.Name == "Immediate"
	}
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if pkg, ok := sel.X.(*ast.Ident); ok && writeCalls[imports[pkg.Name]][sel.Sel.Name] {
		if imports[pkg.Name] == "os" && sel.Sel.Name == "OpenFile" {
			return len(call.Args) > 1 && opensForWrite(call.Args[1])
		}
		return true
	}
	if recv, ok := sel.X.(*ast.Ident); ok && isTx(recv.Name) && sqlCalls[sel.Sel.Name] {
		return false
	}
	switch sel.Sel.Name {
	case "Begin", "BeginTx":
		return true
	case "Conn":
		return len(call.Args) == 1 && pkgName != "database"
	case "Exec":
		return len(call.Args) > 0 && !onlySchema(call.Args[0], consts)
	case "ExecContext":
		return len(call.Args) > 1 && !onlySchema(call.Args[1], consts)
	case "Query", "QueryRow":
		return len(call.Args) > 0 && changesRows(call.Args[0], consts)
	case "QueryContext", "QueryRowContext":
		return len(call.Args) > 1 && changesRows(call.Args[1], consts)
	}
	return false
}

// sqlCalls run one statement. On a *sql.Conn or *sql.Tx parameter they do
// not count: the opener of that conn or transaction does.
var sqlCalls = map[string]bool{"Exec": true, "ExecContext": true, "Query": true, "QueryRow": true, "QueryContext": true, "QueryRowContext": true}

// changesRows reports whether a query is known to change rows, as an INSERT,
// UPDATE, DELETE or REPLACE with RETURNING does. An unknown query is almost
// always a SELECT, so it does not count.
func changesRows(stmt ast.Expr, consts map[string]string) bool {
	text, ok := stringValue(stmt, consts)
	if !ok {
		return false
	}
	word, _, _ := strings.Cut(strings.TrimSpace(text), " ")
	switch strings.ToUpper(word) {
	case "INSERT", "UPDATE", "DELETE", "REPLACE":
		return true
	}
	return false
}

// onlySchema reports whether a statement is known and changes no rows.
func onlySchema(stmt ast.Expr, consts map[string]string) bool {
	text, ok := stringValue(stmt, consts)
	if !ok {
		return false // unknown statement: count it
	}
	word, _, _ := strings.Cut(strings.TrimSpace(text), " ")
	switch strings.ToUpper(strings.TrimRight(word, ";")) {
	case "CREATE", "DROP", "ALTER", "PRAGMA", "BEGIN", "COMMIT", "ROLLBACK", "SAVEPOINT", "RELEASE", "VACUUM", "ANALYZE", "SELECT":
		return true
	}
	return false
}

// opensForWrite reports whether an os.OpenFile flag expression names a
// write flag. Flags kept in a variable count as a write.
func opensForWrite(flags ast.Expr) bool {
	if _, ok := flags.(*ast.Ident); ok {
		return true
	}
	write := false
	ast.Inspect(flags, func(n ast.Node) bool {
		if sel, ok := n.(*ast.SelectorExpr); ok {
			switch sel.Sel.Name {
			case "O_WRONLY", "O_RDWR", "O_APPEND", "O_CREATE", "O_TRUNC":
				write = true
			}
		}
		return true
	})
	return write
}

func TestDirectWritesFound(t *testing.T) {
	src := []byte(`package p

import (
	"context"
	"database/sql"
	"os"
	"dyna-agent/internal/database"
	"dyna-agent/internal/fsutil"
	st "dyna-agent/internal/store"
)

const insertThing = "INSERT INTO things(a)"

type Store struct{}

func (s *Store) Save(ctx context.Context, db interface{ ExecContext(context.Context, string, ...any) (any, error) }) {
	os.WriteFile("a", nil, 0o600)
	f, _ := os.OpenFile("b", os.O_APPEND|os.O_WRONLY, 0)
	_ = f
	fsutil.WriteFile("c", nil, 0o600, true)
	st.WriteJSON("d", nil, 0o600)
	db.ExecContext(ctx, "  update things SET a=1")
	db.ExecContext(ctx, insertThing+" VALUES(1)")
	func() { os.Rename("e", "f") }()
	os.RemoveAll("g")
}

func (s *Store) dynamic(ctx context.Context, db *sql.DB, flags int, query string) {
	database.Immediate(ctx, db, func(conn *sql.Conn) error {
		_, err := conn.ExecContext(ctx, query)
		return err
	})
	db.Exec("WITH x AS (SELECT 1) DELETE FROM things")
	os.OpenFile("i", flags, 0)
}

func read(ctx context.Context, db interface{ ExecContext(context.Context, string, ...any) (any, error) }) {
	os.OpenFile("g", os.O_RDONLY, 0)
	os.ReadFile("h")
	db.ExecContext(ctx, "SELECT 1")
	db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS t(a)")
}
`)
	got, err := directWrites("p.go", src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"p.go Store.Save": 8, "p.go Store.dynamic": 3}
	if !maps.Equal(got, want) {
		t.Fatalf("found %v; want %v", got, want)
	}
}

// A store that writes through Writer.Write needs no allowlist line, even
// when its SQL sits in conn helpers. The same helper under any other opener
// is counted at that opener, and SQL on a *sql.DB is counted where it runs.
func TestOpenersCountNotStatementsOnAConn(t *testing.T) {
	src := []byte(`package p

import (
	"context"
	"database/sql"
	"dyna-agent/internal/database"
	"dyna-agent/internal/hub"
	"dyna-agent/internal/state"
)

type Store struct{ w *state.Writer }

func (s *Store) Close(ctx context.Context, id string) error {
	return s.w.Write(ctx, hub.Event{Topic: "things", Key: id}, func(conn *sql.Conn) error {
		if _, err := conn.ExecContext(ctx, "INSERT INTO log(id) VALUES(?)", id); err != nil {
			return err
		}
		return deleteThing(ctx, conn, id)
	})
}

func deleteThing(ctx context.Context, conn *sql.Conn, id string) error {
	_, err := conn.ExecContext(ctx, "DELETE FROM things WHERE id=?", id)
	return err
}

func inTx(ctx context.Context, tx *sql.Tx) { tx.Exec("DELETE FROM things") }

func oldClose(ctx context.Context, db *sql.DB, id string) error {
	return database.Immediate(ctx, db, func(conn *sql.Conn) error { return deleteThing(ctx, conn, id) })
}

func ownConn(ctx context.Context, db *sql.DB, id string) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return deleteThing(ctx, conn, id)
}

func onDB(db *sql.DB) { db.Exec("DELETE FROM things") }

func returning(ctx context.Context, db *sql.DB, conn *sql.Conn, query string) {
	db.QueryRowContext(ctx, "INSERT INTO things(a) VALUES(1) RETURNING id")
	db.QueryContext(ctx, "  delete FROM things RETURNING id")
	db.QueryRowContext(ctx, "SELECT id FROM things")
	db.QueryContext(ctx, query)
	conn.QueryRowContext(ctx, "UPDATE things SET a=2 RETURNING id")
}
`)
	got, err := directWrites("p.go", src)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"p.go oldClose": 1, "p.go ownConn": 1, "p.go onDB": 1, "p.go returning": 2}
	if !maps.Equal(got, want) {
		t.Fatalf("found %v; want %v (Store.Close, deleteThing and inTx need no line)", got, want)
	}
}

type allowed struct {
	kind   string // "violation" or "not-state"
	count  int
	reason string
}

func readAllowlist(t *testing.T) map[string]allowed {
	t.Helper()
	f, err := os.Open(allowlistPath)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	list := map[string]allowed{}
	lines := bufio.NewScanner(f)
	for n := 1; lines.Scan(); n++ {
		line, reason, _ := strings.Cut(lines.Text(), "#")
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		count := 0
		if len(fields) == 4 {
			count, _ = strconv.Atoi(fields[3])
		}
		reason = strings.TrimSpace(reason)
		if len(fields) != 4 || count < 1 || (fields[0] != "violation" && fields[0] != "not-state") {
			t.Fatalf("%s:%d: want `violation|not-state <file> <func> <count> [# reason]`", allowlistPath, n)
		}
		if fields[0] == "not-state" && reason == "" {
			t.Fatalf("%s:%d: a not-state line needs a reason after #", allowlistPath, n)
		}
		list[fields[1]+" "+fields[2]] = allowed{fields[0], count, reason}
	}
	if err := lines.Err(); err != nil {
		t.Fatal(err)
	}
	return list
}

// testSupport lists the packages that exist only for tests. Production code
// never imports one (TestOnlyTestsImportTestSupportPackages), so their files
// count as test code. A new one is added here on purpose.
var testSupport = map[string]bool{
	"internal/enginetest":             true,
	"internal/sessionlife/recordtest": true,
	"internal/store/storetest":        true,
	"internal/worktrees/worktreetest": true,
}

// repoRoot is the repository, seen from this package.
var repoRoot = filepath.Join("..", "..")

// walkSource calls fn with every production Go file under root's internal/
// and cmd/, outside this package: not a _test.go file, testdata or a
// test-support package.
func walkSource(t *testing.T, root string, fn func(rel string, src []byte) error) {
	t.Helper()
	for _, top := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if d.IsDir() {
				if d.Name() == "testdata" || rel == "internal/state" || testSupport[rel] {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(rel, ".go") || strings.HasSuffix(rel, "_test.go") {
				return nil
			}
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			return fn(rel, src)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

// scanRepo counts direct writes in every production Go file under root.
func scanRepo(t *testing.T, root string) map[string]int {
	t.Helper()
	found := map[string]int{}
	walkSource(t, root, func(rel string, src []byte) error {
		counts, err := directWrites(rel, src)
		for k, v := range counts {
			found[k] += v
		}
		return err
	})
	return found
}

// TestOnlyTestsImportTestSupportPackages keeps the test-support exemption
// honest: production code that imported such a package would hide its
// writes from the scan.
func TestOnlyTestsImportTestSupportPackages(t *testing.T) {
	for rel := range testSupport {
		if _, err := os.Stat(filepath.Join(repoRoot, rel)); err != nil {
			t.Errorf("test-support package %s: %v", rel, err)
		}
	}
	walkSource(t, repoRoot, func(rel string, src []byte) error {
		file, err := parser.ParseFile(token.NewFileSet(), rel, src, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, spec := range file.Imports {
			path := strings.Trim(spec.Path.Value, `"`)
			if testSupport[strings.TrimPrefix(path, "dyna-agent/")] {
				t.Errorf("%s imports the test-support package %s", rel, path)
			}
		}
		return nil
	})
}

// Only the listed test-support packages are skipped: a production package
// whose name merely ends in "test" is still scanned.
func TestScanSkipsOnlyListedTestSupport(t *testing.T) {
	root := t.TempDir()
	write := `package p

import "os"

func Save() { _ = os.WriteFile("state.json", nil, 0o600) }
`
	if err := os.Mkdir(filepath.Join(root, "cmd"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"internal/latest", "internal/enginetest"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, dir, "p.go"), []byte(write), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	found := scanRepo(t, root)
	if found["internal/latest/p.go Save"] != 1 || len(found) != 1 {
		t.Fatalf("scan = %v, want only internal/latest/p.go Save", found)
	}
}

// TestStateWritesGoThroughHelper fails on a new direct state write, and on
// an allowlist line that is now too high. The list may only shrink.
func TestStateWritesGoThroughHelper(t *testing.T) {
	list := readAllowlist(t)
	found := scanRepo(t, repoRoot)
	for key, n := range found {
		if a, ok := list[key]; !ok || n > a.count {
			t.Errorf("%s writes state directly %d times (allowlist: %d). Write through internal/state, or add a `not-state` line with a reason to %s", key, n, a.count, allowlistPath)
		}
	}
	stale := false
	for key, a := range list {
		if found[key] < a.count {
			stale = true
			if !*update {
				t.Errorf("%s now has %d direct writes, its line says %d: lower or remove the line (go test ./internal/state -run TestStateWritesGoThroughHelper -update)", key, found[key], a.count)
			}
		}
	}
	if stale && *update {
		shrinkAllowlist(t, found)
	}
}

// shrinkAllowlist lowers or removes lines. It never adds or raises one.
func shrinkAllowlist(t *testing.T, found map[string]int) {
	t.Helper()
	data, err := os.ReadFile(allowlistPath)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSuffix(string(data), "\n"), "\n") {
		body, reason, hasReason := strings.Cut(line, "#")
		fields := strings.Fields(body)
		if len(fields) != 4 {
			out = append(out, line)
			continue
		}
		listed, _ := strconv.Atoi(fields[3])
		n := min(found[fields[1]+" "+fields[2]], listed)
		if n == 0 {
			continue
		}
		line = fmt.Sprintf("%s %s %s %d", fields[0], fields[1], fields[2], n)
		if hasReason {
			line += " #" + reason
		}
		out = append(out, line)
	}
	if err := os.WriteFile(allowlistPath, []byte(strings.Join(out, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}
