package config

// Configuration sync guard.
//
// The project keeps a 4-place configuration contract that has drifted twice
// in the past (see AGENTS.md, "RU texts + bilingual docs sync" and the
// .env.example / config/config.go 4-place sync rule):
//
//	config/config.go env tags  <->  .env.example  <->  README.md  <->  README_RU.md
//
// These tests automate that contract:
//
//  1. CODE -> ENVEX (hard gate): every env:"NAME" struct tag in
//     config/config.go must have a matching variable line (active or
//     commented out) in .env.example.
//  2. CODE -> READMEs: every CODE variable name must appear verbatim in
//     README.md and README_RU.md, minus an explicit in-test allowlist.
//  3. ENVEX -> CODE (reverse gate): every variable declared in
//     .env.example must be a kong env tag in config.go, minus an explicit
//     allowlist for variables consumed outside the kong config.
//
// Guard sensitivity (RED-proof) is demonstrated by unit tests below that
// run the parsing/diff logic over inline fixtures; the repo-level gates
// stay green as long as the current verified state holds.

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// Allowlists (explicit, each entry carries a justification comment).
// ---------------------------------------------------------------------------

// configSyncReadmeAllowlist lists CODE env variables that are intentionally
// not verbatim in the READMEs (prose-only aliases, command-only knobs, ...).
//
// Seeded EMPTY on 2026-11-18 after grep verification of the current state:
// the three knobs that were historically prose-documented during the wave-3
// audit — TELEGRAM_ADMIN_USER_IDS, TELEGRAM_RELEASE_ALERTS and
// TELEGRAM_RELEASE_CHECK_INTERVAL_HOURS — now appear verbatim (backticked,
// in prose) in BOTH README.md and README_RU.md, so they pass the plain
// string search without an exception. Add entries here ONLY together with a
// justification comment; every entry is a documentation gap accepted by
// review.
var configSyncReadmeAllowlist = map[string]string{}

// configSyncEnvProxyAllowlist lists variables declared in .env.example that
// are NOT kong env tags in config.go, with the reason each is legitimate.
//
// REPORT_URL / REPORT_TOKEN were audited against the kong tags on
// 2026-11-18 and are NOT aliased (config.go carries env:"REPORT_URL" /
// env:"REPORT_TOKEN" directly), so they are intentionally NOT allowlisted:
// if the kong tag ever drifts, this gate must fail.
var configSyncEnvProxyAllowlist = map[string]string{
	"HTTP_PROXY":      "standard Go net/http ProxyFromEnvironment variable; documented in .env.example for outbound proxy support, deliberately not a kong tag",
	"HTTPS_PROXY":     "standard Go net/http ProxyFromEnvironment variable; documented in .env.example for outbound proxy support, deliberately not a kong tag",
	"ALL_PROXY":       "standard Go net/http ProxyFromEnvironment variable; documented in .env.example for outbound proxy support, deliberately not a kong tag",
	"NO_PROXY":        "standard Go net/http ProxyFromEnvironment variable; documented in .env.example for outbound proxy support, deliberately not a kong tag",
	"BOOTSTRAP_PROXY": "bootstrap proxy used only for subscription/geo downloads on restricted hosts; consumed via os.Getenv (config/proxy_env.go, subscription/fetcher.go), not a kong tag",
	"GEO_PROXY":       "geo-download proxy alias consumed via os.Getenv (config/proxy_env.go lists BOOTSTRAP_PROXY/bootstrap_proxy/GEO_PROXY/geo_proxy); not present in .env.example as of 2026-11-18, allowlisted so a future reintroduction cannot silently break the reverse gate",
}

// ---------------------------------------------------------------------------
// Repo-root location (no CWD assumptions: Windows dev shell, Linux CI).
// ---------------------------------------------------------------------------

// configSyncLocateRepoRoot resolves the repository root from this test
// file's own compile-time location via runtime.Caller, never from the
// process working directory, so the guard behaves identically no matter
// where `go test` was invoked from.
func configSyncLocateRepoRoot(t *testing.T) string {
	t.Helper()
	root := ""
	if r, ok := configSyncRootFromCaller(); ok {
		root = r
	} else if wd, err := os.Getwd(); err == nil {
		// Fallback: `go test` normally runs the test binary with the CWD set
		// to the package directory (<root>/config).
		cand := filepath.Dir(filepath.Dir(wd))
		if configSyncIsRepoRoot(cand) {
			root = cand
		}
	}
	if root == "" {
		t.Fatal("config sync guard: could not locate repository root from runtime.Caller or CWD")
	}
	return root
}

