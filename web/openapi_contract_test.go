package web

// OpenAPI <-> Go struct contract guard.
//
// web/openapi.yaml documents the JSON wire shapes of the node reporting
// protocol. The authoritative shapes are the Go structs in the nodes and
// checker packages. This guard fails when the schema and the code drift
// apart:
//
//	components.schemas.*  <->  nodes.ReportPayload / nodes.ReportProxy /
//	                           nodes.IngestResponse / nodes.NodeConfigSync /
//	                           checker.NodeHealth / checker.TargetDiagResult /
//	                           checker.CheckHostSummary /
//	                           checker.CheckHostNodeResult
//
// Contract semantics (deliberately minimal and documented):
//
//  1. every json field WITHOUT omitempty must appear as a schema property
//     (the master/node contract cannot silently lose a mandatory field);
//  2. every schema property must correspond to a json field of the struct
//     (the schema cannot document a field the code never sends);
//  3. omitempty json fields MAY be absent from the schema: they are
//     optional wire additions. required-lists / omitempty strictness are
//     out of scope for this guard (see wave-N-C plan).
//
// Guard sensitivity (RED-proof) is demonstrated by unit tests over inline
// fixtures; the repo-level gate stays green while openapi.yaml and the
// structs agree.
//
// YAML parsing: gopkg.in/yaml.v3 is already part of the module graph as an
// indirect dependency (required by telego/gocron); importing it from a test
// does not require touching go.mod. Only property names are extracted, so
// the minimal model below is sufficient; CI never runs `go mod tidy`.

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"xray-checker/checker"
	"xray-checker/nodes"
)

// openAPIContractSchemas maps every guarded OpenAPI component schema name
// to the Go struct actually serialized on the wire.
var openAPIContractSchemas = map[string]reflect.Type{
	"ReportPayload":       reflect.TypeOf(nodes.ReportPayload{}),
	"ReportProxy":         reflect.TypeOf(nodes.ReportProxy{}),
	"ReportNodeHealth":    reflect.TypeOf(checker.NodeHealth{}),
	"TargetDiagResult":    reflect.TypeOf(checker.TargetDiagResult{}),
	"CheckHostSummary":    reflect.TypeOf(checker.CheckHostSummary{}),
	"CheckHostNodeResult": reflect.TypeOf(checker.CheckHostNodeResult{}),
	"IngestResponse":      reflect.TypeOf(nodes.IngestResponse{}),
	"NodeConfigSync":      reflect.TypeOf(nodes.NodeConfigSync{}),
}

// openAPIContractDoc is a minimal OpenAPI 3 model: only component schema
// property names are needed; types, enums, examples, $refs and required
// lists are intentionally not modeled.
type openAPIContractDoc struct {
	Components struct {
		Schemas map[string]openAPIContractSchema `yaml:"schemas"`
	} `yaml:"components"`
}

type openAPIContractSchema struct {
	Properties map[string]openAPIContractSchema `yaml:"properties"`
}

// openAPIJSONField is one JSON-serialized struct field.
type openAPIJSONField struct {
	name      string
	omitempty bool
}

// openAPIStructJSONFields extracts json tag names from a struct type using
// the same rules encoding/json applies (tag name, "-" exclusion, embedded
// struct flattening including pointer-embedded structs, default field name
// when no tag is set). None of the currently guarded structs use embedding;
// the handling exists so a future embedded field cannot silently bypass the
// contract.
func openAPIStructJSONFields(t reflect.Type) []openAPIJSONField {
	fields := make([]openAPIJSONField, 0, t.NumField())
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		// encoding/json ignores unexported fields, except that unexported
		// embedded struct types still have their exported fields promoted.
		if !f.IsExported() && !f.Anonymous {
			continue
		}
		tag := f.Tag.Get("json")
		if tag == "-" {
			continue
		}
		if f.Anonymous && strings.Split(tag, ",")[0] == "" {
			// Untagged embedded field: encoding/json promotes the embedded
			// struct's (or pointer-to-struct's) fields. Embedded non-struct
			// types are encoded under the type name; unexported embedded
			// non-struct types are ignored.
			ft := f.Type
			if ft.Kind() == reflect.Pointer && ft.Elem().Kind() == reflect.Struct {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct {
				fields = append(fields, openAPIStructJSONFields(ft)...)
			} else if f.IsExported() {
				fields = append(fields, openAPIJSONField{name: f.Name})
			}
			continue
		}
		parts := strings.Split(tag, ",")
		name := parts[0]
		if name == "" {
			name = f.Name // encoding/json default
		}
		omitempty := false
		for _, opt := range parts[1:] {
			if opt == "omitempty" {
				omitempty = true
			}
		}
		fields = append(fields, openAPIJSONField{name: name, omitempty: omitempty})
	}
	return fields
}

