// Command genparams writes spec_gen.go and webhooks/events_gen.go from the published OpenAPI document:
// one typed params struct per job type, named string types with constants for
// every enum, sealed interfaces for unions, a Validate method per struct, and
// the closed sets and shapes the API's responses and webhooks use, such as
// the ffprobe result and each event's data. Run it through go generate:
//
//	go generate ./...
//
// It reads fern/openapi.json, the spec the regenerate workflow keeps current,
// so a new job type, param or enum value reaches the SDK by regeneration.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"go/format"
	"io"
	"log"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// node is a JSON value that keeps object key order, so the generated fields
// follow the spec's order.
type node struct {
	keys   []string
	fields map[string]*node
	items  []*node
	value  any // string, float64, bool or nil for scalars
	isObj  bool
	isArr  bool
}

func parse(dec *json.Decoder) (*node, error) {
	tok, err := dec.Token()
	if err != nil {
		return nil, err
	}
	switch t := tok.(type) {
	case json.Delim:
		switch t {
		case '{':
			n := &node{isObj: true, fields: map[string]*node{}}
			for dec.More() {
				kt, err := dec.Token()
				if err != nil {
					return nil, err
				}
				k := kt.(string)
				v, err := parse(dec)
				if err != nil {
					return nil, err
				}
				n.keys = append(n.keys, k)
				n.fields[k] = v
			}
			_, err := dec.Token()
			return n, err
		case '[':
			n := &node{isArr: true}
			for dec.More() {
				v, err := parse(dec)
				if err != nil {
					return nil, err
				}
				n.items = append(n.items, v)
			}
			_, err := dec.Token()
			return n, err
		}
	case json.Number:
		f, err := t.Float64()
		return &node{value: f}, err
	}
	return &node{value: tok}, nil
}

func (n *node) get(path ...string) *node {
	cur := n
	for _, p := range path {
		if cur == nil {
			return nil
		}
		if cur.isArr {
			i, err := strconv.Atoi(p)
			if err != nil || i >= len(cur.items) {
				return nil
			}
			cur = cur.items[i]
			continue
		}
		cur = cur.fields[p]
	}
	return cur
}

// schemaType reads "type", which may be a list such as ["number", "null"].
func (n *node) schemaType() (t string, nullable bool) {
	v := n.fields["type"]
	if v == nil {
		return "", false
	}
	if !v.isArr {
		s, _ := v.value.(string)
		return s, false
	}
	for _, it := range v.items {
		if s, _ := it.value.(string); s == "null" {
			nullable = true
		} else {
			t = s
		}
	}
	return t, nullable
}

func (n *node) str(key string) string {
	if n == nil || n.fields[key] == nil {
		return ""
	}
	s, _ := n.fields[key].value.(string)
	return s
}

func (n *node) num(key string) (float64, bool) {
	if n == nil || n.fields[key] == nil {
		return 0, false
	}
	f, ok := n.fields[key].value.(float64)
	return f, ok
}

func (n *node) strings(key string) []string {
	if n == nil || n.fields[key] == nil {
		return nil
	}
	var out []string
	for _, it := range n.fields[key].items {
		if s, ok := it.value.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

func (n *node) has(key string) bool { return n != nil && n.fields[key] != nil }

// raw renders a scalar or small value for a godoc "Default:" line.
func (n *node) raw() string {
	switch v := n.value.(type) {
	case string:
		return strconv.Quote(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(v)
	case nil:
		if n.isObj || n.isArr {
			return ""
		}
		return ""
	}
	return ""
}

var initialisms = map[string]string{
	"id": "ID", "url": "URL", "cpu": "CPU", "gpu": "GPU", "fps": "FPS", "http": "HTTP", "json": "JSON",
	"hls": "HLS", "dash": "DASH", "ssim": "SSIM", "vmaf": "VMAF", "api": "API", "rgb": "RGB", "hsv": "HSV",
	"lab": "LAB", "eta": "ETA", "usd": "USD", "aws": "AWS", "ids": "IDs", "arn": "ARN", "ok": "OK",
}

// pascal turns "max-bytes", "maxBytes" or "seedvr2-3b" into MaxBytes, MaxBytes, Seedvr23b.
func pascal(s string) string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = nil
		}
	}
	for i, r := range s {
		switch {
		case !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9'):
			flush()
		case r >= 'A' && r <= 'Z' && i > 0:
			flush()
			cur = append(cur, r)
		default:
			cur = append(cur, r)
		}
	}
	flush()
	var b strings.Builder
	for _, w := range words {
		if up, ok := initialisms[strings.ToLower(w)]; ok {
			b.WriteString(up)
			continue
		}
		b.WriteString(strings.ToUpper(w[:1]) + w[1:])
	}
	return b.String()
}

