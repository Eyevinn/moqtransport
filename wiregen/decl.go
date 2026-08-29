package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
)

// messageDecl is one message declaration read out of the declaration file.
type messageDecl struct {
	name   string
	fields []fieldDecl
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
	file, err := parser.ParseFile(fset, name, src, parser.SkipObjectResolution)
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
			msgs = append(msgs, messageDecl{name: typeSpec.Name.Name, fields: fields})
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
