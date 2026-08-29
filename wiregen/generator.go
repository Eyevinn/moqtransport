// Command wiregen generates append/parse codecs for the MOQT control and
// data messages declared in internal/wire2.
//
// The generator design, its codec tag vocabulary and the template approach
// are derived from wiregen in github.com/mengelbart/moqtransport, which is
// distributed under the MIT license, Copyright (c) 2023 Mathis Engelbart.
// The templates here emit MOQT vi64 varints, delegate Key-Value-Pair lists
// to the hand-written codecs in internal/wire2, and the generator resolves
// its own imports instead of shelling out to goimports.
package main

import (
	"bytes"
	"errors"
	"fmt"
	"go/format"
	"os"
	"reflect"
	"sort"
	"strings"
	"text/template"
)

var errInvalidPackage = errors.New("invalid package name")

type unknownProtoTypeErr string

func (e unknownProtoTypeErr) Error() string {
	return fmt.Sprintf("unknown proto type: %s", string(e))
}

// codec is the generated append/parse pair for one proto tag, together with
// the imports those two snippets need. Keeping the imports next to the
// templates lets the generator emit an exact import block and format with
// go/format, so the module needs no goimports dependency.
type codec struct {
	appendTmpl *template.Template
	parseTmpl  *template.Template
	imports    []string
}

const vi64Import = "github.com/Eyevinn/locmaf/vi64"

func tmpl(name, body string) *template.Template {
	return template.Must(template.New(name).Parse(body))
}

