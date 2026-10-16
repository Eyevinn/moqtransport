package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"strconv"
	"strings"
)

// messageDecl is one message declaration read out of the declaration file.
type messageDecl struct {
	name   string
	fields []fieldDecl

	// drafts lists the drafts the message exists in, from a
	// `//wiregen:drafts N[,M...]` line in its doc comment. Empty means every
	// draft the declaration file is generated for.
	drafts []int
}

// draftsDirective marks a message that exists only in some drafts, e.g.
// PUBLISH_STATE_NOTIFY, which draft-20 added.
const draftsDirective = "//wiregen:drafts "

// inDraft reports whether the message exists in draft.
func (m messageDecl) inDraft(draft int) bool {
	return len(m.drafts) == 0 || slices.Contains(m.drafts, draft)
}

// fieldDecl is one struct field. Only the name and the tag matter: the codec a
// field uses is named by its `proto` tag, never inferred from its Go type, so
// the generator never needs type information.
type fieldDecl struct {
	name string
	tag  reflect.StructTag
}

// protoFields returns the fields carrying a proto tag, i.e. the fields that
// appear on the wire, in declaration order.
func (m messageDecl) protoFields() []fieldDecl {
	var fields []fieldDecl
	for _, f := range m.fields {
		if _, ok := f.tag.Lookup("proto"); ok {
			fields = append(fields, f)
		}
	}
	return fields
}

// wireFields returns the fields on the wire in draft: the proto-tagged ones,
// less any whose `drafts` tag names other drafts only. That tag is how one
// struct serves drafts whose bodies differ by a field, as GOAWAY's Request ID
// does between draft-18 and draft-19.
func (m messageDecl) wireFields(draft int) ([]fieldDecl, error) {
	var fields []fieldDecl
	for _, f := range m.protoFields() {
		drafts, err := parseDraftList(f.tag.Get("drafts"))
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", m.name, f.name, err)
		}
		if len(drafts) == 0 || slices.Contains(drafts, draft) {
			fields = append(fields, f)
		}
	}
	return fields, nil
}

// parseDraftList reads a comma-separated list of draft numbers. An empty
// string is an empty list.
func parseDraftList(s string) ([]int, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var drafts []int
	for _, part := range strings.Split(s, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil {
			return nil, fmt.Errorf("invalid draft list %q", s)
		}
		drafts = append(drafts, n)
	}
	return drafts, nil
}

// draftsFromDoc returns the drafts named by a `//wiregen:drafts` line in doc.
func draftsFromDoc(doc *ast.CommentGroup) ([]int, error) {
	if doc == nil {
		return nil, nil
	}
	for _, c := range doc.List {
		if list, ok := strings.CutPrefix(c.Text, draftsDirective); ok {
			return parseDraftList(list)
		}
	}
	return nil, nil
}

// parseDeclarations reads the declaration file as source and returns its
// package name and every struct type it declares, in declaration order.
//
// It parses rather than reflects deliberately. Reflection would mean linking
// the package the generator writes into, so `go generate` could only run when
// its own output already compiled -- which is never true in the one case that
// matters, a change to the codec method signatures. Reading the syntax breaks
// that cycle: the generator depends on the declaration file alone.
//
// Every struct in the file gets a codec. A message whose body cannot be
// expressed as a flat field list is hand-written and lives in its own file,
// which keeps it out of the generated set without needing a marker.
func parseDeclarations(path string) (string, []messageDecl, error) {
	return parseDecls(path, nil)
}

// parseDecls is parseDeclarations with the source overridable, so tests can
// pass a snippet instead of a file. src follows go/parser: nil reads name.
func parseDecls(name string, src any) (string, []messageDecl, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution|parser.ParseComments)
	if err != nil {
		return "", nil, err
	}

	var msgs []messageDecl
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			typeSpec, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			structType, ok := typeSpec.Type.(*ast.StructType)
			if !ok {
				continue
			}
			fields, err := parseFields(fset, typeSpec.Name.Name, structType)
			if err != nil {
				return "", nil, err
			}
			// A lone type's doc comment attaches to the GenDecl, not to the
			// TypeSpec, unless the declaration is parenthesized.
			doc := typeSpec.Doc
			if doc == nil && len(gen.Specs) == 1 {
				doc = gen.Doc
			}
			drafts, err := draftsFromDoc(doc)
			if err != nil {
				return "", nil, fmt.Errorf("%s: %w", typeSpec.Name.Name, err)
			}
			msgs = append(msgs, messageDecl{name: typeSpec.Name.Name, fields: fields, drafts: drafts})
		}
	}
	return file.Name.Name, msgs, nil
}

func parseFields(fset *token.FileSet, typeName string, structType *ast.StructType) ([]fieldDecl, error) {
	var fields []fieldDecl
	for _, f := range structType.Fields.List {
		if len(f.Names) == 0 {
			// An embedded field has no name to generate against. It is only an
			// error if it is meant to be serialized.
			if f.Tag != nil {
				tag, err := unquoteTag(f.Tag)
				if err != nil {
					return nil, err
				}
				if _, ok := tag.Lookup("proto"); ok {
					return nil, fmt.Errorf("%s: embedded field at %v carries a proto tag",
						typeName, fset.Position(f.Pos()))
				}
			}
			continue
		}

		var tag reflect.StructTag
		if f.Tag != nil {
			var err error
			tag, err = unquoteTag(f.Tag)
			if err != nil {
				return nil, err
			}
		}
		// One declaration can name several fields, and each is serialized in
		// turn: `Group, Object uint64` is two wire fields, not one.
		for _, name := range f.Names {
			fields = append(fields, fieldDecl{name: name.Name, tag: tag})
		}
	}
	return fields, nil
}

func unquoteTag(lit *ast.BasicLit) (reflect.StructTag, error) {
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", fmt.Errorf("malformed struct tag %s: %w", lit.Value, err)
	}
	return reflect.StructTag(value), nil
}
