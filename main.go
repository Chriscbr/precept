package main

import (
	"os"

	"github.com/Chriscbr/precept/cmd"
)

// Version can be overridden at build time with -ldflags.
var Version = "development"

func main() {
	os.Exit(cmd.Execute(Version))
}
