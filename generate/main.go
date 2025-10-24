package main

import (
	"flag"
	"fmt"
	"os"
	"path"
	"reflect"
	"regexp"
	"strings"

	"github.com/mengelbart/moqtransport/internal/wire2"
	"github.com/mengelbart/protogen"
)

var (
	msgs = []any{
		wire2.ClientSetup{},
		wire2.ServerSetup{},
		wire2.GoAway{},
		wire2.MaxRequestID{},
		wire2.RequestsBlocked{},
		wire2.RequestOk{},
		wire2.RequestError{},
		wire2.Subscribe{},
		wire2.SubscribeOk{},
		wire2.SubscribeUpdate{},
		wire2.Unsubscribe{},
		wire2.Publish{},
		wire2.PublishOk{},
		wire2.PublishDone{},
		// &wire2.Fetch{}, // TODO
		wire2.FetchOk{},
		wire2.FetchCancel{},
		wire2.TrackStatus{},
		wire2.PublishNamespace{},
		wire2.PublishNamespaceDone{},
		wire2.PublishNamespaceCancel{},
		wire2.SubscribeNamespace{},
		wire2.UnsubscribeNamespace{},
	}
)

var matchFirstCap = regexp.MustCompile("(.)([A-Z][a-z]+)")
var matchAllCap = regexp.MustCompile("([a-z0-9])([A-Z])")

func toSnakeCase(str string) string {
	snake := matchFirstCap.ReplaceAllString(str, "${1}_${2}")
	snake = matchAllCap.ReplaceAllString(snake, "${1}_${2}")
	return strings.ToLower(snake)
}

func main() {
	version := flag.Int("version", 15, "version suffix for generated files and methods")
	directory := flag.String("dir", ".", "directory to save the generated files")
	flag.Parse()

	for _, m := range msgs {
		mt := reflect.TypeOf(m)
		format, err := protogen.Generate(mt, "wire2", fmt.Sprintf("_v%v", *version))
		if err != nil {
			panic(err)
		}

		filename := toSnakeCase(mt.Name()) + fmt.Sprintf("_v%v", *version) + ".go"
		filename = path.Join(*directory, filename)
		fmt.Println(filename)

		if err := os.WriteFile(filename, format, 0o644); err != nil {
			panic(err)
		}
	}
}
