package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"go/format"
	"os"
	"regexp"
	"strings"
	"testing"
)

var update = flag.Bool("update", false, "rewrite testdata/*.golden")

func schema(t *testing.T, src string) *node {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(src))
	dec.UseNumber()
	n, err := parse(dec)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// fieldLine returns the declaration of one field as the generator writes it.
func fieldLine(t *testing.T, src, field string) string {
	t.Helper()
	g := &gen{types: map[string]bool{}, patterns: map[string]string{}}
	g.object("T", schema(t, src), "", "", "")
	m := regexp.MustCompile(`(?m)^\t` + field + ` .*$`).FindString(g.out.String())
	if m == "" {
		t.Fatalf("no field %s in:\n%s", field, g.out.String())
	}
	return strings.Join(strings.Fields(m), " ")
}

// A harmless schema change must never change a param's Go type: adding or
// removing bounds, or moving a field between required and optional.
func TestTypeRulesIgnoreBoundsAndRequiredness(t *testing.T) {
	for _, tc := range []struct{ field, a, b string }{
		{"N", `{"properties":{"n":{"type":"integer"}}}`, `{"properties":{"n":{"type":"integer","minimum":0,"maximum":10}},"required":["n"]}`},
		{"N", `{"properties":{"n":{"type":"integer","maximum":9007199254740991}}}`, `{"properties":{"n":{"type":"integer","maximum":100}}}`},
		{"F", `{"properties":{"f":{"type":"number"}}}`, `{"properties":{"f":{"type":"number","minimum":0.5}},"required":["f"]}`},
		{"B", `{"properties":{"b":{"type":"boolean"}}}`, `{"properties":{"b":{"type":"boolean"}},"required":["b"]}`},
		{"S", `{"properties":{"s":{"type":"string"}}}`, `{"properties":{"s":{"type":"string","maxLength":5}},"required":["s"]}`},
		{"O", `{"properties":{"o":{"type":"object","properties":{"x":{"type":"string"}}}}}`, `{"properties":{"o":{"type":"object","properties":{"x":{"type":"string"}}}},"required":["o"]}`},
		{"N", `{"properties":{"n":{"type":["integer","null"]}}}`, `{"properties":{"n":{"type":"integer"}},"required":["n"]}`},
	} {
		if a, b := fieldLine(t, tc.a, tc.field), fieldLine(t, tc.b, tc.field); a != b {
			t.Errorf("%s changed type:\n  %s\n  %s", tc.field, a, b)
		}
	}
}

// The generated code for a fixture covering every rule, compared with a
// reviewed copy. Run go test ./internal/cmd/genparams -update to accept a change.
func TestGolden(t *testing.T) {
	raw, err := os.ReadFile("testdata/rules.json")
	if err != nil {
		t.Fatal(err)
	}
	g := &gen{types: map[string]bool{}, patterns: map[string]string{}}
	g.object("RulesParams", schema(t, string(raw)), "", "", "rules.test")
	var src bytes.Buffer
	src.WriteString("package golden\n\n")
	src.Write(g.out.Bytes())
	got, err := format.Source(src.Bytes())
	if err != nil {
		t.Fatalf("generated code does not format: %v\n%s", err, src.String())
	}
	if *update {
		if err := os.WriteFile("testdata/rules.golden", got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/rules.golden")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n")), got) {
		t.Fatalf("generated code differs from testdata/rules.golden. Review the diff, then rerun with -update.\n%s", got)
	}
}
