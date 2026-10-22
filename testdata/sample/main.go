// Package main is a tiny program with deliberately old dependencies, used by
// the end-to-end test.
package main

import (
	"fmt"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

func main() {
	fmt.Println(cases.Title(language.English).String("depbot sample"))
}