func configSyncRootFromCaller() (string, bool) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok || thisFile == "" {
		return "", false
	}
	abs, err := filepath.Abs(thisFile)
	if err != nil {
		return "", false
	}
	// This file lives at <root>/config/config_sync_test.go -> two Dir() steps.
	root := filepath.Dir(filepath.Dir(abs))
	if configSyncIsRepoRoot(root) {
		return root, true
	}
	return "", false
}

// configSyncIsRepoRoot validates a candidate root against the exact set of
// files this guard consumes plus the module file, so a wrong guess fails
// loudly instead of parsing unrelated files.
func configSyncIsRepoRoot(dir string) bool {
	if dir == "" || dir == "." || dir == string(filepath.Separator) {
		return false
	}
	markers := []string{
		filepath.Join("config", "config.go"),
		".env.example",
		"README.md",
		"README_RU.md",
		"go.mod",
	}
	for _, m := range markers {
		if _, err := os.Stat(filepath.Join(dir, m)); err != nil {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// Parsers.
// ---------------------------------------------------------------------------

// configSyncParseCodeEnvTags parses Go source (config/config.go) and
// collects every struct field tag env:"NAME" into name -> diagnostic
// location. Uses go/parser + ast, so tags inside strings, comments or
// non-struct contexts are ignored.
func configSyncParseCodeEnvTags(t *testing.T, filename, src string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filename, src, 0)
	if err != nil {
		t.Fatalf("config sync guard: cannot parse %s: %v", filename, err)
	}
	envs := map[string]string{}
	ast.Inspect(file, func(n ast.Node) bool {
		st, ok := n.(*ast.StructType)
		if !ok {
			return true
		}
		for _, field := range st.Fields.List {
			if field.Tag == nil {
				continue
			}
			// ast keeps the tag literal including the surrounding backticks;
			// reflect.StructTag expects the raw tag content.
			tag := reflect.StructTag(strings.Trim(field.Tag.Value, "`"))
			name := tag.Get("env")
			if name == "" {
				continue
			}
			pos := fset.Position(field.Pos())
			where := fmt.Sprintf("%s:%d", filepath.Base(pos.Filename), pos.Line)
			if len(field.Names) == 0 {
				where += " (embedded field)"
			} else {
				names := make([]string, 0, len(field.Names))
				for _, id := range field.Names {
					names = append(names, id.Name)
				}
				where += " (field " + strings.Join(names, ",") + ")"
			}
			if _, seen := envs[name]; !seen {
				envs[name] = where
			}
		}
		return true
	})
	if len(envs) == 0 {
		t.Fatalf("config sync guard: parsed %s but found no env:\"...\" tags — parser or file changed?", filename)
	}
	return envs
}

// configSyncEnvExampleVarRe matches variable declarations in .env.example,
// both active (`VAR=value`) and commented out (`# VAR=value`).
// The variable name must be a single uppercase token immediately followed
// by '='; prose lines (even when they mention an uppercase word) do not
// match because the token must start at line begin (after an optional '#').
var configSyncEnvExampleVarRe = regexp.MustCompile(`^#?[ \t]*([A-Z][A-Z0-9_]+)=`)

// configSyncParseEnvExample collects variable names declared in
// .env.example content (name -> line reference).
func configSyncParseEnvExample(content string) map[string]string {
	vars := map[string]string{}
	content = strings.TrimPrefix(content, "\uFEFF") // tolerate a UTF-8 BOM
	for i, line := range strings.Split(content, "\n") {
		line = strings.TrimSuffix(line, "\r") // tolerate CRLF checkouts
		if m := configSyncEnvExampleVarRe.FindStringSubmatch(line); m != nil {
			if _, seen := vars[m[1]]; !seen {
				vars[m[1]] = fmt.Sprintf(".env.example:%d", i+1)
			}
		}
	}
	return vars
}

// ---------------------------------------------------------------------------
// Diff helpers (pure, unit-testable).
// ---------------------------------------------------------------------------

// configSyncSortedNames returns the sorted keys of a name -> location map.
func configSyncSortedNames(m map[string]string) []string {
	names := make([]string, 0, len(m))
	for name := range m {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// configSyncReadmeGaps returns CODE env variable names that appear nowhere
// in the given README content and are not covered by allowlist.
func configSyncReadmeGaps(envNames map[string]string, readme string, allowlist map[string]string) []string {
	var missing []string
	for _, name := range configSyncSortedNames(envNames) {
		if _, ok := allowlist[name]; ok {
			continue
		}
		if !strings.Contains(readme, name) {
			missing = append(missing, name)
		}
	}
	return missing
}

// configSyncEnvExampleGaps returns ENVEX variable names that have no kong
// env tag in CODE and are not covered by allowlist.
func configSyncEnvExampleGaps(envex, code map[string]string, allowlist map[string]string) []string {
	var extra []string
	for _, name := range configSyncSortedNames(envex) {
		if _, ok := allowlist[name]; ok {
			continue
		}
		if _, ok := code[name]; !ok {
			extra = append(extra, name)
		}
	}
	return extra
}

// ---------------------------------------------------------------------------
// Repo-level gates (the actual drift detectors).
// ---------------------------------------------------------------------------

// TestConfigSyncEnvExampleCoversAllCodeEnvVars is the hard gate
// (direction CODE -> ENVEX): this direction had real drift historically.
func TestConfigSyncEnvExampleCoversAllCodeEnvVars(t *testing.T) {
	root := configSyncLocateRepoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "config", "config.go"))
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	envex, err := os.ReadFile(filepath.Join(root, ".env.example"))
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}

	code := configSyncParseCodeEnvTags(t, "config.go", string(src))
	envexVars := configSyncParseEnvExample(string(envex))

	var missing []string
	for _, name := range configSyncSortedNames(code) {
		if _, ok := envexVars[name]; !ok {
			missing = append(missing, fmt.Sprintf("%s (%s)", name, code[name]))
		}
	}
	if len(missing) > 0 {
		t.Errorf("config.go declares env vars missing from .env.example (the 4-place config sync contract requires every env tag to have an annotated .env.example entry):\n  %s",
			strings.Join(missing, "\n  "))
	}
}

// TestConfigSyncReadmesCoverAllCodeEnvVars is the README direction of the
// contract: every CODE env variable must be mentioned in both README.md
// and README_RU.md (bilingual docs sync).
func TestConfigSyncReadmesCoverAllCodeEnvVars(t *testing.T) {
	root := configSyncLocateRepoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "config", "config.go"))
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	code := configSyncParseCodeEnvTags(t, "config.go", string(src))

	for _, doc := range []struct{ file, label string }{
		{"README.md", "README.md (English)"},
		{"README_RU.md", "README_RU.md (Russian)"},
	} {
		t.Run(doc.label, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join(root, doc.file))
			if err != nil {
				t.Fatalf("read %s: %v", doc.file, err)
			}
			missing := configSyncReadmeGaps(code, string(raw), configSyncReadmeAllowlist)
			if len(missing) > 0 {
				t.Errorf("%s does not mention the following config.go env vars (bilingual docs sync contract; allowlist entries only with justification):\n  %s",
					doc.file, strings.Join(missing, "\n  "))
			}
		})
	}
}

