package main

import (
	"fmt"
	"io"
	"strings"
)

// writeFields prints each field followed by a NUL, the form bash reads with
// `IFS= read -r -d ”`. A field may hold a tab or a newline. A NUL inside a
// field is dropped, as command substitution dropped it, so text a builder
// wrote cannot shift the fields after it.
func writeFields(w io.Writer, fields ...string) {
	for _, f := range fields {
		fmt.Fprint(w, strings.ReplaceAll(f, "\x00", ""), "\x00")
	}
}
