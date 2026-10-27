package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/swiss-subnet/setupos-forge/pkg/config"
)

func runValidate(args []string) error {
	fs := flag.NewFlagSet("validate", flag.ContinueOnError)
	strict := fs.Bool("strict", false, "treat warnings as findings too")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var r io.Reader = os.Stdin
	switch fs.NArg() {
	case 0:
	case 1:
		if path := fs.Arg(0); path != "-" {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			r = f
		}
	default:
		return fmt.Errorf("validate takes at most one file")
	}

	cfg, ps, err := config.Parse(r)
	if err != nil {
		return err
	}
	ps = append(ps, cfg.Validate()...)

	return report(ps, *strict)
}

func report(ps config.Problems, strict bool) error {
	var errs, warns int
	for _, p := range ps {
		paint := cerr.warn
		if p.Severity == config.SeverityError {
			paint = cerr.bad
			errs++
		} else {
			warns++
		}
		fmt.Fprintln(os.Stderr, paint(p.String()))
	}

	if errs == 0 && warns == 0 {
		fmt.Println(cout.ok("config.ini ok"))
		return nil
	}
	fmt.Fprintf(os.Stderr, "\n%d error(s), %d warning(s)\n", errs, warns)
	if errs > 0 || (strict && warns > 0) {
		return errFindings
	}
	return nil
}