// TestConfigSyncEnvExampleVarsExistInCode is the reverse gate
// (ENVEX -> CODE): every variable documented in .env.example must actually
// be consumed by the code, either as a kong env tag or via the explicit
// standard-proxy/bootstrap allowlist.
func TestConfigSyncEnvExampleVarsExistInCode(t *testing.T) {
	root := configSyncLocateRepoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "config", "config.go"))
	if err != nil {
		t.Fatalf("read config.go: %v", err)
	}
	envex, err := os.ReadFile(filepath.Join(root, ".env.example"))
	if err != nil {
		t.Fatalf("read .env.example: %v", err)
	}

	code := configSyncParseCodeEnvTags(t, "config.go", string(src))
	envexVars := configSyncParseEnvExample(string(envex))

	extra := configSyncEnvExampleGaps(envexVars, code, configSyncEnvProxyAllowlist)
	if len(extra) > 0 {
		var detail []string
		for _, name := range extra {
			detail = append(detail, fmt.Sprintf("%s (%s)", name, envexVars[name]))
		}
		t.Errorf(".env.example declares vars that are neither kong env tags in config.go nor in the allowlist (documented but dead knobs):\n  %s",
			strings.Join(detail, "\n  "))
	}
}

// ---------------------------------------------------------------------------
// Guard sensitivity (RED-proof): the guard logic itself is exercised over
// inline fixtures so a future regression in the guard cannot pass silently
// as "still green".
// ---------------------------------------------------------------------------