// codecs maps a `proto:"..."` struct tag to its codec.
//
// The append templates write into buf; the parse templates consume from data,
// which is always the message body sliced to its exact length, and may keep
// sub-slices of it: the parser allocates a fresh body buffer per message, so
// a parsed message owns its bytes.
var codecs = map[string]codec{
	"varint": {
		appendTmpl: tmpl("varint_append", `	buf = vi64.Append(buf, uint64(m.{{ .Field }}))
`),
		parseTmpl: tmpl("varint_parse", `	m.{{ .Field }}, n, err = vi64.Parse(data)
	if err != nil {
		return err
	}
	data = data[n:]
`),
		imports: []string{vi64Import},
	},

	"tlv_bytes": {
		appendTmpl: tmpl("tlv_bytes_append", `	buf = vi64.Append(buf, uint64(len(m.{{ .Field }})))
	buf = append(buf, m.{{ .Field }}...)
`),
		parseTmpl: tmpl("tlv_bytes_parse", `	var {{ .Var }}Length uint64
	{{ .Var }}Length, n, err = vi64.Parse(data)
	if err != nil {
		return err
	}
	data = data[n:]
{{ if .Max }}	if {{ .Var }}Length > {{ .Max }} {
		return errFieldTooLong
	}
{{ end }}	if uint64(len(data)) < {{ .Var }}Length {
		return io.ErrUnexpectedEOF
	}
	m.{{ .Field }} = data[:{{ .Var }}Length]
	data = data[{{ .Var }}Length:]
`),
		imports: []string{vi64Import, "io"},
	},

	"tlv_string": {
		appendTmpl: tmpl("tlv_string_append", `	buf = vi64.Append(buf, uint64(len(m.{{ .Field }})))
	buf = append(buf, m.{{ .Field }}...)
`),
		parseTmpl: tmpl("tlv_string_parse", `	var {{ .Var }}Length uint64
	{{ .Var }}Length, n, err = vi64.Parse(data)
	if err != nil {
		return err
	}
	data = data[n:]
{{ if .Max }}	if {{ .Var }}Length > {{ .Max }} {
		return errFieldTooLong
	}
{{ end }}	if uint64(len(data)) < {{ .Var }}Length {
		return io.ErrUnexpectedEOF
	}
	m.{{ .Field }} = string(data[:{{ .Var }}Length])
	data = data[{{ .Var }}Length:]
`),
		imports: []string{vi64Import, "io"},
	},

	"ntlv_bytes": {
		appendTmpl: tmpl("ntlv_bytes_append", `	buf = vi64.Append(buf, uint64(len(m.{{ .Field }})))
	for _, v := range m.{{ .Field }} {
		buf = vi64.Append(buf, uint64(len(v)))
		buf = append(buf, v...)
	}
`),
		parseTmpl: tmpl("ntlv_bytes_parse", `	var {{ .Var }}Count uint64
	{{ .Var }}Count, n, err = vi64.Parse(data)
	if err != nil {
		return err
	}
	data = data[n:]
{{ if .Max }}	if {{ .Var }}Count > {{ .Max }} {
		return errTooManyFields
	}
{{ end }}	// The count cannot exceed the remaining bytes: every element costs at
	// least one byte, so this bounds the allocation without trusting it.
	if {{ .Var }}Count > uint64(len(data)) {
		return io.ErrUnexpectedEOF
	}
	m.{{ .Field }} = make([][]byte, 0, {{ .Var }}Count)
	for range {{ .Var }}Count {
		var length uint64
		length, n, err = vi64.Parse(data)
		if err != nil {
			return err
		}
		data = data[n:]
		if uint64(len(data)) < length {
			return io.ErrUnexpectedEOF
		}
		m.{{ .Field }} = append(m.{{ .Field }}, data[:length])
		data = data[length:]
	}
`),
		imports: []string{vi64Import, "io"},
	},

	// A count-prefixed Key-Value-Pair block, as used by Parameters.
	"moq_kvp_list": {
		appendTmpl: tmpl("moq_kvp_list_append", `	buf = m.{{ .Field }}.appendNum(buf)
`),
		parseTmpl: tmpl("moq_kvp_list_parse", `	n, err = m.{{ .Field }}.parseNum(data)
	if err != nil {
		return err
	}
	data = data[n:]
`),
	},

	// A Key-Value-Pair block with neither count nor length prefix, running to
	// the end of the message body. Only ever the last field of a message.
	"moq_kvp_list_no_length": {
		appendTmpl: tmpl("moq_kvp_list_no_length_append", `	buf = m.{{ .Field }}.appendDelta(buf)
`),
		parseTmpl: tmpl("moq_kvp_list_no_length_parse", `	n, err = m.{{ .Field }}.parseAll(data)
	if err != nil {
		return err
	}
	data = data[n:]
`),
	},

	"bool": {
		appendTmpl: tmpl("bool_append", `	if m.{{ .Field }} {
		buf = append(buf, 1)
	} else {
		buf = append(buf, 0)
	}
`),
		parseTmpl: tmpl("bool_parse", `	if len(data) < 1 {
		return io.ErrUnexpectedEOF
	}
	if data[0] > 1 {
		return errInvalidBoolValue
	}
	m.{{ .Field }} = data[0] == 1
	data = data[1:]
`),
		imports: []string{"io"},
	},

	// An optional trailing Redirect structure, present in REQUEST_ERROR only
	// when the Error Code is REDIRECT. Since it is the last field, the message
	// body length tells the parser whether it is there.
	"moq_opt_redirect": {
		appendTmpl: tmpl("moq_opt_redirect_append", `	if m.{{ .Field }} != nil {
		buf = m.{{ .Field }}.append(buf)
	}
`),
		parseTmpl: tmpl("moq_opt_redirect_parse", `	if len(data) > 0 {
		m.{{ .Field }} = &Redirect{}
		n, err = m.{{ .Field }}.parse(data)
		if err != nil {
			return err
		}
		data = data[n:]
	}
`),
	},

	"moq_location": {
		appendTmpl: tmpl("moq_location_append", `	buf = m.{{ .Field }}.append(buf)
`),
		parseTmpl: tmpl("moq_location_parse", `	n, err = m.{{ .Field }}.parse(data)
	if err != nil {
		return err
	}
	data = data[n:]
`),
	},
}

type generator struct {
	pkg          string
	methodSuffix string
	args         string

	imports map[string]struct{}
	body    bytes.Buffer
}

// generate emits the append/parse pair for typ. methodSuffix distinguishes the
// draft a declaration set belongs to, so several sets can live in one package.
func generate(typ reflect.Type, pkg, methodSuffix, args string) ([]byte, error) {
	if pkg == "" {
		return nil, errInvalidPackage
	}
	g := &generator{
		pkg:          pkg,
		methodSuffix: methodSuffix,
		args:         args,
		imports:      map[string]struct{}{},
	}
	if err := g.generateAppend(typ); err != nil {
		return nil, err
	}
	if err := g.generateParse(typ); err != nil {
		return nil, err
	}
	return g.format()
}

