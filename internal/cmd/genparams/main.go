// Command genparams writes params_gen.go from the published OpenAPI document:
// one typed params struct per job type, named string types with constants for
// every enum, sealed interfaces for unions, a Validate method per struct, and
// the closed sets the job responses use. Run it through go generate:
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
	"math"
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
	"lab": "LAB", "eta": "ETA", "usd": "USD",
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

func intType(n *node) string {
	lo, okLo := n.num("minimum")
	hi, okHi := n.num("maximum")
	if !okLo {
		lo, okLo = n.num("exclusiveMinimum")
	}
	if okLo && okHi && lo >= math.MinInt32 && hi <= math.MaxInt32 {
		return "int"
	}
	return "int64"
}

// field is one generated struct field.
type field struct {
	goName, wire, goType string
	required, pointer    bool
	schema               *node
	kind                 string // "string", "int", "number", "bool", "enum", "union", "object"
}

// typeOf works out a property's Go type, emitting any named type it needs.
func (g *gen) typeOf(name string, n *node) (goType, kind string) {
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
	}
	log.Fatalf("genparams: %s: unsupported schema %v", name, n.keys)
	return "", ""
}

func (g *gen) enum(name string, n *node, values []string, validated bool) {
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
	for _, v := range values {
		c := name + pascal(v)
		if prev, dup := seen[c]; dup {
			log.Fatalf("genparams: %q and %q both name %s", prev, v, c)
		}
		seen[c] = v
		g.p("\t%s %s = %q\n", c, name, v)
	}
	g.p(")\n\n")
	if validated {
		g.p("func (v %s) known() bool {\n\tswitch v {\n\tcase ", name)
		var cs []string
		for _, val := range values {
			cs = append(cs, name+pascal(val))
		}
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
		list = append(list, branch{name + bn, b})
	}
	var names []string
	for _, b := range list {
		names = append(names, b.name)
	}
	g.p("%s.\n", strings.Join(names, ", "))
	g.p("type %s interface {\n\tis%s()\n\tvalidate(c *check, path string)\n}\n\n", name, name)
	for _, b := range list {
		switch {
		case b.n.str("type") == "object":
			disc, val := discriminator(b.n)
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

// object emits a struct. disc/discValue name a const property that the
// struct sends but does not expose. jobType, when set, makes it a JobParams.
func (g *gen) object(name string, n *node, disc, discValue, jobType string) {
	g.claim(name)
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
			f.goType, f.kind = g.typeOf(strings.TrimSuffix(name, "Params")+pascal(k), prop)
			_, nullable := branches(prop)
			if _, null := prop.schemaType(); null || nullable {
				f.required = false
			}
			if !f.required && (f.kind == "int" || f.kind == "number" || f.kind == "bool" || f.kind == "object") {
				f.pointer = true
			}
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
		tag := f.wire
		if !f.required {
			tag += ",omitempty"
		}
		g.p("\t%s %s `json:%q`\n", f.goName, t, tag)
	}
	g.p("}\n\n")

	if disc != "" {
		g.p("// MarshalJSON sends %s %q with the fields.\n", disc, discValue)
		g.p("func (v %s) MarshalJSON() ([]byte, error) {\n\ttype plain %s\n\treturn marshalWith(plain(v), %q, %q)\n}\n\n", name, name, disc, discValue)
	}
	if jobType != "" {
		g.p("// JobType returns %q.\nfunc (%s) JobType() string { return %q }\n\n", jobType, name, jobType)
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
	switch f.kind {
	case "union":
		if f.required {
			g.p("\tif %s == nil {\n\t\tc.fail(%s, \"is required\")\n\t} else {\n\t\t%s.validate(c, %s)\n\t}\n", ref, path, ref, path)
		} else {
			g.p("\tif %s != nil {\n\t\t%s.validate(c, %s)\n\t}\n", ref, ref, path)
		}
		return
	case "object":
		if f.pointer {
			g.p("\tif %s != nil {\n\t\t%s.validate(c, %s)\n\t}\n", ref, ref, path)
		} else {
			g.p("\t%s.validate(c, %s)\n", ref, path)
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
	if f.pointer {
		if inner := g.bounds("*"+ref, f.kind, f.schema, path); inner != "" {
			g.p("\tif %s != nil {\n%s\t}\n", ref, inner)
		}
		return
	}
	g.p("%s", g.bounds(ref, f.kind, f.schema, path))
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
	outPath := flag.String("out", "params_gen.go", "the file to write")
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
		{"FileType", "JobComplete/properties/output/properties/file/properties/type", false},
		{"CallbackStatus", "JobComplete/properties/callback/properties/status", false},
		{"DeliveryStatus", "JobComplete/properties/deliveries/items/properties/status", false},
		{"DeliveryReason", "JobComplete/properties/deliveries/items/properties/reason", false},
		{"FailurePhase", "JobFailed/properties/error/properties/failedPhase", false},
		{"Currency", "JobComplete/properties/cost/properties/currency", false},
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
	g.p("// JobStatus is where a job is in its life. The API may add statuses later,\n// so treat an unknown one as still in progress.\ntype JobStatus string\n\n")
	g.p("// The job statuses this SDK knows.\nconst (\n")
	for _, s := range statuses {
		g.p("\tStatus%s JobStatus = %q\n", pascal(s), s)
	}
	g.p(")\n\n")

	sort.Strings(g.known)
	g.p("// typedJobTypes maps each job type with a typed params struct to it, so\n// CustomParams can point there instead.\nvar typedJobTypes = map[string]bool{\n")
	for _, k := range g.known {
		g.p("\t%q: true,\n", k)
	}
	g.p("}\n\n")

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
	src.WriteString("import (\n\t\"regexp\"\n\t\"unicode/utf8\"\n)\n\n")
	src.Write(g.vars.Bytes())
	src.Write(g.out.Bytes())
	formatted, err := format.Source(src.Bytes())
	if err != nil {
		os.WriteFile(*outPath+".broken", src.Bytes(), 0o644)
		log.Fatalf("genparams: generated code does not format: %v (see %s.broken)", err, *outPath)
	}
	if err := os.WriteFile(*outPath, formatted, 0o644); err != nil {
		log.Fatal(err)
	}
}
