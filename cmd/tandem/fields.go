package main

import (
	"fmt"
	"io"
)

// writeFields prints each field followed by a NUL, the form bash reads with
// `IFS= read -r -d ”`. A field may hold a tab or a newline.
func writeFields(w io.Writer, fields ...string) {
	for _, f := range fields {
		fmt.Fprint(w, f, "\x00")
	}
}