// branchNames names union branches where the generic rule reads badly.
var branchNames = map[string]string{
	"CompressTargetAlpha/0": "Mode",
	"CompressTargetAlpha/1": "Color",
}

type gen struct {
	out      bytes.Buffer
	vars     bytes.Buffer // package-level regexps
	types    map[string]bool
	patterns map[string]string
	known    []string // job types with a typed struct, for CustomParams

	// Set for the request bodies of the resource methods (resources.go), never
	// for job params, so their generated code does not change.
	requests bool
	shared   map[string]string // a generated name to the shared type it is written as
	kinds    map[string]string // a shared type already written, to its kind
	shapes   map[string]string // the JSON of an object already written, to its type
	override map[string]string // "Type.prop" to a Go type the SDK already has, sent unchecked
}

func (g *gen) p(format string, args ...any) { fmt.Fprintf(&g.out, format, args...) }

func (g *gen) claim(name string) {
	if g.types[name] {
		log.Fatalf("genparams: type %s would be generated twice: add a name to branchNames", name)
	}
	g.types[name] = true
}

func comment(prefix string, n *node) string {
	d := strings.Join(strings.Fields(n.str("description")), " ")
	if def := n.fields["default"]; def != nil {
		if r := def.raw(); r != "" && r != `""` {
			if d != "" && !strings.HasSuffix(d, ".") {
				d += "."
			}
			d = strings.TrimSpace(d + " Default: " + r + ".")
		}
	}
	if d == "" {
		return ""
	}
	return prefix + "// " + d + "\n"
}

func article(word string) string {
	if strings.ContainsAny(word[:1], "aeiou") {
		return "an"
	}
	return "a"
}

func skipped(prop *node) bool {
	if v, ok := prop.fields["deprecated"]; ok && v.value == true {
		return true
	}
	return strings.HasPrefix(strings.ToLower(prop.str("description")), "deprecated")
}

// branches flattens anyOf/oneOf, dropping {"type":"null"}.
func branches(n *node) (out []*node, nullable bool) {
	for _, key := range []string{"anyOf", "oneOf"} {
		for _, b := range n.fields[key].itemsOrNil() {
			if b.str("type") == "null" {
				nullable = true
				continue
			}
			if b.has("anyOf") || b.has("oneOf") {
				inner, nn := branches(b)
				out = append(out, inner...)
				nullable = nullable || nn
				continue
			}
			out = append(out, b)
		}
	}
	return out, nullable
}

func (n *node) itemsOrNil() []*node {
	if n == nil {
		return nil
	}
	return n.items
}

func isUnion(n *node) bool { return n.has("anyOf") || n.has("oneOf") }

// The Go type of a param must not change when the API makes a harmless
// schema change, such as adding a maximum or making a field optional. So the
// rules below depend on the JSON type alone, never on bounds or required-ness:
//
//   - every integer is int64, every number float64
//   - an integer, number, boolean or object field is always a pointer, so
//     "absent" stays expressible whether the field is required or optional
//   - a string or enum field is always a value: "" is absent
//   - a union is an interface value: nil is absent
//   - every JSON tag has omitempty
//
// A required field is checked by Validate, not by its Go type.
func intType(*node) string { return "int64" }

// field is one generated struct field.
type field struct {
	goName, wire, goType string
	required, pointer    bool
	schema               *node
	kind                 string // "string", "int", "number", "bool", "enum", "union", "object"
}