// openAPIContractDiff implements the contract semantics documented at the
// top of this file and returns both mismatch directions.
func openAPIContractDiff(fields []openAPIJSONField, schemaProps []string) (missingInSchema, unknownInSchema []string) {
	propSet := make(map[string]bool, len(schemaProps))
	for _, p := range schemaProps {
		propSet[p] = true
	}
	fieldNames := make(map[string]bool, len(fields))
	for _, f := range fields {
		fieldNames[f.name] = true
		if !f.omitempty && !propSet[f.name] {
			missingInSchema = append(missingInSchema, f.name)
		}
	}
	for _, p := range schemaProps {
		if !fieldNames[p] {
			unknownInSchema = append(unknownInSchema, p)
		}
	}
	sort.Strings(missingInSchema)
	sort.Strings(unknownInSchema)
	return missingInSchema, unknownInSchema
}

// openAPIContractSpecPath locates web/openapi.yaml from this test file's
// compile-time location (runtime.Caller), never from the process working
// directory, so the guard behaves identically on a Windows dev shell and
// Linux CI.
func openAPIContractSpecPath(t *testing.T) string {
	t.Helper()
	candidates := []string{}
	if _, thisFile, _, ok := runtime.Caller(0); ok && thisFile != "" {
		if abs, err := filepath.Abs(thisFile); err == nil {
			candidates = append(candidates, filepath.Join(filepath.Dir(abs), "openapi.yaml"))
		}
	}
	if wd, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(wd, "openapi.yaml"))
	}
	for _, cand := range candidates {
		if info, err := os.Stat(cand); err == nil && !info.IsDir() {
			return cand
		}
	}
	t.Fatalf("openapi contract guard: could not locate web/openapi.yaml (tried %v)", candidates)
	return ""
}

// TestOpenAPIContractSchemasMatchStructs is the repo-level gate.
func TestOpenAPIContractSchemasMatchStructs(t *testing.T) {
	specPath := openAPIContractSpecPath(t)
	raw, err := os.ReadFile(specPath)
	if err != nil {
		t.Fatalf("read %s: %v", specPath, err)
	}
	var doc openAPIContractDoc
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", specPath, err)
	}
	if len(doc.Components.Schemas) == 0 {
		t.Fatalf("%s parsed but components.schemas is empty — the YAML model in this guard may no longer match the document layout", specPath)
	}

	// The guard itself must not silently shrink: every mapped schema must
	// still exist in the document.
	names := make([]string, 0, len(openAPIContractSchemas))
	for name := range openAPIContractSchemas {
		names = append(names, name)
		if _, ok := doc.Components.Schemas[name]; !ok {
			t.Errorf("openapi.yaml is missing components.schemas.%s required by the contract guard", name)
		}
	}
	sort.Strings(names)

	for _, name := range names {
		typ := openAPIContractSchemas[name]
		schema := doc.Components.Schemas[name]
		props := make([]string, 0, len(schema.Properties))
		for p := range schema.Properties {
			props = append(props, p)
		}
		sort.Strings(props)
		fields := openAPIStructJSONFields(typ)

		missing, unknown := openAPIContractDiff(fields, props)
		if len(missing) > 0 {
			t.Errorf("contract drift %s <-> %s: non-omitempty json fields missing from openapi.yaml properties: %s",
				name, typ.String(), strings.Join(missing, ", "))
		}
		if len(unknown) > 0 {
			t.Errorf("contract drift %s <-> %s: openapi.yaml properties with no matching json field: %s",
				name, typ.String(), strings.Join(unknown, ", "))
		}
	}
}

