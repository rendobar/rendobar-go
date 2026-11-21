package main

import (
	"bytes"
	"fmt"
	"log"
	"strings"
)

// resp writes read-only types for spec components the API sends: structs with
// JSON tags, open enums and discriminated unions. A reader tolerates unknown
// fields and values: they are ignored, never an error.
type resp struct {
	out     bytes.Buffer
	schemas *node
	qual    string            // the root package's qualifier from another package, "rendobar." or ""
	mapped  map[string]string // components the hand-written layer already models, to their Go type
	aliases map[string]string // enum names that are a type the SDK already has
	fields  map[string]string // "Type.prop" or "prop" to a Go type the SDK already has, for a field the spec leaves a plain string
	done    map[string]bool
	order   []string // emitted type names, so a name is never written twice
}

func (r *resp) p(format string, args ...any) { fmt.Fprintf(&r.out, format, args...) }

func refName(n *node) string {
	return strings.TrimPrefix(n.str("$ref"), "#/components/schemas/")
}

// enumType returns the type for an enum: a known one when aliases names it,
// else a new one written here.
func (r *resp) enumType(name string, n *node, values []string) string {
	if t, ok := r.aliases[name]; ok {
		return t
	}
	if r.done[name] {
		log.Fatalf("genparams: enum %s would be written twice", name)
	}
	r.done[name] = true
	if c := comment("", n); c != "" {
		r.p("%s//\n", c)
	}
	r.p("// The API may add values, so keep a default branch.\ntype %s string\n\n// The %s values this SDK knows.\nconst (\n", name, name)
	for _, v := range values {
		r.p("\t%s%s %s = %q\n", name, pascal(v), name, v)
	}
	r.p(")\n\n")
	return name
}

// typ returns the Go type that reads n. name names any type n needs.
// optional is true when the property may be absent.
func (r *resp) typ(name, prop string, n *node, optional bool) string {
	if ref := refName(n); ref != "" {
		if t, ok := r.mapped[ref]; ok {
			return t
		}
		target := r.schemas.fields[ref]
		t := r.named(ref, target)
		if isObject(target) || isUnion(target) {
			return "*" + t
		}
		return t
	}
	if isUnion(n) {
		bs, nullable := branches(n)
		if t := scalarBranches(bs); t != "" {
			if nullable {
				return "*" + t
			}
			return t // such as rotation: one of 0, 90, 180 or 270
		}
		if len(bs) == 1 {
			t := r.typ(name, prop, bs[0], optional || nullable)
			if nullable && !strings.HasPrefix(t, "*") && !strings.HasPrefix(t, "[]") && !strings.HasPrefix(t, "map[") && t != "json.RawMessage" {
				return "*" + t
			}
			return t
		}
		if n.has("discriminator") {
			return "*" + r.named(name, n)
		}
		return "json.RawMessage" // an undiscriminated union stays raw
	}
	t, nullable := n.schemaType()
	ptr := func(s string) string {
		if nullable {
			return "*" + s
		}
		return s
	}
	switch t {
	case "string":
		if values := n.strings("enum"); len(values) > 0 {
			return ptr(r.enumType(name, n, values))
		}
		return "string" // a null string reads as ""
	case "integer":
		if strings.HasSuffix(prop, "At") {
			return r.qual + "UnixMillis"
		}
		return ptr("int64")
	case "number":
		return ptr("float64")
	case "boolean":
		return ptr("bool")
	case "array":
		return "[]" + strings.TrimPrefix(r.typ(name+"Item", prop, n.fields["items"], false), "*")
	case "object", "":
		if n.has("properties") {
			return "*" + r.named(name, n)
		}
		if ap := n.fields["additionalProperties"]; ap != nil && ap.isObj && len(ap.keys) > 0 {
			if t := strings.TrimPrefix(r.typ(name+"Value", "", ap, false), "*"); t != "json.RawMessage" && !strings.HasPrefix(t, "[]") {
				return "map[string]" + t
			}
		}
		return "json.RawMessage"
	}
	log.Fatalf("genparams: %s: unsupported response schema type %q", name, t)
	return ""
}

// discriminators are the property names a type's own kind rides on. With a
// single value they say nothing the type does not, so they are left out.
var discriminators = map[string]bool{"status": true, "kind": true, "event": true, "version": true}