// typeOf works out a property's Go type, emitting any named type it needs.
func (g *gen) typeOf(name string, n *node) (goType, kind string) {
	if s, ok := g.shared[name]; ok {
		name = s
		if k, done := g.kinds[name]; done {
			return name, k
		}
	}
	if g.requests && isObject(n) && !isUnion(n) {
		if t, ok := g.shapes[canon(n)]; ok {
			return t, "object" // the same object as a type already written, such as one set of storage keys
		}
	}
	goType, kind = g.typeOfNew(name, n)
	if _, ok := g.kinds[name]; !ok && g.kinds != nil && g.types[name] {
		g.kinds[name] = kind
	}
	return goType, kind
}

func (g *gen) typeOfNew(name string, n *node) (goType, kind string) {
	if isUnion(n) {
		bs, _ := branches(n)
		if len(bs) == 1 {
			return g.typeOf(name, bs[0])
		}
		g.union(name, n, bs)
		return name, "union"
	}
	t, _ := n.schemaType()
	switch t {
	case "string":
		if n.has("enum") {
			g.enum(name, n, n.strings("enum"), true)
			return name, "enum"
		}
		return "string", "string"
	case "integer":
		return intType(n), "int"
	case "number":
		return "float64", "number"
	case "boolean":
		return "bool", "bool"
	case "object":
		g.object(name, n, "", "", "")
		return name, "object"
	case "array":
		if !g.requests || n.fields["items"] == nil {
			break
		}
		elem, kind := g.typeOf(name+"Item", n.fields["items"])
		return "[]" + elem, "array/" + kind
	}
	log.Fatalf("genparams: %s: unsupported schema %v", name, n.keys)
	return "", ""
}

func (g *gen) enum(name string, n *node, values []string, validated bool) {
	if s, ok := g.shared[name]; ok {
		name = s
	}
	if g.requests && g.types[name] {
		return // a shared enum, such as MemberRole, already written
	}
	g.claim(name)
	// A request value is checked against this list, so a new value arrives by
	// regeneration. A response value is never checked, so the API may add one.
	note := "Validate refuses a value outside the list below."
	if !validated {
		note = "The API may add values, so keep a default branch."
	}
	if c := comment("", n); c != "" {
		g.p("%s", strings.TrimSuffix(c, "\n")+"\n//\n// "+note+"\n")
	} else {
		g.p("// %s is a string from a fixed list. %s\n", name, note)
	}
	g.p("type %s string\n\n", name)
	g.p("// The %s values this SDK knows.\nconst (\n", name)
	seen := map[string]string{}
	var cs []string
	varnames := n.strings("x-enum-varnames")
	for i, v := range values {
		c := name + pascal(v)
		if g.requests && len(varnames) == len(values) {
			c = name + varnames[i] // such as StatsWindowDay for "24h"
		}
		if prev, dup := seen[c]; dup {
			log.Fatalf("genparams: %q and %q both name %s", prev, v, c)
		}
		seen[c] = v
		cs = append(cs, c)
		g.p("\t%s %s = %q\n", c, name, v)
	}
	g.p(")\n\n")
	if validated {
		g.p("func (v %s) known() bool {\n\tswitch v {\n\tcase ", name)
		g.p("%s:\n\t\treturn true\n\t}\n\treturn false\n}\n\n", strings.Join(cs, ", "))
	}
}

