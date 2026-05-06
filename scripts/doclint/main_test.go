package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// camelToUnderscore
// ---------------------------------------------------------------------------

func TestCamelToUnderscore(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"clientSend", "client_send"},
		{"Send", "send"},
		{"HTTPTransport", "h_t_t_p_transport"},
		{"Client", "client"},
		{"clientGetMembership", "client_get_membership"},
		{"", ""},
		{"alreadylower", "alreadylower"},
		{"A", "a"},
		{"AB", "a_b"},
		{"MyFunc", "my_func"},
	}
	for _, tc := range tests {
		got := camelToUnderscore(tc.in)
		if got != tc.want {
			t.Errorf("camelToUnderscore(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// symbolKey
// ---------------------------------------------------------------------------

func TestSymbolKey(t *testing.T) {
	tests := []struct {
		receiver string
		name     string
		want     string
	}{
		{"", "Send", "send"},
		{"Client", "Send", "client_send"},
		{"client", "Send", "client_send"},
		{"Client", "GetMembership", "client_getmembership"},
		{"", "Init", "init"},
		{"MyType", "Do", "mytype_do"},
	}
	for _, tc := range tests {
		got := symbolKey(tc.receiver, tc.name)
		if got != tc.want {
			t.Errorf("symbolKey(%q, %q) = %q, want %q", tc.receiver, tc.name, got, tc.want)
		}
	}
}

// ---------------------------------------------------------------------------
// helpers to build AST nodes programmatically
// ---------------------------------------------------------------------------

// parseSource parses Go source text and returns all packages.
func parseSource(t *testing.T, src string) map[string]*ast.Package {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, "", func(fi os.FileInfo) bool {
		_ = fi
		return false
	}, parser.ParseComments)
	// ParseDir on empty string will fail; use ParseFile instead.
	_ = pkgs
	_ = err

	f, parseErr := parser.ParseFile(fset, "test.go", src, parser.ParseComments)
	if parseErr != nil {
		t.Fatalf("parse error: %v", parseErr)
	}
	pkgName := f.Name.Name
	return map[string]*ast.Package{
		pkgName: {
			Name:  pkgName,
			Files: map[string]*ast.File{"test.go": f},
		},
	}
}

// parseTestSource parses Go source as a _test.go file.
func parseTestSource(t *testing.T, src string) map[string]*ast.Package {
	t.Helper()
	fset := token.NewFileSet()
	f, parseErr := parser.ParseFile(fset, "test_test.go", src, parser.ParseComments)
	if parseErr != nil {
		t.Fatalf("parse error: %v", parseErr)
	}
	pkgName := f.Name.Name
	return map[string]*ast.Package{
		pkgName: {
			Name:  pkgName,
			Files: map[string]*ast.File{"test_test.go": f},
		},
	}
}

// ---------------------------------------------------------------------------
// collectExamples
// ---------------------------------------------------------------------------

func TestCollectExamples_Empty(t *testing.T) {
	pkgs := parseTestSource(t, `package foo_test`)
	got := collectExamples(pkgs)
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

func TestCollectExamples_ExampleFunc(t *testing.T) {
	src := `package foo_test
func Example_clientSend() {}
`
	pkgs := parseTestSource(t, src)
	got := collectExamples(pkgs)
	// Should contain "clientsend" (lower) and "client_send" (camelToUnderscore)
	if !got["clientsend"] {
		t.Errorf("expected key 'clientsend', got %v", got)
	}
	if !got["client_send"] {
		t.Errorf("expected key 'client_send', got %v", got)
	}
}

func TestCollectExamples_ExampleMethodStyle(t *testing.T) {
	src := `package foo_test
func ExampleClient_Send() {}
`
	pkgs := parseTestSource(t, src)
	got := collectExamples(pkgs)
	// Suffix after "Example" is "Client_Send"
	// lower = "client_send"
	if !got["client_send"] {
		t.Errorf("expected key 'client_send', got %v", got)
	}
}

func TestCollectExamples_ExampleNoSuffix(t *testing.T) {
	src := `package foo_test
func Example() {}
`
	pkgs := parseTestSource(t, src)
	got := collectExamples(pkgs)
	// Suffix is "", lower is ""
	if !got[""] {
		t.Errorf("expected key '', got %v", got)
	}
}

func TestCollectExamples_NonExampleFuncsSkipped(t *testing.T) {
	src := `package foo_test
func TestSomething(t interface{}) {}
func BenchmarkX(b interface{}) {}
`
	pkgs := parseTestSource(t, src)
	got := collectExamples(pkgs)
	if len(got) != 0 {
		t.Errorf("expected empty map, got %v", got)
	}
}

func TestCollectExamples_MultipleExamples(t *testing.T) {
	src := `package foo_test
func Example_clientSend() {}
func ExampleClient_Receive() {}
func Example_init() {}
`
	pkgs := parseTestSource(t, src)
	got := collectExamples(pkgs)
	if !got["clientsend"] {
		t.Errorf("missing 'clientsend'")
	}
	if !got["client_receive"] {
		t.Errorf("missing 'client_receive'")
	}
	if !got["init"] {
		t.Errorf("missing 'init'")
	}
}

// ---------------------------------------------------------------------------
// checkFuncDecl
// ---------------------------------------------------------------------------

func funcDeclFromSource(t *testing.T, src string) *ast.FuncDecl {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "f.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	for _, decl := range f.Decls {
		if fd, ok := decl.(*ast.FuncDecl); ok {
			return fd
		}
	}
	t.Fatal("no FuncDecl found in source")
	return nil
}

func TestCheckFuncDecl_UnexportedSkipped(t *testing.T) {
	src := `package foo
func unexported() {}`
	fd := funcDeclFromSource(t, src)
	got := checkFuncDecl(fd, map[string]bool{}, "foo")
	if got != 0 {
		t.Errorf("expected 0 errors for unexported func, got %d", got)
	}
}

func TestCheckFuncDecl_ExportedMissingDocMissingExample(t *testing.T) {
	src := `package foo
func Exported() {}`
	fd := funcDeclFromSource(t, src)
	got := checkFuncDecl(fd, map[string]bool{}, "foo")
	if got != 2 {
		t.Errorf("expected 2 errors (missing doc + missing example), got %d", got)
	}
}

func TestCheckFuncDecl_ExportedHasDocMissingExample(t *testing.T) {
	src := `package foo
// Exported does something.
func Exported() {}`
	fd := funcDeclFromSource(t, src)
	got := checkFuncDecl(fd, map[string]bool{}, "foo")
	if got != 1 {
		t.Errorf("expected 1 error (missing example only), got %d", got)
	}
}

func TestCheckFuncDecl_ExportedHasDocHasExample(t *testing.T) {
	src := `package foo
// Exported does something.
func Exported() {}`
	fd := funcDeclFromSource(t, src)
	examples := map[string]bool{"exported": true}
	got := checkFuncDecl(fd, examples, "foo")
	if got != 0 {
		t.Errorf("expected 0 errors, got %d", got)
	}
}

func TestCheckFuncDecl_MethodOnExportedType(t *testing.T) {
	src := `package foo
type Client struct{}
// Send sends a message.
func (c *Client) Send() {}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "f.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	var fd *ast.FuncDecl
	for _, decl := range f.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Recv != nil {
			fd = d
		}
	}
	if fd == nil {
		t.Fatal("no method FuncDecl found")
	}

	// Without example
	got := checkFuncDecl(fd, map[string]bool{}, "foo")
	if got != 1 {
		t.Errorf("expected 1 error (missing example), got %d", got)
	}

	// With example via "client_send" key
	examples := map[string]bool{"client_send": true}
	got = checkFuncDecl(fd, examples, "foo")
	if got != 0 {
		t.Errorf("expected 0 errors with example key 'client_send', got %d", got)
	}
}

func TestCheckFuncDecl_MethodOnExportedType_ConcatKey(t *testing.T) {
	src := `package foo
type Client struct{}
// Send sends a message.
func (c *Client) Send() {}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "f.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	var fd *ast.FuncDecl
	for _, decl := range f.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Recv != nil {
			fd = d
		}
	}
	if fd == nil {
		t.Fatal("no method FuncDecl found")
	}

	// With example via "clientsend" (concat) key
	examples := map[string]bool{"clientsend": true}
	got := checkFuncDecl(fd, examples, "foo")
	if got != 0 {
		t.Errorf("expected 0 errors with example key 'clientsend', got %d", got)
	}
}

func TestCheckFuncDecl_MethodOnUnexportedType_Skipped(t *testing.T) {
	src := `package foo
type client struct{}
// Send sends a message.
func (c *client) Send() {}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "f.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	var fd *ast.FuncDecl
	for _, decl := range f.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Recv != nil {
			fd = d
		}
	}
	if fd == nil {
		t.Fatal("no method FuncDecl found")
	}
	got := checkFuncDecl(fd, map[string]bool{}, "foo")
	if got != 0 {
		t.Errorf("expected 0 errors for method on unexported type, got %d", got)
	}
}

func TestCheckFuncDecl_MethodMissingDoc(t *testing.T) {
	src := `package foo
type Client struct{}
func (c *Client) Send() {}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "f.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	var fd *ast.FuncDecl
	for _, decl := range f.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Recv != nil {
			fd = d
		}
	}
	if fd == nil {
		t.Fatal("no method FuncDecl found")
	}
	examples := map[string]bool{"client_send": true}
	got := checkFuncDecl(fd, examples, "foo")
	if got != 1 {
		t.Errorf("expected 1 error (missing doc), got %d", got)
	}
}

