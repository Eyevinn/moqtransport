package main

import (
	"flag"
	"fmt"
	"os"
	"path"
	"reflect"
	"regexp"
	"strings"
)

// msgs lists the message declarations to generate codecs for. Every entry must
// be a struct in the target package whose wire fields carry `proto` tags.
//
// The list is populated together with the declaration file it mirrors.
var msgs = []any{}

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
