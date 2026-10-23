package main

import (
	"flag"
	"fmt"
	"os"
)

// Set at build time with -ldflags "-X main.version=… -X main.commit=… -X main.date=…".
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("vitals", flag.ContinueOnError)
	fs.SetOutput(stderr)
	showVersion := fs.Bool("version", false, "print version, commit, and build date, then exit")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *showVersion {
		fmt.Fprintf(stdout, "vitals %s (commit %s, built %s)\n", version, commit, date)
		return 0
	}
	fmt.Fprintln(stderr, "vitals: no server yet; run with -version")
	return 0
}