// union emits a sealed interface and one type per branch.
func (g *gen) union(name string, n *node, bs []*node) {
	g.claim(name)
	if c := comment("", n); c != "" {
		g.p("%s", c)
	}
	g.p("//\n// Set it to one of: ")
	type branch struct {
		name string
		n    *node
	}
	var list []branch
	for i, b := range bs {
		bn := branchNames[fmt.Sprintf("%s/%d", name, i)]
		if bn == "" {
			bn = branchName(b)
		}
		if _, val := enumDiscriminator(b); g.requests && val != "" {
			bn = pascal(val)
		}
		full := name + bn
		if s, ok := g.shared[full]; ok {
			full = s
		}
		if t, ok := g.shapes[canon(b)]; g.requests && ok {
			full = t // the same object as a branch already written
		}
		list = append(list, branch{full, b})
	}
	var names []string
	for _, b := range list {
		names = append(names, b.name)
	}
	g.p("%s.\n", strings.Join(names, ", "))
	g.p("type %s interface {\n\tis%s()\n\tvalidate(c *check, path string)\n}\n\n", name, name)
	for _, b := range list {
		switch {
		case g.types[b.name]:
			// written for another union already: it only joins this one
		case b.n.str("type") == "object":
			disc, val := discriminator(b.n)
			if g.requests {
				disc, val = enumDiscriminator(b.n)
			}
			g.object(b.name, b.n, disc, val, "")
		case b.n.str("type") == "string" && b.n.has("enum"):
			g.enum(b.name, b.n, b.n.strings("enum"), true)
			g.p("func (v %s) validate(c *check, path string) {\n\tif !v.known() {\n\t\tc.fail(path, \"is not one of the known values\")\n\t}\n}\n\n", b.name)
		default:
			goType, kind := g.typeOf("", b.n)
			g.claim(b.name)
			g.p("%stype %s %s\n\n", comment("", b.n), b.name, goType)
			g.p("func (v %s) validate(c *check, path string) {\n%s}\n\n", b.name, g.bounds(goType+"(v)", kind, b.n, "path"))
		}
		g.p("func (%s) is%s() {}\n\n", b.name, name)
	}
}

func branchName(b *node) string {
	if disc, val := discriminator(b); disc != "" {
		return pascal(val)
	}
	switch b.str("type") {
	case "object":
		if props := b.fields["properties"]; props != nil && len(props.keys) == 1 {
			return pascal(props.keys[0])
		}
		return "Object"
	case "string":
		if b.has("enum") {
			return "Preset"
		}
		return "String"
	case "integer":
		return "Int"
	case "number":
		return "Number"
	case "boolean":
		return "Bool"
	}
	return "Value"
}

// discriminator finds the property whose schema is a const string.
func discriminator(n *node) (prop, value string) {
	props := n.fields["properties"]
	if props == nil {
		return "", ""
	}
	for _, k := range props.keys {
		if c := props.fields[k].str("const"); c != "" {
			return k, c
		}
	}
	return "", ""
}

// enumDiscriminator also takes a property with a one-value enum, the way the
// request bodies of the resource methods mark their branches.
func enumDiscriminator(n *node) (prop, value string) {
	if prop, value = discriminator(n); prop != "" {
		return prop, value
	}
	props := n.fields["properties"]
	if props == nil {
		return "", ""
	}
	for _, k := range props.keys {
		if vs := props.fields[k].strings("enum"); len(vs) == 1 && props.fields[k].str("type") == "string" {
			return k, vs[0]
		}
	}
	return "", ""
}

// object emits a struct. disc/discValue name a const property that the
// struct sends but does not expose. jobType, when set, makes it a JobParams.
func (g *gen) object(name string, n *node, disc, discValue, jobType string) {
	g.claim(name)
	if g.shapes != nil {
		g.shapes[canon(n)] = name
	}
	props := n.fields["properties"]
	required := map[string]bool{}
	for _, r := range n.strings("required") {
		required[r] = true
	}
	var fields []field
	if props != nil {
		for _, k := range props.keys {
			prop := props.fields[k]
			if k == disc || skipped(prop) {
				continue
			}
			f := field{goName: pascal(k), wire: k, required: required[k], schema: prop}
			if t, ok := g.override[name+"."+k]; ok {
				f.goType, f.kind = t, "raw"
			} else {
				f.goType, f.kind = g.typeOf(strings.TrimSuffix(name, "Params")+pascal(k), prop)
			}
			_, nullable := branches(prop)
			if _, null := prop.schemaType(); null || nullable {
				f.required = false
			}
			f.pointer = f.kind == "int" || f.kind == "number" || f.kind == "bool" || f.kind == "object"
			fields = append(fields, f)
		}
	}
	doc := comment("", n)
	if jobType != "" {
		if strings.HasPrefix(doc, "// Params of") {
			doc = "" // the spec's own line says no more than the generated one
		}
		doc = fmt.Sprintf("// %s are the params of %s %s job.\n", name, article(jobType), jobType) + strings.Replace(doc, "// ", "//\n// ", 1)
		if discValue != "" {
			doc += fmt.Sprintf("//\n// It sends %s %q.\n", disc, discValue)
		}
	}
	g.p("%stype %s struct {\n", doc, name)
	for _, f := range fields {
		g.p("%s", comment("\t", f.schema))
		t := f.goType
		if f.pointer {
			t = "*" + t
		}
		g.p("\t%s %s `json:%q`\n", f.goName, t, f.wire+",omitempty")
	}
	g.p("}\n\n")

	if disc != "" {
		g.p("// MarshalJSON sends %s %q with the fields.\n", disc, discValue)
		g.p("func (v %s) MarshalJSON() ([]byte, error) {\n\ttype plain %s\n\treturn marshalWith(plain(v), %q, %q)\n}\n\n", name, name, disc, discValue)
	}
	if jobType != "" {
		g.p("// JobType returns JobType%s.\nfunc (%s) JobType() JobType { return JobType%s }\n\n", pascal(jobType), name, pascal(jobType))
		g.p("// Validate checks the params against the job type's schema without sending anything.\n")
		g.p("// It returns an *Error with code INVALID_ARGUMENT listing each problem.\n")
		g.p("func (v %s) Validate() error {\n\tvar c check\n\tv.validate(&c, \"\")\n\treturn c.err()\n}\n\n", name)
		g.p("func (%s) isJobParams() {}\n\n", name)
	}
	g.p("func (v %s) validate(c *check, path string) {\n", name)
	for _, f := range fields {
		g.fieldChecks(f)
	}
	g.p("}\n\n")
}

