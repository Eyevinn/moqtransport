package main

import (
	"flag"
	"fmt"
	"os"
	"path"
	"regexp"
	"strings"
)

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
	decl := flag.String("decl", "", "declaration file to read the message set from")
	directory := flag.String("dir", ".", "directory to write the generated files to")
	flag.Parse()

	if *decl == "" {
		fmt.Fprintln(os.Stderr, "wiregen: -decl is required")
		os.Exit(2)
	}

	pkg, msgs, err := parseDeclarations(*decl)
	if err != nil {
		fmt.Fprintf(os.Stderr, "reading %s: %v\n", *decl, err)
		os.Exit(1)
	}

	args := strings.Join(os.Args[1:], " ")
	suffix := fmt.Sprintf("V%d", *draft)

	for _, msg := range msgs {
		src, err := generate(msg, pkg, suffix, args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "generating %s: %v\n", msg.name, err)
			os.Exit(1)
		}

		filename := path.Join(*directory, fmt.Sprintf("%s_v%d.go", toSnakeCase(msg.name), *draft))
		if err := os.WriteFile(filename, src, 0o644); err != nil {
			fmt.Fprintf(os.Stderr, "writing %s: %v\n", filename, err)
			os.Exit(1)
		}
		fmt.Println(filename)
	}
}