// ---------------------------------------------------------------------------
// Guard sensitivity (RED-proof) over inline fixtures.
// ---------------------------------------------------------------------------

func TestOpenAPIContractGuardSensitivity(t *testing.T) {
	t.Run("detects non-omitempty field missing from schema", func(t *testing.T) {
		fields := []openAPIJSONField{
			{name: "present", omitempty: false},
			{name: "absent", omitempty: false},
		}
		missing, unknown := openAPIContractDiff(fields, []string{"present"})
		if len(missing) != 1 || missing[0] != "absent" {
			t.Fatalf("expected missing=[absent], got %v", missing)
		}
		if len(unknown) != 0 {
			t.Fatalf("expected no unknown properties, got %v", unknown)
		}
	})

	t.Run("detects schema property without json field", func(t *testing.T) {
		fields := []openAPIJSONField{{name: "real", omitempty: false}}
		missing, unknown := openAPIContractDiff(fields, []string{"real", "ghost"})
		if len(missing) != 0 {
			t.Fatalf("expected no missing fields, got %v", missing)
		}
		if len(unknown) != 1 || unknown[0] != "ghost" {
			t.Fatalf("expected unknown=[ghost], got %v", unknown)
		}
	})

	t.Run("allows omitempty field to be absent from schema", func(t *testing.T) {
		// Rule 3: optional (omitempty) wire additions may be undocumented.
		fields := []openAPIJSONField{
			{name: "mandatory", omitempty: false},
			{name: "optional", omitempty: true},
		}
		missing, unknown := openAPIContractDiff(fields, []string{"mandatory"})
		if len(missing) != 0 || len(unknown) != 0 {
			t.Fatalf("omitempty-absent case must pass, got missing=%v unknown=%v", missing, unknown)
		}
		// ...but it must still be accepted when documented (rule 2 covers
		// schema properties against any json field, omitempty or not).
		missing, unknown = openAPIContractDiff(fields, []string{"mandatory", "optional"})
		if len(missing) != 0 || len(unknown) != 0 {
			t.Fatalf("omitempty-documented case must pass, got missing=%v unknown=%v", missing, unknown)
		}
	})

	t.Run("struct reflection honors json tags", func(t *testing.T) {
		type nested struct {
			Inner string `json:"inner"`
		}
		type fixture struct {
			Plain    string `json:"plain"`
			Optional int    `json:"optional,omitempty"`
			Skipped  string `json:"-"`
			NoTag    bool   // encoding/json default name: "NoTag"
			unexport string // fixture: must be ignored (unexported)
			*nested         // embedded struct is flattened
		}
		fields := openAPIStructJSONFields(reflect.TypeOf(fixture{}))
		got := map[string]bool{}
		for _, f := range fields {
			got[f.name] = f.omitempty
		}
		want := map[string]bool{
			"plain":    false,
			"optional": true,
			"NoTag":    false,
			"inner":    false,
		}
		if len(got) != len(want) {
			t.Fatalf("reflection produced %v, want %v", got, want)
		}
		for name, omitempty := range want {
			have, ok := got[name]
			if !ok {
				t.Errorf("reflection missed field %q", name)
				continue
			}
			if have != omitempty {
				t.Errorf("field %q omitempty=%v, want %v", name, have, omitempty)
			}
		}
		if _, ok := got["Skipped"]; ok {
			t.Errorf("json:\"-\" field must be ignored")
		}
		if _, ok := got["unexport"]; ok {
			t.Errorf("unexported field must be ignored")
		}
	})
}