func (g *gen) fieldChecks(f field) {
	ref := "v." + f.goName
	path := fmt.Sprintf("join(path, %q)", f.wire)
	if elem, ok := strings.CutPrefix(f.kind, "array/"); ok {
		g.arrayChecks(f, elem, ref, path)
		return
	}
	switch f.kind {
	case "raw":
		if f.required {
			g.p("\tif len(%s) == 0 {\n\t\tc.fail(%s, \"is required\")\n\t}\n", ref, path)
		}
		return
	case "union":
		if f.required {
			g.p("\tif %s == nil {\n\t\tc.fail(%s, \"is required\")\n\t} else {\n\t\t%s.validate(c, %s)\n\t}\n", ref, path, ref, path)
		} else {
			g.p("\tif %s != nil {\n\t\t%s.validate(c, %s)\n\t}\n", ref, ref, path)
		}
		return
	case "object":
		if f.required {
			g.p("\tif %s == nil {\n\t\tc.fail(%s, \"is required\")\n\t} else {\n\t\t%s.validate(c, %s)\n\t}\n", ref, path, ref, path)
		} else {
			g.p("\tif %s != nil {\n\t\t%s.validate(c, %s)\n\t}\n", ref, ref, path)
		}
		return
	case "enum":
		if f.required {
			g.p("\tif %s == \"\" {\n\t\tc.fail(%s, \"is required\")\n\t} else if !%s.known() {\n\t\tc.fail(%s, \"is not one of the known values\")\n\t}\n", ref, path, ref, path)
		} else {
			g.p("\tif %s != \"\" && !%s.known() {\n\t\tc.fail(%s, \"is not one of the known values\")\n\t}\n", ref, ref, path)
		}
		return
	case "string":
		inner := g.bounds(ref, f.kind, f.schema, path)
		switch {
		case f.required && inner != "":
			g.p("\tif %s == \"\" {\n\t\tc.fail(%s, \"is required\")\n\t} else {\n%s\t}\n", ref, path, inner)
		case f.required:
			g.p("\tif %s == \"\" {\n\t\tc.fail(%s, \"is required\")\n\t}\n", ref, path)
		case inner != "":
			g.p("\tif %s != \"\" {\n%s\t}\n", ref, inner)
		}
		return
	}
	inner := g.bounds("*"+ref, f.kind, f.schema, path)
	switch {
	case f.required && inner != "":
		g.p("\tif %s == nil {\n\t\tc.fail(%s, \"is required\")\n\t} else {\n%s\t}\n", ref, path, inner)
	case f.required:
		g.p("\tif %s == nil {\n\t\tc.fail(%s, \"is required\")\n\t}\n", ref, path)
	case inner != "":
		g.p("\tif %s != nil {\n%s\t}\n", ref, inner)
	}
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// safeInt is the largest integer JSON carries exactly. A bound at it means
// "any integer" and needs no check.
const safeInt = 9007199254740991

// bounds returns the min/max, length and pattern checks for one value, or "".
func (g *gen) bounds(val, kind string, n *node, path string) string {
	var b strings.Builder
	w := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }
	switch kind {
	case "int", "number":
		if lo, ok := n.num("minimum"); ok && lo > -safeInt {
			w("\tif float64(%s) < %s {\n\t\tc.fail(%s, \"must be at least %s\")\n\t}\n", val, num(lo), path, num(lo))
		}
		if lo, ok := n.num("exclusiveMinimum"); ok {
			w("\tif float64(%s) <= %s {\n\t\tc.fail(%s, \"must be more than %s\")\n\t}\n", val, num(lo), path, num(lo))
		}
		if hi, ok := n.num("maximum"); ok && hi < safeInt {
			w("\tif float64(%s) > %s {\n\t\tc.fail(%s, \"must be at most %s\")\n\t}\n", val, num(hi), path, num(hi))
		}
	case "string":
		if lo, ok := n.num("minLength"); ok && lo > 1 {
			w("\tif utf8.RuneCountInString(string(%s)) < %s {\n\t\tc.fail(%s, \"must be at least %s characters\")\n\t}\n", val, num(lo), path, num(lo))
		}
		if hi, ok := n.num("maxLength"); ok {
			w("\tif utf8.RuneCountInString(string(%s)) > %s {\n\t\tc.fail(%s, \"must be at most %s characters\")\n\t}\n", val, num(hi), path, num(hi))
		}
		if pat := n.str("pattern"); pat != "" {
			if _, err := regexp.Compile(pat); err == nil {
				w("\tif !%s.MatchString(string(%s)) {\n\t\tc.fail(%s, %q)\n\t}\n", g.pattern(pat), val, path, "must match "+pat)
			}
		}
	}
	return b.String()
}

