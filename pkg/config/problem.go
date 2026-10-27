package config

import "fmt"

type Severity int

const (
	SeverityError Severity = iota
	SeverityWarning
)

func (s Severity) String() string {
	if s == SeverityWarning {
		return "warning"
	}
	return "error"
}

// Enforcer names the upstream file, so a finding can be checked against the
// source rather than trusted.
type Problem struct {
	Severity Severity
	Key      string // config.ini key, empty when the finding is file-wide
	Message  string
	Enforcer string // upstream path that rejects this
}

func (p Problem) String() string {
	key := p.Key
	if key == "" {
		key = "config.ini"
	}
	return fmt.Sprintf("%s: %s: %s (%s)", p.Severity, key, p.Message, p.Enforcer)
}

type Problems []Problem

func (ps Problems) HasErrors() bool {
	for _, p := range ps {
		if p.Severity == SeverityError {
			return true
		}
	}
	return false
}

func (ps *Problems) add(s Severity, key, enforcer, format string, args ...any) {
	*ps = append(*ps, Problem{
		Severity: s,
		Key:      key,
		Message:  fmt.Sprintf(format, args...),
		Enforcer: enforcer,
	})
}
