// Package selector holds the one file-filter semantics shared by every filter
// site (file checks, archive members, leak check). A Selector matches a subject
// string against per-pattern compiled RE2 regexes, unanchored:
//
//   - case-sensitive, unless the rule sets ignoreCase;
//   - exclude wins over include; an empty include list admits everything;
//   - an empty or uncompilable pattern is a load error, and all of a rule's bad
//     patterns are reported at once, named with the rule;
//   - patterns are never joined with "|": one bad entry would corrupt the whole
//     expression and an empty entry would match everything.
//
// A Selector is immutable and stateless after Compile - one Selector is shared
// by every worker goroutine, so it owns no buffer. Patterns that are complete
// literals skip the regex engine and are matched by substring scan. Under
// ignoreCase that scan needs a case-folded subject, which the CALLER owns: it
// puts each subject into a Scratch once and matches every selector against that
// Scratch. Callers that match case-sensitively need no Scratch and pay nothing
// for it; the folding is lazy, so even a Scratch caller pays nothing until a
// fold pattern actually needs the folded bytes.
//
// The package is a leaf: it imports nothing else in this repo, so config,
// readers and checks may all import it.
//
// # Union pre-filtering
//
// A caller that runs many selectors over the same subjects may want one
// multi-pattern literal pass in front of them, so that a subject no selector
// can match is skipped without consulting any of them. UnionLiterals is the
// only entry point for that: it reports the literals a selector contributes,
// and whether the selector may take part at all. A selector opts out when it
// has no include pattern (it admits everything), when an include pattern needs
// the regex engine, or when it matches case-insensitively - a pre-filter that
// folds case by lowercasing is strictly narrower than RE2's Unicode simple
// folding (U+017F folds onto "s", U+212A onto "k"), so a skip decided there
// would silently drop subjects the selector admits. Selectors that opt out
// contribute no skippable literals: a subject may be skipped only when every
// selector in the pass opted in and none of their literals matched.
package selector

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"regexp/syntax"
	"strings"
	"unicode/utf8"
)

// ErrEmptyPattern rejects the one pattern form the old filter sites disagreed
// about: an empty string matched everything as a regex and nothing as a
// literal. Patterns that only spell the empty string differently ("(?:)", "()",
// "x{0}") are the same error.
var ErrEmptyPattern = errors.New("empty pattern")

// ErrUnknownSubject rejects a subject other than "name" or "path".
var ErrUnknownSubject = errors.New(`unknown subject (want "name" or "path")`)

// Subject names the string a selector matches against. The dispatch site knows
// its own scope and extracts it: file scope passes the file's base name or its
// collection-relative path, archive scopes the member path or its base slice.
type Subject uint8

const (
	SubjectName Subject = iota // base name
	SubjectPath                // collection-relative path
)

func (s Subject) String() string {
	switch s {
	case SubjectName:
		return "name"
	case SubjectPath:
		return "path"
	default:
		return fmt.Sprintf("subject(%d)", uint8(s))
	}
}

// ParseSubject maps the declared subject onto its Subject. The empty string is
// the default, "name".
func ParseSubject(s string) (Subject, error) {
	switch s {
	case "", "name":
		return SubjectName, nil
	case "path":
		return SubjectPath, nil
	default:
		return SubjectName, ErrUnknownSubject
	}
}

// Spec is the declarative form of a selector, as it arrives from TOML: strings
// only, validated by Compile.
type Spec struct {
	Rule       string // rule name, used for error attribution only
	Subject    string // "name" (default) or "path"
	IgnoreCase bool
	Include    []string
	Exclude    []string
}

// Fault is one rejected field of a Spec. It is itself an error, so errors.As
// pulls it out of a CompileError and errors.Is reaches the sentinel below it.
type Fault struct {
	Field string // "include", "exclude" or "subject"
	Index int    // position in the list, -1 for "subject"
	Value string
	Err   error
}

func (f Fault) Error() string {
	if f.Index < 0 {
		return fmt.Sprintf("%s %q: %v", f.Field, f.Value, f.Err)
	}
	return fmt.Sprintf("%s[%d] %q: %v", f.Field, f.Index, f.Value, f.Err)
}

func (f Fault) Unwrap() error { return f.Err }

// CompileError reports every fault of one rule at once, so a config author sees
// all bad patterns from one run instead of one per run.
type CompileError struct {
	Rule   string
	Faults []Fault
}

func (e *CompileError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "selector for rule %q: ", e.Rule)
	for i, f := range e.Faults {
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(f.Error())
	}
	return b.String()
}