func (g *gen) pattern(pat string) string {
	if v, ok := g.patterns[pat]; ok {
		return v
	}
	v := fmt.Sprintf("pattern%d", len(g.patterns)+1)
	g.patterns[pat] = v
	return v
}

func main() {
	specPath := flag.String("spec", "fern/openapi.json", "the OpenAPI document")
	outPath := flag.String("out", "spec_gen.go", "the root package file to write")
	hooksPath := flag.String("webhooks", "webhooks/events_gen.go", "the webhooks package file to write")
	flag.Parse()
	f, err := os.Open(*specPath)
	if err != nil {
		log.Fatal(err)
	}
	dec := json.NewDecoder(f)
	dec.UseNumber()
	spec, err := parse(dec)
	f.Close()
	if err != nil && err != io.EOF {
		log.Fatal(err)
	}
	schemas := spec.get("components", "schemas")
	g := &gen{types: map[string]bool{}, patterns: map[string]string{}}

	// Job params: every Submit<X>Job in the submit request body names a type and its params.
	body := spec.get("paths", "/jobs", "post", "requestBody", "content", "application/json", "schema")
	for _, ref := range body.fields["anyOf"].items {
		submit := schemas.fields[strings.TrimPrefix(ref.str("$ref"), "#/components/schemas/")]
		jobType := submit.get("properties", "type").str("const")
		paramsRef := submit.get("properties", "params").str("$ref")
		if jobType == "" || paramsRef == "" {
			continue // SubmitAnyJob, the untyped form
		}
		name := strings.TrimPrefix(paramsRef, "#/components/schemas/")
		params := schemas.fields[name]
		g.known = append(g.known, jobType)
		if bs, _ := branches(params); len(bs) > 0 {
			base := strings.TrimSuffix(name, "Params")
			for _, b := range bs {
				disc, val := discriminator(b)
				if disc == "" {
					log.Fatalf("genparams: %s: a union branch without a const discriminator", name)
				}
				g.object(base+pascal(val)+"Params", b, disc, val, jobType)
			}
			continue
		}
		g.object(name, params, "", "", jobType)
	}

	// The closed sets the API uses. Response sets are not validated, so a value
	// the API adds later still reads. A request set (sent by the SDK) is.
	for _, e := range []struct {
		name, schema string
		request      bool
	}{
		{"CallbackEvent", "SubmitAnyJob/properties/callback/properties/events/items", true},
		{"OutputCategory", "JobComplete/properties/outputCategory", false},
		{"FileType", "OutputFileType", false},
		{"CallbackStatus", "CallbackStatus/properties/status", false},
		{"DeliveryStatus", "JobDelivery/properties/status", false},
		{"DeliveryReason", "JobDelivery/properties/reason", false},
		{"FailurePhase", "JobError/properties/failedPhase", false},
		{"Currency", "JobCost/properties/currency", false},
		{"FieldType", "ConnectorField/properties/type", false},
	} {
		n := schemas.get(strings.Split(e.schema, "/")...)
		if n == nil || !n.has("enum") {
			log.Fatalf("genparams: %s has no enum", e.schema)
		}
		g.enum(e.name, n, n.strings("enum"), e.request)
	}

	// Job statuses, from each status schema's own status const.
	var statuses []string
	for _, ref := range schemas.get("Job", "oneOf").itemsOrNil() {
		s := schemas.fields[strings.TrimPrefix(ref.str("$ref"), "#/components/schemas/")]
		statuses = append(statuses, s.get("properties", "status").strings("enum")...)
	}
	if len(statuses) == 0 {
		log.Fatal("genparams: no job statuses found")
	}
	g.p("// JobStatus is where a job is in its life. The API may add statuses, so\n// keep a default branch, and ask Job.IsTerminal whether a job has finished.\ntype JobStatus string\n\n")
	g.p("// The job statuses this SDK knows.\nconst (\n")
	for _, s := range statuses {
		g.p("\tStatus%s JobStatus = %q\n", pascal(s), s)
	}
	g.p(")\n\n")

	sort.Strings(g.known)
	g.p("// The job types with typed params in this SDK. Jobs.Types lists every live\n// type, which may include ones newer than this list.\nconst (\n")
	for _, k := range g.known {
		g.p("\tJobType%s JobType = %q\n", pascal(k), k)
	}
	g.p(")\n\n")

	// Response shapes the spec now names, for readers the hand-written layer
	// does not model itself: the ffprobe result.
	root := &resp{schemas: schemas, done: g.types, mapped: map[string]string{}, aliases: map[string]string{}, fields: map[string]string{}}
	root.named("FfprobeResult", schemas.fields["FfprobeResult"])
	g.out.Write(root.out.Bytes())

	// The request bodies and responses of the resource methods.
	g.resources(spec, schemas)

	// Webhook events: one data type per event, behind Event.Payload.
	hooks := &resp{
		schemas: schemas, qual: "rendobar.", done: map[string]bool{},
		mapped: map[string]string{"JobOutput": "*rendobar.JobOutput", "JobError": "*rendobar.JobError"},
		aliases: map[string]string{
			"SettledDeliveryStatus":  "rendobar.DeliveryStatus",
			"SettledDeliveryReason":  "rendobar.DeliveryReason",
			"WebhookJobCostCurrency": "rendobar.Currency",
		},
		fields: map[string]string{
			"jobType":                      "rendobar.JobType",
			"triggeredByJobType":           "rendobar.JobType",
			"outputCategory":               "rendobar.OutputCategory",
			"WebhookJobStep.status":        "rendobar.StepStatus",
			"JobDeliveryFailedData.reason": "rendobar.DeliveryReason",
		},
	}
	writeWebhooks(spec, hooks, *hooksPath)

	var pats []string
	for pat := range g.patterns {
		pats = append(pats, pat)
	}
	sort.Strings(pats)
	if len(pats) > 0 {
		g.vars.WriteString("var (\n")
		for _, pat := range pats {
			fmt.Fprintf(&g.vars, "\t%s = regexp.MustCompile(%q)\n", g.patterns[pat], pat)
		}
		g.vars.WriteString(")\n\n")
	}

	var src bytes.Buffer
	src.WriteString("// Code generated by genparams from fern/openapi.json. DO NOT EDIT.\n\npackage rendobar\n\n")
	src.WriteString("import (\n\t\"encoding/json\"\n\t\"regexp\"\n\t\"strconv\"\n\t\"unicode/utf8\"\n)\n\n")
	src.Write(g.vars.Bytes())
	src.Write(g.out.Bytes())
	write(*outPath, src.Bytes())
}