func TestConfigSyncGuardDetectsCodeVarMissingFromEnvExample(t *testing.T) {
	fixture := "package fixture\n\n" +
		"type CLI struct {\n" +
		"\tExisting string `help:\"ok\" env:\"EXISTING_VAR\"`\n" +
		"\tNewcomer bool   `help:\"new\" env:\"NEWCOMER_VAR\"`\n" +
		"\tNoTag    string\n" +
		"}\n"
	code := configSyncParseCodeEnvTags(t, "fixture.go", fixture)
	if len(code) != 2 {
		t.Fatalf("expected 2 env tags in fixture, got %d: %v", len(code), code)
	}
	envex := configSyncParseEnvExample("EXISTING_VAR=1\nGHOST_VAR=2\n")
	if len(envex) != 2 {
		t.Fatalf("expected 2 env vars in fixture .env.example, got %d: %v", len(envex), envex)
	}
	// Reverse gate: GHOST_VAR is documented in .env.example but consumed nowhere.
	gaps := configSyncEnvExampleGaps(envex, code, map[string]string{})
	if len(gaps) != 1 || gaps[0] != "GHOST_VAR" {
		t.Fatalf("expected reverse gate to flag only GHOST_VAR, got %v", gaps)
	}
	// Hard gate direction: NEWCOMER_VAR is declared in code but missing from
	// .env.example.
	var missing []string
	for _, name := range configSyncSortedNames(code) {
		if _, ok := envex[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) != 1 || missing[0] != "NEWCOMER_VAR" {
		t.Fatalf("expected hard gate to flag only NEWCOMER_VAR, got %v", missing)
	}
}

func TestConfigSyncGuardEnvExampleRegex(t *testing.T) {
	content := "" +
		"ACTIVE_VAR=1\n" +
		"# COMMENTED_VAR=2\n" +
		"#\tINDENTED_COMMENTED_VAR=3\n" +
		"  SPACED_VAR=4\n" +
		// Prose lines that must NOT be picked up:
		"# SSRF protection: http(s) subscription URLs are rejected\n" +
		"# Docker Compose: do NOT set 0 here\n" +
		"#   ip      — fetch PROXY_IP_CHECK_URL through the proxy\n" +
		"# (default: true)\n" +
		"# Bare metal: set METRICS_PORT=0 to disable the HTTP server\n" +
		"lowercase_var=5\n" +
		"# 123_DIGITS=6\n"
	vars := configSyncParseEnvExample(content)
	want := []string{"ACTIVE_VAR", "COMMENTED_VAR", "INDENTED_COMMENTED_VAR", "SPACED_VAR"}
	if got := configSyncSortedNames(vars); len(got) != len(want) {
		t.Fatalf("env.example parser matched wrong set: got %v, want %v", got, want)
	}
	for _, w := range want {
		if _, ok := vars[w]; !ok {
			t.Errorf("env.example parser missed %q", w)
		}
	}
	for _, bad := range []string{"PROXY_IP_CHECK_URL", "METRICS_PORT", "SSRF", "Docker"} {
		if _, ok := vars[bad]; ok {
			t.Errorf("env.example parser falsely matched prose token %q", bad)
		}
	}
	// CRLF tolerance.
	if vars := configSyncParseEnvExample("A_VAR=1\r\nB_VAR=2\r\n"); len(vars) != 2 {
		t.Errorf("CRLF content parsed as %v, want A_VAR and B_VAR", vars)
	}
}

func TestConfigSyncGuardReadmeGapDetection(t *testing.T) {
	envNames := map[string]string{
		"DOCUMENTED_VAR": "config.go:1",
		"UNDOCUMENTED":   "config.go:2",
		"ALLOWLISTED":    "config.go:3",
	}
	readme := "usage: set DOCUMENTED_VAR=1 before starting; see also ALLOWLISTED prose"
	gaps := configSyncReadmeGaps(envNames, readme, map[string]string{"ALLOWLISTED": "justified"})
	if len(gaps) != 1 || gaps[0] != "UNDOCUMENTED" {
		t.Fatalf("expected readme guard to flag only UNDOCUMENTED, got %v", gaps)
	}
	// An allowlist entry that is actually present in the README is a no-op.
	gaps = configSyncReadmeGaps(envNames, readme, map[string]string{"UNDOCUMENTED": "why"})
	if len(gaps) != 0 {
		t.Fatalf("allowlisting should silence UNDOCUMENTED, got %v", gaps)
	}
}