// Unwrap exposes the individual faults to errors.Is and errors.As.
func (e *CompileError) Unwrap() []error {
	errs := make([]error, len(e.Faults))
	for i, f := range e.Faults {
		errs[i] = f
	}
	return errs
}

// match strategies. The regex is always compiled and always correct; the
// literal modes are the fast path over it.
const (
	modeRegex uint8 = iota
	modeLiteral
	modeLiteralFold
)

type pattern struct {
	re    *regexp.Regexp // the matcher for modeRegex, the fallback for the rest
	lit   string         // literal text, modeLiteral only
	litLo []byte         // ASCII-lowered literal text, modeLiteralFold only
	mode  uint8
}

func (p *pattern) match(subject string, sc *Scratch) bool {
	switch p.mode {
	case modeLiteral:
		return strings.Contains(subject, p.lit)
	case modeLiteralFold:
		if sc != nil {
			if lowered, ok := sc.lowered(); ok {
				return bytes.Contains(lowered, p.litLo)
			}
		}
	}
	return p.re.MatchString(subject)
}

// Selector decides whether one subject is admitted. The zero value is the empty
// selector: it admits everything and filters nothing.
//
// Its methods take a pointer receiver, so keep Selectors in addressable places
// (a struct field, a slice element, a local); a Selector stored as a map value
// cannot be matched without copying it out first.
type Selector struct {
	include    []pattern
	exclude    []pattern
	unionLits  []string // literals for a union pre-filter, unionOK only
	subject    Subject
	ignoreCase bool
	unionOK    bool
}

// Compile validates and compiles a Spec once, at load. Every pattern is
// compiled on its own; all faults of the rule are aggregated into one
// *CompileError.
//
// Compile is the only legitimate constructor. On error the returned Selector is
// the zero value, which admits EVERYTHING: a caller that ignores the error
// filters nothing, so the error must reach the operator and stop the load.
func Compile(spec Spec) (Selector, error) {
	var sel Selector
	var faults []Fault

	subject, err := ParseSubject(spec.Subject)
	if err != nil {
		faults = append(faults, Fault{Field: "subject", Index: -1, Value: spec.Subject, Err: err})
	}
	sel.subject = subject
	sel.ignoreCase = spec.IgnoreCase
	sel.include, faults = compileList("include", spec.Include, spec.IgnoreCase, faults)
	sel.exclude, faults = compileList("exclude", spec.Exclude, spec.IgnoreCase, faults)
	if len(faults) > 0 {
		return Selector{}, &CompileError{Rule: spec.Rule, Faults: faults}
	}
	sel.buildUnion()
	return sel, nil
}

// buildUnion precomputes the union pre-filter contribution: every include
// pattern must be a case-sensitive literal, and there must be at least one.
func (s *Selector) buildUnion() {
	if len(s.include) == 0 || s.ignoreCase {
		return
	}
	lits := make([]string, 0, len(s.include))
	for i := range s.include {
		if s.include[i].mode != modeLiteral {
			return
		}
		lits = append(lits, s.include[i].lit)
	}
	s.unionLits, s.unionOK = lits, true
}

func compileList(field string, patterns []string, ignoreCase bool, faults []Fault) ([]pattern, []Fault) {
	if len(patterns) == 0 {
		return nil, faults
	}
	compiled := make([]pattern, 0, len(patterns))
	for i, src := range patterns {
		p, err := compilePattern(src, ignoreCase)
		if err != nil {
			faults = append(faults, Fault{Field: field, Index: i, Value: src, Err: err})
			continue
		}
		compiled = append(compiled, p)
	}
	return compiled, faults
}

func compilePattern(src string, ignoreCase bool) (pattern, error) {
	if src == "" {
		return pattern{}, ErrEmptyPattern
	}
	tree, err := syntax.Parse(src, syntax.Perl)
	if err != nil {
		return pattern{}, err
	}
	if isEmptyMatch(tree.Simplify()) {
		return pattern{}, ErrEmptyPattern
	}
	probe, err := regexp.Compile(src)
	if err != nil {
		return pattern{}, err
	}
	p := pattern{re: probe}
	if ignoreCase {
		// Prefix form, not "(?i:" + src + ")": the group form appends a ")"
		// that an unterminated \Q in src would swallow, failing the load with a
		// parse error about an expression the operator never wrote.
		folded, err := regexp.Compile("(?i)" + src)
		if err != nil {
			return pattern{}, err
		}
		p.re = folded
	}

	// The literal text comes from the unfolded pattern: a FoldCase literal
	// stores its runes case-folded, which is not what the subject contains.
	lit, complete := probe.LiteralPrefix()
	if !complete || lit == "" || !isPlainLiteral(src, ignoreCase) {
		return p, nil
	}
	switch {
	case !ignoreCase:
		p.lit, p.mode = lit, modeLiteral
	case isASCII(lit):
		p.litLo, p.mode = lowerASCII(lit), modeLiteralFold
	default:
		// A non-ASCII literal cannot be found in an ASCII-folded scratch, so
		// the engine keeps this one.
	}
	return p, nil
}

