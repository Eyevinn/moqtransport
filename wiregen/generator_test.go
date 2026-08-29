package main

import (
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// declSource exercises every entry in codecs, so a template change shows up as
// a golden diff rather than silently re-encoding the whole message set.
//
// It is parsed as source, not reflected over, which is why the stand-in types
// its fields name never have to exist.
const declSource = `package wire2

type allTags struct {
	Ignored        string   ` + "`json:\"ignored\"`" + `
	RequestID      uint64   ` + "`proto:\"varint\"`" + `
	TrackName      []byte   ` + "`proto:\"tlv_bytes\"`" + `
	ErrorReason    string   ` + "`proto:\"tlv_string\" max:\"1024\"`" + `
	TrackNamespace [][]byte ` + "`proto:\"ntlv_bytes\" max:\"32\"`" + `
	EndOfTrack     bool     ` + "`proto:\"bool\"`" + `
	EndLocation    Location ` + "`proto:\"moq_location\"`" + `
	Parameters     Parameters ` + "`proto:\"moq_params\"`" + `
	Properties     KVPList  ` + "`proto:\"moq_kvp_list_no_length\"`" + `
	Redirect       *Redirect ` + "`proto:\"moq_opt_redirect\"`" + `
}

// noFields has nothing on the wire; its codecs must still compile.
type noFields struct {
	Ignored string
}
`

func declFor(t *testing.T, name string) messageDecl {
	t.Helper()
	_, msgs, err := parseDecls("decl.go", declSource)
	if err != nil {
		t.Fatalf("parseDecls: %v", err)
	}
	for _, m := range msgs {
		if m.name == name {
			return m
		}
	}
	t.Fatalf("no declaration named %q", name)
	return messageDecl{}
}

func TestGenerateGolden(t *testing.T) {
	for _, tc := range []struct {
		name    string
		message string
		suffix  string
	}{
		{"all_tags_v18", "allTags", "V18"},
		{"all_tags_v20", "allTags", "V20"},
		{"no_fields_v18", "noFields", "V18"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := generate(declFor(t, tc.message), "wire2", tc.suffix, "-draft 18")
			if err != nil {
				t.Fatalf("generate: %v", err)
			}
			golden := filepath.Join("testdata", tc.name+".go.txt")
			if *update {
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatalf("writing golden: %v", err)
				}
				return
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("reading golden (run: go test ./wiregen -update): %v", err)
			}
			if string(got) != string(want) {
				t.Errorf("generated output differs from %s:\n--- got ---\n%s", golden, got)
			}
		})
	}
}

func TestParseDeclarations(t *testing.T) {
	pkg, msgs, err := parseDecls("decl.go", declSource)
	if err != nil {
		t.Fatalf("parseDecls: %v", err)
	}
	if pkg != "wire2" {
		t.Errorf("package = %q, want wire2", pkg)
	}
	if len(msgs) != 2 || msgs[0].name != "allTags" || msgs[1].name != "noFields" {
		t.Fatalf("got %d messages, want allTags then noFields", len(msgs))
	}

	// Declaration order must survive, since it is the wire order.
	want := []string{"RequestID", "TrackName", "ErrorReason", "TrackNamespace",
		"EndOfTrack", "EndLocation", "Parameters", "Properties", "Redirect"}
	fields := msgs[0].protoFields()
	if len(fields) != len(want) {
		t.Fatalf("got %d proto fields, want %d", len(fields), len(want))
	}
	for i, name := range want {
		if fields[i].name != name {
			t.Errorf("field %d = %q, want %q", i, fields[i].name, name)
		}
	}

	// An untagged field is not on the wire.
	if len(msgs[1].protoFields()) != 0 {
		t.Error("noFields should have no proto fields")
	}
	if fields[2].tag.Get("max") != "1024" {
		t.Errorf("max tag = %q, want 1024", fields[2].tag.Get("max"))
	}
}

// TestParseMultiNameField pins that one declaration naming several fields
// serializes each of them, in order.
func TestParseMultiNameField(t *testing.T) {
	src := `package wire2

type pair struct {
	Group, Object uint64 ` + "`proto:\"varint\"`" + `
}
`
	_, msgs, err := parseDecls("decl.go", src)
	if err != nil {
		t.Fatalf("parseDecls: %v", err)
	}
	fields := msgs[0].protoFields()
	if len(fields) != 2 || fields[0].name != "Group" || fields[1].name != "Object" {
		t.Fatalf("got %v, want Group then Object", fields)
	}
}

func TestParseRejectsTaggedEmbeddedField(t *testing.T) {
	src := `package wire2

type bad struct {
	Location ` + "`proto:\"moq_location\"`" + `
}
`
	if _, _, err := parseDecls("decl.go", src); err == nil {
		t.Fatal("expected an error for a proto-tagged embedded field")
	}
}

func TestParseIgnoresNonStructTypes(t *testing.T) {
	src := `package wire2

type Alias = uint64

type Named uint64

type real struct {
	X uint64 ` + "`proto:\"varint\"`" + `
}
`
	_, msgs, err := parseDecls("decl.go", src)
	if err != nil {
		t.Fatalf("parseDecls: %v", err)
	}
	if len(msgs) != 1 || msgs[0].name != "real" {
		t.Fatalf("got %d messages, want only real", len(msgs))
	}
}

func TestGenerateRejectsUnknownTag(t *testing.T) {
	src := `package wire2

type bad struct {
	X uint64 ` + "`proto:\"no_such_codec\"`" + `
}
`
	_, msgs, err := parseDecls("decl.go", src)
	if err != nil {
		t.Fatalf("parseDecls: %v", err)
	}
	if _, err := generate(msgs[0], "wire2", "V18", ""); err == nil {
		t.Fatal("expected an error for an unknown proto tag")
	}
}

func TestGenerateRejectsEmptyPackage(t *testing.T) {
	if _, err := generate(declFor(t, "noFields"), "", "V18", ""); err == nil {
		t.Fatal("expected an error for an empty package name")
	}
}

func TestToSnakeCase(t *testing.T) {
	for in, want := range map[string]string{
		"Subscribe":          "subscribe",
		"SubscribeOk":        "subscribe_ok",
		"PublishNamespace":   "publish_namespace",
		"GoAwayCtrl":         "go_away_ctrl",
		"FetchHeader":        "fetch_header",
		"SubscribeTracks":    "subscribe_tracks",
		"RequestUpdate":      "request_update",
		"PublishStateNotify": "publish_state_notify",
	} {
		if got := toSnakeCase(in); got != want {
			t.Errorf("toSnakeCase(%q) = %q, want %q", in, got, want)
		}
	}
}
