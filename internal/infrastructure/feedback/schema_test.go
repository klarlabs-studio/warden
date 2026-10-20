package feedback

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// jsonSchema validates a decoded JSON value against the subset of JSON Schema
// draft-04 the published SARIF 2.1.0 schema uses. It refuses any keyword it
// does not implement, so the schema cannot grow a constraint this check
// silently ignores. "format" is an annotation here, as draft-04 permits.
//
// warden carries no JSON Schema library; this keeps the SARIF test free of a
// dependency the product does not need.
type jsonSchema struct {
	root map[string]any
}

func loadSchema(t *testing.T, path string) jsonSchema {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatal(err)
	}
	return jsonSchema{root: root}
}

var annotationKeywords = map[string]bool{
	"$schema": true, "id": true, "title": true, "description": true, "default": true, "format": true, "definitions": true,
}

func (s jsonSchema) validate(v any) []string {
	var problems []string
	s.check(s.root, v, "$", &problems)
	return problems
}

func (s jsonSchema) check(schema map[string]any, v any, at string, problems *[]string) {
	fail := func(format string, args ...any) {
		*problems = append(*problems, at+": "+fmt.Sprintf(format, args...))
	}
	if ref, ok := schema["$ref"].(string); ok {
		name, found := strings.CutPrefix(ref, "#/definitions/")
		def, _ := s.root["definitions"].(map[string]any)[name].(map[string]any)
		if !found || def == nil {
			fail("unresolvable $ref %q", ref)
			return
		}
		s.check(def, v, at, problems)
		return
	}
	keys := make([]string, 0, len(schema))
	for k := range schema {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		kw := schema[k]
		switch k {
		case "type":
			types, ok := kw.([]any)
			if !ok {
				types = []any{kw}
			}
			matched := false
			for _, want := range types {
				matched = matched || hasType(v, want.(string))
			}
			if !matched {
				fail("want type %v, got %T", kw, v)
			}
		case "enum":
			if !containsValue(kw.([]any), v) {
				fail("%v not in enum %v", v, kw)
			}
		case "pattern":
			if str, ok := v.(string); ok && !regexp.MustCompile(kw.(string)).MatchString(str) {
				fail("%q does not match %s", str, kw)
			}
		case "minimum":
			if n, ok := v.(float64); ok && n < kw.(float64) {
				fail("%v below minimum %v", n, kw)
			}
		case "maximum":
			if n, ok := v.(float64); ok && n > kw.(float64) {
				fail("%v above maximum %v", n, kw)
			}
		case "minItems":
			if arr, ok := v.([]any); ok && float64(len(arr)) < kw.(float64) {
				fail("%d items, want at least %v", len(arr), kw)
			}
		case "uniqueItems":
			if arr, ok := v.([]any); ok && kw.(bool) {
				for i := range arr {
					for j := i + 1; j < len(arr); j++ {
						if reflect.DeepEqual(arr[i], arr[j]) {
							fail("items %d and %d are equal", i, j)
						}
					}
				}
			}
		case "items":
			if arr, ok := v.([]any); ok {
				for i, item := range arr {
					s.check(kw.(map[string]any), item, fmt.Sprintf("%s[%d]", at, i), problems)
				}
			}
		case "required":
			if obj, ok := v.(map[string]any); ok {
				for _, name := range kw.([]any) {
					if _, present := obj[name.(string)]; !present {
						fail("missing required %q", name)
					}
				}
			}
		case "properties", "additionalProperties":
			// Checked together below.
		case "anyOf", "oneOf":
			matched := 0
			for _, alt := range kw.([]any) {
				var sub []string
				s.check(alt.(map[string]any), v, at, &sub)
				if len(sub) == 0 {
					matched++
				}
			}
			if matched == 0 || (k == "oneOf" && matched != 1) {
				fail("%s matched %d alternatives", k, matched)
			}
		default:
			if !annotationKeywords[k] {
				fail("schema keyword %q is not implemented by this validator", k)
			}
		}
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return
	}
	props, _ := schema["properties"].(map[string]any)
	for name, value := range obj {
		if sub, ok := props[name].(map[string]any); ok {
			s.check(sub, value, at+"."+name, problems)
			continue
		}
		switch extra := schema["additionalProperties"].(type) {
		case bool:
			if !extra {
				fail("property %q is not allowed", name)
			}
		case map[string]any:
			s.check(extra, value, at+"."+name, problems)
		}
	}
}

func hasType(v any, want string) bool {
	switch want {
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "string":
		_, ok := v.(string)
		return ok
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		n, ok := v.(float64)
		return ok && n == math.Trunc(n)
	case "null":
		return v == nil
	}
	return false
}

func containsValue(values []any, v any) bool {
	for _, candidate := range values {
		if reflect.DeepEqual(candidate, v) {
			return true
		}
	}
	return false
}

func sarifSchemaValidator(t *testing.T) jsonSchema {
	t.Helper()
	return loadSchema(t, filepath.Join("testdata", "sarif-schema-2.1.0.json"))
}

// The validator must reject what the schema rejects, or a pass proves nothing.
func TestSARIFSchemaValidatorRejectsInvalidLogs(t *testing.T) {
	schema := sarifSchemaValidator(t)
	valid := `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"warden"}},"results":[{"message":{"text":"x"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"a.go"},"region":{"startLine":3}}}]}]}]}`
	var doc any
	if err := json.Unmarshal([]byte(valid), &doc); err != nil {
		t.Fatal(err)
	}
	if problems := schema.validate(doc); len(problems) != 0 {
		t.Fatalf("a minimal valid log was rejected: %v", problems)
	}
	for name, invalid := range map[string]string{
		"wrong version":         `{"version":"2.0.0","runs":[]}`,
		"no runs":               `{"version":"2.1.0"}`,
		"no driver name":        `{"version":"2.1.0","runs":[{"tool":{"driver":{}}}]}`,
		"result no message":     `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"w"}},"results":[{}]}]}`,
		"bad level":             `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"w"}},"results":[{"message":{"text":"x"},"level":"fatal"}]}]}`,
		"line zero":             `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"w"}},"results":[{"message":{"text":"x"},"locations":[{"physicalLocation":{"artifactLocation":{"uri":"a"},"region":{"startLine":0}}}]}]}]}`,
		"unknown run property":  `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"w"}},"warden":{}}]}`,
		"invocation no verdict": `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"w"}},"invocations":[{}]}]}`,
	} {
		if err := json.Unmarshal([]byte(invalid), &doc); err != nil {
			t.Fatal(err)
		}
		if problems := schema.validate(doc); len(problems) == 0 {
			t.Errorf("%s: accepted", name)
		}
	}
}