// isPlainLiteral reports whether the expression the ENGINE matches is nothing
// but a literal string. It must parse that same expression: under ignoreCase an
// inline "(?-i:...)" makes a literal-looking pattern case-sensitive again, and
// classifying the raw source would put it on the folded fast path. LiteralPrefix
// alone does not answer the question either - it reports ("foo", true) for
// "^foo$", whose anchors a substring scan would silently drop.
func isPlainLiteral(src string, ignoreCase bool) bool {
	if ignoreCase {
		src = "(?i:" + src + ")"
	}
	re, err := syntax.Parse(src, syntax.Perl)
	if err != nil || re.Op != syntax.OpLiteral {
		return false
	}
	if ignoreCase {
		return re.Flags&syntax.FoldCase != 0
	}
	return re.Flags&syntax.FoldCase == 0
}

// isEmptyMatch reports whether the pattern only spells the empty string.
// Patterns that merely CAN match empty ("a*") are not empty patterns.
func isEmptyMatch(re *syntax.Regexp) bool {
	switch re.Op {
	case syntax.OpEmptyMatch:
		return true
	case syntax.OpCapture:
		return len(re.Sub) == 1 && isEmptyMatch(re.Sub[0])
	}
	return false
}

func isASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] >= utf8.RuneSelf {
			return false
		}
	}
	return true
}

func lowerASCII(s string) []byte {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return b
}

// Match reports whether subject is admitted: rejected by any exclude pattern,
// otherwise admitted by any include pattern, and admitted outright when there
// is no include pattern.
//
// Under ignoreCase, literal patterns fall back to the regex engine here because
// there is no folded subject to scan. Hot loops should use a Scratch.
func (s *Selector) Match(subject string) bool {
	return s.admit(subject, nil)
}

// MatchScratch is Match for callers that run several selectors over one
// subject: the subject comes from the Scratch (see Scratch.Set), which folds it
// at most once however many selectors read it.
func (s *Selector) MatchScratch(sc *Scratch) bool {
	return s.admit(sc.subject, sc)
}

func (s *Selector) admit(subject string, sc *Scratch) bool {
	for i := range s.exclude {
		if s.exclude[i].match(subject, sc) {
			return false
		}
	}
	if len(s.include) == 0 {
		return true
	}
	for i := range s.include {
		if s.include[i].match(subject, sc) {
			return true
		}
	}
	return false
}

// Subject returns the declared subject, so dispatch code extracts the right
// string for the scope it is in.
func (s *Selector) Subject() Subject { return s.subject }

// Unfiltered reports whether the selector holds no pattern at all, i.e. admits
// every subject. Callers that would copy a file set to narrow it skip the copy.
func (s *Selector) Unfiltered() bool { return len(s.include) == 0 && len(s.exclude) == 0 }

// UnionLiterals returns the literals this selector contributes to a union
// pre-filter (as literal text: `\.log` is reported as `.log`), and whether it
// may take part at all - see the package doc. When ok is false the caller must
// not skip any subject on this selector's behalf. lits is a fresh copy, safe to
// append to and to hand on.
func (s *Selector) UnionLiterals() (lits []string, ok bool) {
	if !s.unionOK {
		return nil, false
	}
	lits = make([]string, len(s.unionLits))
	copy(lits, s.unionLits)
	return lits, true
}

