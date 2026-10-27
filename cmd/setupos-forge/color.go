package main

import "os"

// Colour only when the stream is a terminal, so piping to a file or through a
// pager stays clean. NO_COLOR and TERM=dumb turn it off everywhere.
type palette struct{ on bool }

func paletteFor(f *os.File) palette {
	if os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return palette{}
	}
	info, err := f.Stat()
	if err != nil {
		return palette{}
	}
	return palette{on: info.Mode()&os.ModeCharDevice != 0}
}

var (
	cout = paletteFor(os.Stdout)
	cerr = paletteFor(os.Stderr)
)

func (p palette) wrap(code, s string) string {
	if !p.on {
		return s
	}
	return "\033[" + code + "m" + s + "\033[0m"
}

func (p palette) ok(s string) string   { return p.wrap("32", s) }
func (p palette) bad(s string) string  { return p.wrap("31", s) }
func (p palette) warn(s string) string { return p.wrap("33", s) }
func (p palette) bold(s string) string { return p.wrap("1", s) }
func (p palette) dim(s string) string  { return p.wrap("2", s) }
