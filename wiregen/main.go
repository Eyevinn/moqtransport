package main

import (
	"flag"
	"fmt"
	"os"
	"path"
	"reflect"
	"regexp"
	"strings"

	"github.com/Eyevinn/moqtransport/internal/wire2"
)

// msgs lists the message declarations to generate codecs for. Every entry must
// be a struct in the target package whose wire fields carry `proto` tags.
//
// It mirrors internal/wire2/messages_v18.go. Messages whose body is
// conditional on a sibling field are hand-written and deliberately absent:
// Fetch, whose optional structures are selected by Fetch Type.
var msgs = []any{
	wire2.Setup{},
	wire2.GoAwayCtrl{},
	wire2.GoAwayReq{},

	wire2.Subscribe{},
	wire2.SubscribeOk{},
	wire2.TrackStatus{},
	wire2.RequestUpdate{},
	wire2.Publish{},
	wire2.PublishDone{},
	wire2.FetchOk{},
	wire2.PublishNamespace{},
	wire2.Namespace{},
	wire2.NamespaceDone{},
	wire2.SubscribeNamespace{},
	wire2.SubscribeTracks{},
	wire2.PublishBlocked{},
	wire2.RequestOk{},
	wire2.PublishOk{},
	wire2.RequestError{},

	wire2.FetchHeader{},
	wire2.Padding{},
}

var (
	matchFirstCap = regexp.MustCompile("(.)([A-Z][a-z]+)")
	matchAllCap   = regexp.MustCompile("([a-z0-9])([A-Z])")
)

func toSnakeCase(str string) string {
	snake := matchFirstCap.ReplaceAllString(str, "${1}_${2}")
	snake = matchAllCap.ReplaceAllString(snake, "${1}_${2}")
	return strings.ToLower(snake)
}

func main() {
	draft := flag.Int("draft", 18, "MOQT draft the declaration set targets; selects the file and method suffix")
	directory := flag.String("dir", ".", "directory to write the generated files to")
	pkg := flag.String("pkg", "wire2", "package name of the generated files")
	flag.Parse()

	args := strings.Join(os.Args[1:], " ")
	suffix := fmt.Sprintf("V%d", *draft)

	for _, m := range msgs {
		mt := reflect.TypeOf(m)
		src, err := generate(mt, *pkg, suffix, args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "generating %s: %v\n", mt.Name(), err)
			os.Exit(1)
		}

		filename := path.Join(*directory, fmt.Sprintf("%s_v%d.go", toSnakeCase(mt.Name()), *draft))
		if err := os.WriteFile(filename, src, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "writing %s: %v\n", filename, err)
			os.Exit(1)
		}
		fmt.Println(filename)
	}
}