func write(path string, src []byte) {
	formatted, err := format.Source(src)
	if err != nil {
		os.WriteFile(path+".broken", src, 0o644)
		log.Fatalf("genparams: generated code does not format: %v (see %s.broken)", err, path)
	}
	if err := os.WriteFile(path, formatted, 0o644); err != nil {
		log.Fatal(err)
	}
}

// writeWebhooks writes the event names from the spec's webhooks section, one
// data type per event, and Event.Payload, which reads the data for Type.
func writeWebhooks(spec *node, r *resp, path string) {
	payload := spec.get("components", "schemas", "WebhookPayload")
	mapping := payload.get("discriminator", "mapping")
	if mapping == nil {
		log.Fatal("genparams: WebhookPayload has no discriminator mapping")
	}
	type event struct{ name, constant, data string }
	var events []event
	version := ""
	for _, name := range mapping.keys {
		envelope := r.schemas.fields[strings.TrimPrefix(mapping.fields[name].value.(string), "#/components/schemas/")]
		if v := envelope.get("properties", "version").strings("enum"); len(v) == 1 {
			version = v[0]
		}
		dataRef := refName(envelope.get("properties", "data"))
		if dataRef == "" {
			log.Fatalf("genparams: the %s envelope has no data component", name)
		}
		events = append(events, event{name, pascal(name), r.named(dataRef, r.schemas.fields[dataRef])})
	}
	var head bytes.Buffer
	head.WriteString("// Code generated by genparams from fern/openapi.json. DO NOT EDIT.\n\npackage webhooks\n\n")
	head.WriteString("import (\n\t\"encoding/json\"\n\t\"fmt\"\n\n\t\"github.com/rendobar/rendobar-go\"\n)\n\n")
	head.WriteString("var _ = rendobar.UnixMillis(0) // the data types below may not name the root package\n\n")
	head.WriteString("// The events this SDK knows. The API may add events: an unknown one still\n// parses, with its own Type, so keep a default branch.\nconst (\n")
	for _, e := range events {
		desc := strings.Join(strings.Fields(spec.get("webhooks", e.name, "post").str("summary")), " ")
		fmt.Fprintf(&head, "\t%s rendobar.WebhookEvent = %q // %s\n", e.constant, e.name, desc)
	}
	head.WriteString(")\n\n")
	for _, e := range events {
		fmt.Fprintf(&head, "func (*%s) isPayload() {}\n\n", e.data)
	}
	head.WriteString("// Payload reads Data as the type for Type, such as *JobCompletedData for\n// JobCompleted. An event newer than this SDK gives *UnknownPayload.\n")
	if version == "" {
		log.Fatal("genparams: the webhook envelopes name no version")
	}
	fmt.Fprintf(&head, "// envelopeVersion is the envelope version the data types below describe.\nconst envelopeVersion = %q\n\n", version)
	head.WriteString("// An envelope of another version gives *UnknownPayload: its data may have\n// another shape.\n")
	head.WriteString("func (e *Event) Payload() (Payload, error) {\n\tif e.Version != envelopeVersion {\n\t\treturn &UnknownPayload{Type: e.Type, Data: e.Data}, nil\n\t}\n\tvar p Payload\n\tswitch e.Type {\n")
	for _, e := range events {
		fmt.Fprintf(&head, "\tcase %s:\n\t\tp = new(%s)\n", e.constant, e.data)
	}
	head.WriteString("\tdefault:\n\t\treturn &UnknownPayload{Type: e.Type, Data: e.Data}, nil\n\t}\n")
	head.WriteString("\tif err := json.Unmarshal(e.Data, p); err != nil {\n\t\treturn nil, fmt.Errorf(\"%w: %s data: %v\", ErrPayload, e.Type, err)\n\t}\n\treturn p, nil\n}\n\n")
	head.Write(r.out.Bytes())
	write(path, head.Bytes())
}