func (g *generator) generateAppend(typ reflect.Type) error {
	g.printf("func (m *%s) append%s(buf []byte) []byte {\n", typ.Name(), g.methodSuffix)
	for _, f := range protoFields(typ) {
		c, err := g.codecFor(f)
		if err != nil {
			return err
		}
		if err := c.appendTmpl.Execute(&g.body, templateData(f)); err != nil {
			return err
		}
	}
	g.printf("\treturn buf\n}\n\n")
	return nil
}

func (g *generator) generateParse(typ reflect.Type) error {
	fields := protoFields(typ)

	g.printf("func (m *%s) parse%s(data []byte) error {\n", typ.Name(), g.methodSuffix)
	// A message with no wire fields parses nothing, so the scratch variables
	// would be unused.
	if len(fields) > 0 {
		g.printf("\tvar err error\n\tvar n int\n\n")
	}
	for _, f := range fields {
		c, err := g.codecFor(f)
		if err != nil {
			return err
		}
		if err := c.parseTmpl.Execute(&g.body, templateData(f)); err != nil {
			return err
		}
		g.printf("\n")
	}
	if len(fields) > 0 {
		// Trailing bytes mean the declared message length disagrees with the
		// declaration, which draft-18 makes a PROTOCOL_VIOLATION.
		g.printf("\tif len(data) > 0 {\n\t\treturn errTrailingBytes\n\t}\n")
	}
	g.printf("\treturn nil\n}\n")
	return nil
}

func (g *generator) codecFor(f reflect.StructField) (codec, error) {
	proto := f.Tag.Get("proto")
	c, ok := codecs[proto]
	if !ok {
		return codec{}, unknownProtoTypeErr(proto)
	}
	for _, imp := range c.imports {
		g.imports[imp] = struct{}{}
	}
	return c, nil
}

// templateData exposes the field name, a lowerCamel variant safe to use as a
// local variable name in the generated parser, and the optional `max` tag.
//
// `max` carries a draft-18 upper bound that the receiver MUST enforce by
// closing the session: a byte length for tlv_bytes and tlv_string (Reason
// Phrase is 1024, New Session URI 8192), an element count for ntlv_bytes (a
// Track Namespace holds at most 32 fields). Without the tag no bound is
// generated beyond what the message body itself implies.
func templateData(f reflect.StructField) map[string]string {
	return map[string]string{
		"Field": f.Name,
		"Var":   strings.ToLower(f.Name[:1]) + f.Name[1:],
		"Max":   f.Tag.Get("max"),
	}
}

// protoFields returns the fields of typ carrying a proto tag, i.e. the fields
// that appear on the wire, in declaration order.
func protoFields(typ reflect.Type) []reflect.StructField {
	var fields []reflect.StructField
	for i := range typ.NumField() {
		f := typ.Field(i)
		if _, ok := f.Tag.Lookup("proto"); ok {
			fields = append(fields, f)
		}
	}
	return fields
}

func (g *generator) printf(format string, args ...any) {
	fmt.Fprintf(&g.body, format, args...)
}

func (g *generator) format() ([]byte, error) {
	var out bytes.Buffer
	fmt.Fprintf(&out, "// Code generated by \"wiregen %s\"; DO NOT EDIT.\n\npackage %s\n\n", g.args, g.pkg)

	if len(g.imports) > 0 {
		std, ext := []string{}, []string{}
		for imp := range g.imports {
			if strings.Contains(strings.SplitN(imp, "/", 2)[0], ".") {
				ext = append(ext, imp)
			} else {
				std = append(std, imp)
			}
		}
		sort.Strings(std)
		sort.Strings(ext)
		out.WriteString("import (\n")
		for _, imp := range std {
			fmt.Fprintf(&out, "\t%q\n", imp)
		}
		if len(std) > 0 && len(ext) > 0 {
			out.WriteString("\n")
		}
		for _, imp := range ext {
			fmt.Fprintf(&out, "\t%q\n", imp)
		}
		out.WriteString(")\n\n")
	}
	out.Write(g.body.Bytes())

	src, err := format.Source(out.Bytes())
	if err != nil {
		fmt.Fprintln(os.Stderr, out.String())
		return nil, fmt.Errorf("formatting generated source: %w", err)
	}
	return src, nil
}