// CompileLegacyLists compiles the whitelist/blacklist pair of a legacy
// [test.X] section into one Selector carrying the meaning those lists have at
// the archive member filter: case-insensitive LITERAL substrings over the full
// member path. Every surviving entry is therefore quoted - a metacharacter
// keeps matching itself, never the regex it spells - and the rule matches with
// ignoreCase over subject "path".
//
// Three outcomes, and a caller must distinguish all three:
//
//   - sel != nil: the compiled filter.
//   - sel == nil, admitNone == false: no usable pattern, admit every subject.
//   - admitNone == true: admit NOTHING. A whitelist that held only empty
//     entries selected nothing before this translation existed, and must go on
//     selecting nothing: turning it into "no filter" would silently widen a
//     no-scan configuration into a full scan.
//
// Empty entries are dropped rather than rejected: an empty entry matched
// nothing under the old literal matcher, so beside a real entry it stays inert.
// The config layer rejects them outright once rules own their patterns, which
// is the real fix; this function is transitional and dies with the rules
// migration.
//
// Two divergences from the old matcher, both deliberate:
//
//   - An entry holding a NON-ASCII byte keeps the (?i) regex engine, because
//     the fold fast path is ASCII-scoped. Its case folding is therefore RE2
//     simple folding rather than the old Unicode ToLower, which no translation
//     can preserve in both directions (ToLower maps "İ" onto "i̇" where simple
//     folding does not; simple folding maps "ſ" onto "s" where ToLower does
//     not). Such an entry costs ~1 us per member instead of ~80 ns - the one
//     shape this translation makes slower, and only until rules carry their
//     own compiled selectors.
//   - An error is reachable only for an entry holding invalid UTF-8, which no
//     TOML config can carry: quoting escapes every metacharacter, so nothing a
//     valid string spells can fail to compile. Callers must fail CLOSED on it -
//     an unusable filter means the scan cannot honour the operator's exclusions,
//     so the subjects go unscanned and acknowledged, never scanned unfiltered.
func CompileLegacyLists(rule string, whitelist []string, blacklist []string) (sel *Selector, admitNone bool, err error) {
	include, exclude := quoteEntries(whitelist), quoteEntries(blacklist)
	if len(whitelist) > 0 && len(include) == 0 {
		return nil, true, nil
	}
	if len(include) == 0 && len(exclude) == 0 {
		return nil, false, nil
	}
	compiled, err := Compile(Spec{
		Rule:       rule,
		Subject:    "path",
		IgnoreCase: true,
		Include:    include,
		Exclude:    exclude,
	})
	if err != nil {
		return nil, false, err
	}
	return &compiled, false, nil
}

// quoteEntries escapes each non-empty entry so it matches its own text.
func quoteEntries(list []string) []string {
	if len(list) == 0 {
		return nil
	}
	quoted := make([]string, 0, len(list))
	for _, entry := range list {
		if entry == "" {
			continue
		}
		quoted = append(quoted, regexp.QuoteMeta(entry))
	}
	return quoted
}

// Scratch carries one subject through a set of selectors. A Selector is shared
// across worker goroutines and therefore holds no buffer of its own; each
// goroutine keeps its own Scratch. The zero value is ready to use.
type Scratch struct {
	subject string
	lower   []byte
	valid   bool // lower is current for subject
	usable  bool // lower may be scanned, meaningful when valid
}

// Set points the scratch at the next subject. Folding is deferred to the first
// ignoreCase literal pattern that needs it, so a case-sensitive rule set never
// pays for it.
func (sc *Scratch) Set(subject string) {
	sc.subject = subject
	sc.valid = false
}

// Subject returns the subject the scratch currently carries.
func (sc *Scratch) Subject() string { return sc.subject }

// lowered folds the subject once per Set. ok is false when the subject holds a
// rune that RE2's Unicode simple folding maps onto ASCII but an ASCII fold does
// not - U+017F ("ſ", onto "s") and U+212A ("K", onto "k") are the only two in
// the whole rune space - in which case the caller must use the regex engine
// instead. Every other non-ASCII byte is left alone: UTF-8 is self
// synchronising, so its bytes can never be mistaken for the ASCII bytes of a
// literal.
func (sc *Scratch) lowered() ([]byte, bool) {
	if sc.valid {
		return sc.lower, sc.usable
	}
	buf := append(sc.lower[:0], sc.subject...)
	usable := true
	for i := range buf {
		c := buf[i]
		if c < utf8.RuneSelf {
			if c >= 'A' && c <= 'Z' {
				buf[i] = c + ('a' - 'A')
			}
			continue
		}
		if (c == 0xC5 && i+1 < len(buf) && buf[i+1] == 0xBF) ||
			(c == 0xE2 && i+2 < len(buf) && buf[i+1] == 0x84 && buf[i+2] == 0xAA) {
			usable = false
			break
		}
	}
	sc.lower, sc.valid, sc.usable = buf, true, usable
	return buf, usable
}