// scalarBranches returns the one Go type every branch shares when each is a
// scalar of one JSON type, such as a number enum split into branches, else "".
func scalarBranches(bs []*node) string {
	if len(bs) < 2 {
		return ""
	}
	first, _ := bs[0].schemaType()
	integral := true
	for _, b := range bs {
		t, _ := b.schemaType()
		if t != first || b.has("properties") {
			return ""
		}
		for _, v := range b.fields["enum"].itemsOrNil() {
			if f, ok := v.value.(float64); !ok || f != float64(int64(f)) {
				integral = false
			}
		}
	}
	switch {
	case first == "integer", first == "number" && integral:
		return "int64"
	case first == "number":
		return "float64"
	case first == "string":
		return "string"
	case first == "boolean":
		return "bool"
	}
	return ""
}

func isObject(n *node) bool {
	t, _ := n.schemaType()
	return t == "object" || n.has("properties")
}

// named writes a struct, enum or union under name once, and returns name.
func (r *resp) named(name string, n *node) string {
	if r.done[name] {
		return name
	}
	switch {
	case isUnion(n):
		r.done[name] = true
		r.union(name, n)
	case isObject(n):
		r.done[name] = true
		r.object(name, n)
	default:
		return strings.TrimPrefix(r.typ(name, "", n, false), "*")
	}
	r.order = append(r.order, name)
	return name
}

func (r *resp) object(name string, n *node) {
	props := n.fields["properties"]
	required := map[string]bool{}
	for _, k := range n.strings("required") {
		required[k] = true
	}
	var body bytes.Buffer
	if props != nil {
		for _, k := range props.keys {
			prop := props.fields[k]
			if discriminators[k] && (len(prop.strings("enum")) == 1 || prop.str("const") != "") || skipped(prop) {
				continue // a discriminator with one value: the type already says it
			}
			t := r.typ(name+pascal(k), k, prop, !required[k])
			if o, ok := r.fields[name+"."+k]; ok {
				t = o
			} else if o, ok := r.fields[k]; ok {
				t = o
			}
			fmt.Fprintf(&body, "%s\t%s %s `json:%q`\n", comment("\t", prop), pascal(k), t, k)
		}
	}
	doc := comment("", n)
	if doc == "" {
		doc = fmt.Sprintf("// %s is read from the API. Fields it does not send are zero.\n", name)
	}
	r.p("%stype %s struct {\n%s}\n\n", doc, name, body.String())
}

// union writes a discriminated union as one pointer per variant, the way Job
// models statuses: exactly one is set, or none for a variant newer than this SDK.
func (r *resp) union(name string, n *node) {
	disc := n.get("discriminator").str("propertyName")
	mapping := n.get("discriminator", "mapping")
	if disc == "" || mapping == nil {
		log.Fatalf("genparams: union %s has no discriminator mapping", name)
	}
	kind := r.enumType(name+pascal(disc), n, mapping.keys)
	type variant struct{ key, field, typ string }
	var vs []variant
	for _, k := range mapping.keys {
		ref := strings.TrimPrefix(mapping.fields[k].value.(string), "#/components/schemas/")
		vs = append(vs, variant{k, pascal(k), r.named(ref, r.schemas.fields[ref])})
	}
	if c := comment("", n); c != "" {
		r.p("%s//\n", c)
	}
	r.p("// %s holds one variant, matching %s. A %s newer than this SDK sets\n// %s and leaves every variant nil.\ntype %s struct {\n\t%s %s\n", name, pascal(disc), disc, pascal(disc), name, pascal(disc), kind)
	for _, v := range vs {
		r.p("\t%s *%s\n", v.field, v.typ)
	}
	r.p("}\n\n")
	r.p("// UnmarshalJSON reads the variant %s names.\nfunc (u *%s) UnmarshalJSON(b []byte) error {\n", disc, name)
	r.p("\tvar d struct {\n\t\tV %s `json:%q`\n\t}\n\tif err := json.Unmarshal(b, &d); err != nil {\n\t\treturn err\n\t}\n", kind, disc)
	r.p("\t*u = %s{%s: d.V}\n\tswitch d.V {\n", name, pascal(disc))
	for _, v := range vs {
		r.p("\tcase %q:\n\t\tu.%s = new(%s)\n\t\treturn json.Unmarshal(b, u.%s)\n", v.key, v.field, v.typ, v.field)
	}
	r.p("\t}\n\treturn nil\n}\n\n")
}
