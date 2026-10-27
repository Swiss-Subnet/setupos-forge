// Command setupos-forge prepares and validates IC-OS SetupOS installer images.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
)

const usage = `setupos-forge prepares and validates IC-OS SetupOS installer images.

usage:
  setupos-forge derive    -bmc-mac MAC -prefix PREFIX [-env ENV] [-slot N] [-json]
  setupos-forge validate  [FILE]
  setupos-forge render    -prefix PREFIX -gateway ADDR -reward-type TYPE [...]
  setupos-forge build     -image IMG -config config.ini [-revision SHA] [-out IMG]
  setupos-forge inspect   -image IMG [-revision SHA]
  setupos-forge version

exit status:
  0  clean
  1  findings, or the command failed
  2  usage error
`

// errFindings separates "the input is wrong" from "the tool broke".
var errFindings = errors.New("findings reported")

// version is stamped at build time. See the dist recipe in the justfile.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}

	var err error
	switch os.Args[1] {
	case "derive":
		err = runDerive(os.Args[2:])
	case "validate":
		err = runValidate(os.Args[2:])
	case "render":
		err = runRender(os.Args[2:])
	case "build":
		err = runBuild(os.Args[2:])
	case "inspect":
		err = runInspect(os.Args[2:])
	case "version", "-version", "--version":
		fmt.Printf("setupos-forge %s\n", version)
		return
	case "help", "-h", "--help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "setupos-forge: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}

	switch {
	case errors.Is(err, flag.ErrHelp):
		os.Exit(2)
	case errors.Is(err, errFindings):
		os.Exit(1)
	case err != nil:
		fmt.Fprintf(os.Stderr, "setupos-forge: %v\n", err)
		os.Exit(1)
	}
}