func TestCheckFuncDecl_ValueReceiverOnExportedType(t *testing.T) {
	src := `package foo
type Client struct{}
// Do does something.
func (c Client) Do() {}`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "f.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	var fd *ast.FuncDecl
	for _, decl := range f.Decls {
		if d, ok := decl.(*ast.FuncDecl); ok && d.Recv != nil {
			fd = d
		}
	}
	if fd == nil {
		t.Fatal("no method FuncDecl found")
	}
	examples := map[string]bool{"client_do": true}
	got := checkFuncDecl(fd, examples, "foo")
	if got != 0 {
		t.Errorf("expected 0 errors, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// checkGenDecl — type specs
// ---------------------------------------------------------------------------

func genDeclsFromSource(t *testing.T, src string) []*ast.GenDecl {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "g.go", src, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse error: %v", err)
	}
	var decls []*ast.GenDecl
	for _, decl := range f.Decls {
		if gd, ok := decl.(*ast.GenDecl); ok {
			decls = append(decls, gd)
		}
	}
	return decls
}

func TestCheckGenDecl_ExportedTypeMissingDocMissingExample(t *testing.T) {
	src := `package foo
type MyType struct{}`
	decls := genDeclsFromSource(t, src)
	if len(decls) == 0 {
		t.Fatal("no GenDecl found")
	}
	got := checkGenDecl(decls[0], map[string]bool{}, "foo")
	if got != 2 {
		t.Errorf("expected 2 errors, got %d", got)
	}
}

func TestCheckGenDecl_ExportedTypeHasBlockDoc(t *testing.T) {
	src := `package foo
// MyType is something.
type MyType struct{}`
	decls := genDeclsFromSource(t, src)
	if len(decls) == 0 {
		t.Fatal("no GenDecl found")
	}
	// Still missing example
	got := checkGenDecl(decls[0], map[string]bool{}, "foo")
	if got != 1 {
		t.Errorf("expected 1 error (missing example), got %d", got)
	}
}

func TestCheckGenDecl_ExportedTypeHasDocAndExample(t *testing.T) {
	src := `package foo
// MyType is something.
type MyType struct{}`
	decls := genDeclsFromSource(t, src)
	examples := map[string]bool{"mytype": true}
	got := checkGenDecl(decls[0], examples, "foo")
	if got != 0 {
		t.Errorf("expected 0 errors, got %d", got)
	}
}

func TestCheckGenDecl_UnexportedTypeSkipped(t *testing.T) {
	src := `package foo
type myType struct{}`
	decls := genDeclsFromSource(t, src)
	got := checkGenDecl(decls[0], map[string]bool{}, "foo")
	if got != 0 {
		t.Errorf("expected 0 errors for unexported type, got %d", got)
	}
}

func TestCheckGenDecl_ExportedConst_MissingDoc(t *testing.T) {
	src := `package foo
const MyConst = 42`
	decls := genDeclsFromSource(t, src)
	got := checkGenDecl(decls[0], map[string]bool{}, "foo")
	if got != 1 {
		t.Errorf("expected 1 error (missing doc on const), got %d", got)
	}
}

func TestCheckGenDecl_ExportedConst_HasBlockDoc(t *testing.T) {
	src := `package foo
// MyConst is the answer.
const MyConst = 42`
	decls := genDeclsFromSource(t, src)
	got := checkGenDecl(decls[0], map[string]bool{}, "foo")
	if got != 0 {
		t.Errorf("expected 0 errors for const with doc, got %d", got)
	}
}

func TestCheckGenDecl_ExportedVar_MissingDoc(t *testing.T) {
	src := `package foo
var MyVar = "hello"`
	decls := genDeclsFromSource(t, src)
	got := checkGenDecl(decls[0], map[string]bool{}, "foo")
	if got != 1 {
		t.Errorf("expected 1 error (missing doc on var), got %d", got)
	}
}

func TestCheckGenDecl_ExportedVar_HasDoc(t *testing.T) {
	src := `package foo
// MyVar is a variable.
var MyVar = "hello"`
	decls := genDeclsFromSource(t, src)
	got := checkGenDecl(decls[0], map[string]bool{}, "foo")
	if got != 0 {
		t.Errorf("expected 0 errors for var with doc, got %d", got)
	}
}

func TestCheckGenDecl_UnexportedConst_Skipped(t *testing.T) {
	src := `package foo
const myConst = 42`
	decls := genDeclsFromSource(t, src)
	got := checkGenDecl(decls[0], map[string]bool{}, "foo")
	if got != 0 {
		t.Errorf("expected 0 errors for unexported const, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// checkPackage — filesystem-level integration
// ---------------------------------------------------------------------------

// writeTempDir creates a temporary directory with given files.
func writeTempDir(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	return dir
}

func TestCheckPackage_InvalidDir(t *testing.T) {
	got := checkPackage("/tmp/does-not-exist-doclint-test-xyz")
	if got != 1 {
		t.Errorf("expected 1 error for invalid dir, got %d", got)
	}
}

func TestCheckPackage_EmptyPackage(t *testing.T) {
	dir := writeTempDir(t, map[string]string{
		"doc.go": `package mypkg`,
	})
	got := checkPackage(dir)
	if got != 0 {
		t.Errorf("expected 0 errors for package with no exported symbols, got %d", got)
	}
}

func TestCheckPackage_ExportedFuncAllGood(t *testing.T) {
	dir := writeTempDir(t, map[string]string{
		"doc.go": `package mypkg

// Greet says hello.
func Greet() string { return "hello" }
`,
		"doc_test.go": `package mypkg

func Example_greet() { _ = Greet() }
`,
	})
	got := checkPackage(dir)
	if got != 0 {
		t.Errorf("expected 0 errors for fully documented package, got %d", got)
	}
}

func TestCheckPackage_ExportedFuncMissingExample(t *testing.T) {
	dir := writeTempDir(t, map[string]string{
		"doc.go": `package mypkg

// Greet says hello.
func Greet() string { return "hello" }
`,
	})
	got := checkPackage(dir)
	if got < 1 {
		t.Errorf("expected >=1 error for func missing example, got %d", got)
	}
}

func TestCheckPackage_ExportedFuncMissingDoc(t *testing.T) {
	dir := writeTempDir(t, map[string]string{
		"doc.go": `package mypkg

func Greet() string { return "hello" }
`,
		"doc_test.go": `package mypkg

func Example_greet() { _ = Greet() }
`,
	})
	got := checkPackage(dir)
	if got < 1 {
		t.Errorf("expected >=1 error for func missing doc, got %d", got)
	}
}

func TestCheckPackage_ExportedTypeMissingBoth(t *testing.T) {
	dir := writeTempDir(t, map[string]string{
		"doc.go": `package mypkg

type Widget struct{}
`,
	})
	got := checkPackage(dir)
	if got < 2 {
		t.Errorf("expected >=2 errors (missing doc + example), got %d", got)
	}
}

func TestCheckPackage_ExportedTypeWithDocAndExample(t *testing.T) {
	dir := writeTempDir(t, map[string]string{
		"doc.go": `package mypkg

// Widget is a UI element.
type Widget struct{}
`,
		"doc_test.go": `package mypkg

func Example_widget() {}
`,
	})
	got := checkPackage(dir)
	if got != 0 {
		t.Errorf("expected 0 errors, got %d", got)
	}
}

func TestCheckPackage_MethodOnExportedType(t *testing.T) {
	dir := writeTempDir(t, map[string]string{
		"doc.go": `package mypkg

// Client is the main API.
type Client struct{}

// Do performs an action.
func (c *Client) Do() {}
`,
		"doc_test.go": `package mypkg

func Example_client() {}
func ExampleClient_Do() {}
`,
	})
	got := checkPackage(dir)
	if got != 0 {
		t.Errorf("expected 0 errors for method with doc and example, got %d", got)
	}
}

func TestCheckPackage_MethodOnExportedType_MissingMethodExample(t *testing.T) {
	dir := writeTempDir(t, map[string]string{
		"doc.go": `package mypkg

// Client is the main API.
type Client struct{}

// Do performs an action.
func (c *Client) Do() {}
`,
		"doc_test.go": `package mypkg

func Example_client() {}
`,
	})
	got := checkPackage(dir)
	if got < 1 {
		t.Errorf("expected >=1 error for missing method example, got %d", got)
	}
}

func TestCheckPackage_UnexportedSymbolsIgnored(t *testing.T) {
	dir := writeTempDir(t, map[string]string{
		"doc.go": `package mypkg

type internal struct{}
func helper() {}
const privateConst = 1
var privateVar = "x"
`,
	})
	got := checkPackage(dir)
	if got != 0 {
		t.Errorf("expected 0 errors for package with only unexported symbols, got %d", got)
	}
}

func TestCheckPackage_MultipleExportedFuncs(t *testing.T) {
	dir := writeTempDir(t, map[string]string{
		"doc.go": `package mypkg

// Alpha does alpha.
func Alpha() {}

// Beta does beta.
func Beta() {}
`,
		"doc_test.go": `package mypkg

func Example_alpha() {}
func Example_beta() {}
`,
	})
	got := checkPackage(dir)
	if got != 0 {
		t.Errorf("expected 0 errors, got %d", got)
	}
}

func TestCheckPackage_MixedExportedExamples_MethodStyle(t *testing.T) {
	dir := writeTempDir(t, map[string]string{
		"doc.go": `package mypkg

// Client handles requests.
type Client struct{}

// Send sends a payload.
func (c *Client) Send() {}
`,
		"doc_test.go": `package mypkg

func Example_client() {}
func Example_clientSend() {}
`,
	})
	got := checkPackage(dir)
	if got != 0 {
		t.Errorf("expected 0 errors, got %d", got)
	}
}

// TestCheckPackage_ExportedConstWithDoc verifies constants with docs pass.
func TestCheckPackage_ExportedConstWithDoc(t *testing.T) {
	dir := writeTempDir(t, map[string]string{
		"doc.go": `package mypkg

// StatusOK is the success status.
const StatusOK = 0
`,
	})
	got := checkPackage(dir)
	if got != 0 {
		t.Errorf("expected 0 errors for const with doc, got %d", got)
	}
}

// TestCheckPackage_MultipleConstsMissingDoc checks a group const without doc.
func TestCheckPackage_MultipleConstsMissingDoc(t *testing.T) {
	dir := writeTempDir(t, map[string]string{
		"doc.go": `package mypkg

const (
	StatusOK  = 0
	StatusErr = 1
)
`,
	})
	// Both exported consts lack a block-level doc → 2 errors
	got := checkPackage(dir)
	if got < 2 {
		t.Errorf("expected >=2 errors for 2 exported consts missing docs, got %d", got)
	}
}

// Verify that the output includes recognisable content on missing symbols.
func TestCheckPackage_OutputContainsMissing(t *testing.T) {
	dir := writeTempDir(t, map[string]string{
		"doc.go": `package mypkg

func Greet() string { return "hello" }
`,
	})
	// We can't easily capture stdout here, but we confirm the error count.
	got := checkPackage(dir)
	if got < 1 {
		t.Errorf("expected errors, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// collectExamples — camelCase edge cases
// ---------------------------------------------------------------------------

func TestCollectExamples_PascalCaseSuffix(t *testing.T) {
	src := `package foo_test
func ExampleMyFunc() {}
`
	pkgs := parseTestSource(t, src)
	got := collectExamples(pkgs)
	// Suffix = "MyFunc", lower = "myfunc", camelToUnderscore = "my_func"
	if !got["myfunc"] {
		t.Errorf("expected 'myfunc', got keys: %v", keysOf(got))
	}
	if !got["my_func"] {
		t.Errorf("expected 'my_func', got keys: %v", keysOf(got))
	}
}

func TestCollectExamples_UnderscorePrefixSuffix(t *testing.T) {
	src := `package foo_test
func Example_MyFunc() {}
`
	pkgs := parseTestSource(t, src)
	got := collectExamples(pkgs)
	// "Example_" → suffix after TrimPrefix "Example" is "_MyFunc", then TrimPrefix "_" → "MyFunc"
	if !got["myfunc"] {
		t.Errorf("expected 'myfunc', got keys: %v", keysOf(got))
	}
}

func keysOf(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}

// ---------------------------------------------------------------------------
// symbolKey — edge cases
// ---------------------------------------------------------------------------

func TestSymbolKey_AllLower(t *testing.T) {
	got := symbolKey("", "send")
	if got != "send" {
		t.Errorf("expected 'send', got %q", got)
	}
}

func TestSymbolKey_ReceiverAndMethod_Lowercased(t *testing.T) {
	got := symbolKey("CLIENT", "SEND")
	want := "client_send"
	if got != want {
		t.Errorf("expected %q, got %q", want, got)
	}
}

// ---------------------------------------------------------------------------
// checkFuncDecl — nil name guard
// ---------------------------------------------------------------------------

func TestCheckFuncDecl_NilNameSkipped(t *testing.T) {
	fd := &ast.FuncDecl{Name: nil}
	got := checkFuncDecl(fd, map[string]bool{}, "foo")
	if got != 0 {
		t.Errorf("expected 0 for nil-name FuncDecl, got %d", got)
	}
}

// ---------------------------------------------------------------------------
// camelToUnderscore — rune boundary
// ---------------------------------------------------------------------------

func TestCamelToUnderscore_AlreadyUnderscore(t *testing.T) {
	// If the previous rune is already '_', don't insert another
	got := camelToUnderscore("my_Func")
	// 'F' follows '_', no extra underscore should be added before 'F'
	if strings.Contains(got, "__") {
		t.Errorf("unexpected double underscore in %q", got)
	}
}
