package main

import (
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

var update = flag.Bool("update", false, "rewrite the golden files in testdata")

// allTags exercises every entry in codecs, so a template change shows up as a
// golden diff rather than silently re-encoding the whole message set.
type allTags struct {
	Ignored        string   `json:"ignored"`
	RequestID      uint64   `proto:"varint"`
	TrackName      []byte   `proto:"tlv_bytes"`
	ErrorReason    string   `proto:"tlv_string"`
	TrackNamespace [][]byte `proto:"ntlv_bytes"`
	EndOfTrack     bool     `proto:"bool"`
	EndLocation    Location `proto:"moq_location"`
	Parameters     KVPList  `proto:"moq_kvp_list"`
	Properties     KVPList  `proto:"moq_kvp_list_no_length"`
}

// noFields has nothing on the wire; its codecs must still compile.
type noFields struct {
	Ignored string
}

// Location and KVPList stand in for the hand-written types in the target
// package. Only their names reach the generated source.
type (
	Location struct{}
	KVPList  []struct{}
)

func TestGenerateGolden(t *testing.T) {
	for _, tc := range []struct {
		name   string
		typ    reflect.Type
		suffix string
	}{
		{"all_tags_v18", reflect.TypeOf(allTags{}), "V18"},
		{"all_tags_v20", reflect.TypeOf(allTags{}), "V20"},
		{"no_fields_v18", reflect.TypeOf(noFields{}), "V18"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := generate(tc.typ, "wire2", tc.suffix, "-draft 18")
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

func TestGenerateRejectsUnknownTag(t *testing.T) {
	type bad struct {
		X uint64 `proto:"no_such_codec"`
	}
	if _, err := generate(reflect.TypeOf(bad{}), "wire2", "V18", ""); err == nil {
		t.Fatal("expected an error for an unknown proto tag")
	}
}

func TestGenerateRejectsEmptyPackage(t *testing.T) {
	if _, err := generate(reflect.TypeOf(noFields{}), "", "V18", ""); err == nil {
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
