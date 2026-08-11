package selector

import (
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
)

func mustCompile(t *testing.T, spec Spec) Selector {
	t.Helper()
	sel, err := Compile(spec)
	if err != nil {
		t.Fatalf("Compile(%+v) failed: %v", spec, err)
	}
	return sel
}

func TestSelectorCompileErrors(t *testing.T) {
	_, err := Compile(Spec{
		Rule:    "credentials",
		Subject: "basename",
		Include: []string{"(", `\.log`, "[a-"},
		Exclude: []string{"ok", "*bad"},
	})
	if err == nil {
		t.Fatal("expected a compile error")
	}

	var cerr *CompileError
	if !errors.As(err, &cerr) {
		t.Fatalf("expected *CompileError, got %T", err)
	}
	if cerr.Rule != "credentials" {
		t.Errorf("rule = %q, want %q", cerr.Rule, "credentials")
	}
	// All faults are reported at once: the bad subject and three bad patterns.
	if len(cerr.Faults) != 4 {
		t.Fatalf("faults = %d (%v), want 4", len(cerr.Faults), cerr.Faults)
	}
	msg := err.Error()
	for _, want := range []string{`"credentials"`, `"basename"`, `"("`, `"[a-"`, `"*bad"`, "include", "exclude", "subject"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %s", msg, want)
		}
	}
	if strings.Contains(msg, `"ok"`) || strings.Contains(msg, `\.log`) {
		t.Errorf("error %q blames a valid pattern", msg)
	}
	if !errors.Is(err, ErrUnknownSubject) {
		t.Error("errors.Is(err, ErrUnknownSubject) = false")
	}

	// A Fault is an error itself, reachable with errors.As.
	var fault Fault
	if !errors.As(err, &fault) {
		t.Fatal("errors.As(err, &Fault{}) = false")
	}
	if fault.Field != "subject" || fault.Index != -1 || fault.Value != "basename" {
		t.Errorf("first fault = %+v, want the subject fault", fault)
	}
	if !errors.Is(fault, ErrUnknownSubject) {
		t.Error("errors.Is(fault, ErrUnknownSubject) = false")
	}

	// A single bad pattern still names the rule.
	_, err = Compile(Spec{Rule: "solo", Exclude: []string{"("}})
	if err == nil || !strings.Contains(err.Error(), `"solo"`) {
		t.Errorf("single-fault error = %v, want it to name the rule", err)
	}

	// A failed Compile returns the zero Selector, which admits everything: the
	// caller must not ignore the error.
	sel, err := Compile(Spec{Rule: "bad", Include: []string{"("}})
	if err == nil {
		t.Fatal("expected an error")
	}
	if !sel.Unfiltered() {
		t.Error("failed Compile returned a non-zero Selector")
	}
}

func TestSelectorEmptyPatternRejected(t *testing.T) {
	for _, spec := range []Spec{
		{Rule: "r", Include: []string{""}},
		{Rule: "r", Exclude: []string{""}},
		{Rule: "r", Include: []string{"ok", ""}},
		// The empty pattern under another spelling.
		{Rule: "r", Include: []string{"(?:)"}},
		{Rule: "r", Include: []string{"()"}},
		{Rule: "r", Include: []string{"x{0}"}},
		{Rule: "r", Include: []string{"(?i)"}},
	} {
		_, err := Compile(spec)
		if err == nil {
			t.Fatalf("Compile(%+v) succeeded, want an error", spec)
		}
		if !errors.Is(err, ErrEmptyPattern) {
			t.Errorf("Compile(%+v) = %v, want ErrEmptyPattern", spec, err)
		}
	}

	// Patterns that merely CAN match empty stay legal.
	for _, p := range []string{"a*", "a?", `.*\.log$`} {
		if _, err := Compile(Spec{Rule: "r", Include: []string{p}}); err != nil {
			t.Errorf("Compile(%q) = %v, want it accepted", p, err)
		}
	}
}

func TestSelectorSubjects(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Subject
	}{
		{"", SubjectName},
		{"name", SubjectName},
		{"path", SubjectPath},
	} {
		sel := mustCompile(t, Spec{Rule: "r", Subject: tc.in})
		if got := sel.Subject(); got != tc.want {
			t.Errorf("Subject(%q) = %v, want %v", tc.in, got, tc.want)
		}
		got, err := ParseSubject(tc.in)
		if err != nil || got != tc.want {
			t.Errorf("ParseSubject(%q) = %v, %v", tc.in, got, err)
		}
		if s := tc.want.String(); tc.in != "" && s != tc.in {
			t.Errorf("Subject(%q).String() = %q", tc.in, s)
		}
	}

	if _, err := ParseSubject("relpath"); !errors.Is(err, ErrUnknownSubject) {
		t.Errorf("ParseSubject(%q) error = %v, want ErrUnknownSubject", "relpath", err)
	}
	if _, err := Compile(Spec{Rule: "r", Subject: "relpath"}); !errors.Is(err, ErrUnknownSubject) {
		t.Errorf("unknown subject error = %v, want ErrUnknownSubject", err)
	}
	if got := Subject(3).String(); got != "subject(3)" {
		t.Errorf("Subject(3).String() = %q, want %q", got, "subject(3)")
	}

	// The declared subject decides which string the dispatch site extracts.
	file := struct{ name, relPath string }{name: "readme.md", relPath: "docs/readme.md"}
	subjectOf := func(sel *Selector) string {
		if sel.Subject() == SubjectPath {
			return file.relPath
		}
		return file.name
	}
	byPath := mustCompile(t, Spec{Rule: "docs", Subject: "path", Include: []string{"^docs/"}})
	byName := mustCompile(t, Spec{Rule: "docs", Subject: "name", Include: []string{"^docs/"}})
	if !byPath.Match(subjectOf(&byPath)) {
		t.Error(`subject "path": ^docs/ should match docs/readme.md`)
	}
	if byName.Match(subjectOf(&byName)) {
		t.Error(`subject "name": ^docs/ should not match readme.md`)
	}
}

func TestSelectorExcludeWins(t *testing.T) {
	for _, tc := range []struct {
		name    string
		spec    Spec
		subject string
		want    bool
	}{
		{"both match, exclude wins", Spec{Include: []string{`\.csv$`}, Exclude: []string{"^raw_"}}, "raw_data.csv", false},
		{"include only", Spec{Include: []string{`\.csv$`}, Exclude: []string{"^raw_"}}, "clean_data.csv", true},
		{"neither", Spec{Include: []string{`\.csv$`}, Exclude: []string{"^raw_"}}, "notes.txt", false},
		{"empty include admits", Spec{Exclude: []string{"^raw_"}}, "notes.txt", true},
		{"exclude alone rejects", Spec{Exclude: []string{"^raw_"}}, "raw_data.csv", false},
		{"empty selector admits", Spec{}, "anything", true},
		{"literal exclude wins over literal include", Spec{Include: []string{"data"}, Exclude: []string{"raw"}}, "raw_data.csv", false},
		{"folded exclude wins", Spec{IgnoreCase: true, Include: []string{"data"}, Exclude: []string{"RAW"}}, "Raw_Data.csv", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.Rule = "r"
			sel := mustCompile(t, tc.spec)
			if got := sel.Match(tc.subject); got != tc.want {
				t.Errorf("Match(%q) = %v, want %v", tc.subject, got, tc.want)
			}
			var sc Scratch
			sc.Set(tc.subject)
			if got := sel.MatchScratch(&sc); got != tc.want {
				t.Errorf("MatchScratch(%q) = %v, want %v", tc.subject, got, tc.want)
			}
		})
	}

	// The zero value filters nothing.
	var zero Selector
	if !zero.Match("anything") || !zero.Unfiltered() {
		t.Error("zero Selector should admit everything and report Unfiltered")
	}
}

// equivalenceSpecs covers pure literals, literals with a metachar tail, anchored
// patterns, inline case flags and case variants.
var equivalenceSpecs = []Spec{
	{Include: []string{"temp", `\.log`}},
	{Include: []string{"temp", `\.log`}, IgnoreCase: true},
	{Include: []string{`.*\.log$`, "temp.*"}},
	{Include: []string{`.*\.log$`, "temp.*"}, IgnoreCase: true},
	{Include: []string{"^foo$"}},
	{Include: []string{"^foo$"}, IgnoreCase: true},
	{Include: []string{"foo$", "^docs/"}},
	{Include: []string{"TEMP"}},
	{Include: []string{"TEMP"}, IgnoreCase: true},
	{Include: []string{"a|b"}},
	{Include: []string{"日本"}},
	{Include: []string{"日本"}, IgnoreCase: true},
	{Include: []string{"Ä"}, IgnoreCase: true},
	{Include: []string{"test"}, IgnoreCase: true},
	{Include: []string{"k"}, IgnoreCase: true},
	{Include: []string{"(?-i:foo)"}, IgnoreCase: true},
	{Include: []string{"(?i)foo"}},
	{Include: []string{`data/raw\.txt`}, Subject: "path"},
	{Exclude: []string{"temp"}},
	{Include: []string{".*"}, Exclude: []string{"TEMP"}, IgnoreCase: true},
	{Include: []string{"data"}, Exclude: []string{`\.log`}},
}

var equivalenceSubjects = []string{
	"", "temp.log", "TEMP.LOG", "Temp.Log", "temp", "log", ".log",
	"docs/readme.md", "x/docs/y.txt", "data/raw.txt", "DATA/RAW.TXT",
	"foo", "FOO", "xfooy", "a", "b", "c",
	"日本語.txt", "にほん.txt", "MÜLLER.TXT", "müller.txt", "Ärger.csv", "ärger.csv",
	"café_temp.log", "CAFÉ_TEMP.LOG",
	"teſt.csv", "teſt.txt", "K.txt", "k.txt", "\u212A.txt", "\u212A_TEMP.log",
}

// refSelector is the pure regex-engine reading of a Spec: no literal fast path,
// no scratch. It defines the semantics the fast path must reproduce exactly.
type refSelector struct {
	include, exclude []*regexp.Regexp
}

func newRefSelector(t *testing.T, spec Spec) refSelector {
	t.Helper()
	compile := func(list []string) []*regexp.Regexp {
		out := make([]*regexp.Regexp, 0, len(list))
		for _, p := range list {
			if spec.IgnoreCase {
				p = "(?i)" + p
			}
			re, err := regexp.Compile(p)
			if err != nil {
				t.Fatalf("reference compile %q: %v", p, err)
			}
			out = append(out, re)
		}
		return out
	}
	return refSelector{include: compile(spec.Include), exclude: compile(spec.Exclude)}
}

func (r refSelector) match(subject string) bool {
	for _, re := range r.exclude {
		if re.MatchString(subject) {
			return false
		}
	}
	if len(r.include) == 0 {
		return true
	}
	for _, re := range r.include {
		if re.MatchString(subject) {
			return true
		}
	}
	return false
}

func TestSelectorLiteralFastPathEquivalence(t *testing.T) {
	var sc Scratch // reused across subjects, as a real caller does
	for _, spec := range equivalenceSpecs {
		spec.Rule = "r"
		sel := mustCompile(t, spec)
		ref := newRefSelector(t, spec)
		for _, subject := range equivalenceSubjects {
			want := ref.match(subject)
			if got := sel.Match(subject); got != want {
				t.Errorf("%+v Match(%q) = %v, regex engine says %v", spec, subject, got, want)
			}
			sc.Set(subject)
			if got := sel.MatchScratch(&sc); got != want {
				t.Errorf("%+v MatchScratch(%q) = %v, regex engine says %v", spec, subject, got, want)
			}
		}
	}
}

// TestFoldFormEquivalence pins the reason the matching regex uses the "(?i)"
// prefix while the literal classification uses the "(?i:...)" group: the two
// forms agree on every pattern that survives the group form, and the group form
// does not survive an unterminated \Q.
func TestFoldFormEquivalence(t *testing.T) {
	for _, spec := range equivalenceSpecs {
		for _, p := range append(append([]string{}, spec.Include...), spec.Exclude...) {
			group, err := regexp.Compile("(?i:" + p + ")")
			if err != nil {
				continue
			}
			prefix := regexp.MustCompile("(?i)" + p)
			for _, s := range equivalenceSubjects {
				if prefix.MatchString(s) != group.MatchString(s) {
					t.Errorf("pattern %q subject %q: prefix form = %v, group form = %v",
						p, s, prefix.MatchString(s), group.MatchString(s))
				}
			}
		}
	}

	//lint:ignore SA1000 deliberately invalid: pins why the prefix form is used
	if _, err := regexp.Compile(`(?i:\Qfoo)`); err == nil {
		t.Error(`(?i:\Qfoo) compiles - the group form no longer needs avoiding`)
	}
	sel := mustCompile(t, Spec{Rule: "r", IgnoreCase: true, Include: []string{`\Qa(b`}})
	if !sel.Match("XA(BY") {
		t.Error(`\Q pattern should compile and match case-insensitively`)
	}
}

// TestSelectorFastPathEngaged guards the equivalence test against passing
// trivially: the patterns that must skip the regex engine, and the ones that
// must not.
func TestSelectorFastPathEngaged(t *testing.T) {
	for _, tc := range []struct {
		pattern    string
		ignoreCase bool
		want       uint8
	}{
		{"temp", false, modeLiteral},
		{`\.log`, false, modeLiteral},
		{"日本", false, modeLiteral},
		{"TEMP", true, modeLiteralFold},
		{"^foo$", false, modeRegex}, // LiteralPrefix reports "foo", complete
		{"^foo$", true, modeRegex},
		{"temp.*", false, modeRegex},    // literal prefix, non-literal tail
		{"a|b", false, modeRegex},       //
		{"(?i)temp", false, modeRegex},  // folded literal, not a plain one
		{"(?-i:foo)", true, modeRegex},  // unfolded under an ignoreCase rule
		{"日本", true, modeRegex},         // non-ASCII literal, the scratch is ASCII
		{`\Qa(b`, true, modeRegex},      // group form fails to parse, stay safe
		{"temp", true, modeLiteralFold}, //
	} {
		sel := mustCompile(t, Spec{Rule: "r", IgnoreCase: tc.ignoreCase, Include: []string{tc.pattern}})
		if got := sel.include[0].mode; got != tc.want {
			t.Errorf("pattern %q (ignoreCase=%v): mode = %d, want %d", tc.pattern, tc.ignoreCase, got, tc.want)
		}
	}

	// A subject-level fallback: only the two runes that fold onto ASCII send a
	// subject to the engine, other non-ASCII bytes stay on the fast path.
	for _, tc := range []struct {
		subject string
		usable  bool
	}{
		{"café_temp.log", true},
		{"MÜLLER_TEMP.LOG", true},
		{"日本_temp.log", true},
		{"temp.log", true},
		{"", true},
		{"teſt_temp.log", false},
		{"\u212A_temp.log", false},
	} {
		var sc Scratch
		sc.Set(tc.subject)
		if _, ok := sc.lowered(); ok != tc.usable {
			t.Errorf("scratch usable for %q = %v, want %v", tc.subject, ok, tc.usable)
		}
	}
}

func TestSelectorScratchContract(t *testing.T) {
	spec := Spec{Rule: "r", IgnoreCase: true, Include: []string{"temp", `\.log`}}
	sel := mustCompile(t, spec)
	ref := newRefSelector(t, spec)

	// One scratch, many subjects: Set must invalidate the previous folding,
	// including across the usable/engine-fallback boundary.
	var sc Scratch
	for range 3 {
		for _, subject := range equivalenceSubjects {
			want := ref.match(subject)
			sc.Set(subject)
			if sc.Subject() != subject {
				t.Fatalf("Subject() = %q, want %q", sc.Subject(), subject)
			}
			if got := sel.MatchScratch(&sc); got != want {
				t.Errorf("MatchScratch(%q) = %v, want %v", subject, got, want)
			}
			// Folding is memoized, so a second read must agree with the first.
			if got := sel.MatchScratch(&sc); got != want {
				t.Errorf("second MatchScratch(%q) = %v, want %v", subject, got, want)
			}
			if got := sel.Match(subject); got != want {
				t.Errorf("Match(%q) = %v, want %v", subject, got, want)
			}
		}
	}

	// The zero Scratch is ready to use.
	var zero Scratch
	zero.Set("TEMP_FILE.CSV")
	if !sel.MatchScratch(&zero) {
		t.Error("zero Scratch + Set should match")
	}
}

func TestSelectorUnionLiterals(t *testing.T) {
	sel := mustCompile(t, Spec{Rule: "r", Include: []string{"temp", `\.log`}, Exclude: []string{"raw"}})
	lits, ok := sel.UnionLiterals()
	if !ok {
		t.Fatal("UnionLiterals() ok = false for an all-literal include list")
	}
	want := []string{"temp", ".log"}
	if len(lits) != len(want) {
		t.Fatalf("lits = %q, want %q", lits, want)
	}
	for i := range want {
		if lits[i] != want[i] {
			t.Fatalf("lits = %q, want %q", lits, want)
		}
	}

	// The result is a copy with no spare capacity: mutating it must not
	// reach the Selector, and an append cannot land in shared memory.
	lits[0] = "mutated"
	if cap(lits) != len(lits) {
		t.Errorf("UnionLiterals() cap %d > len %d - append could write shared memory", cap(lits), len(lits))
	}
	again, _ := sel.UnionLiterals()
	if again[0] != "temp" || len(again) != 2 {
		t.Errorf("UnionLiterals() = %q after the caller mutated an earlier result", again)
	}

	for _, tc := range []struct {
		name string
		spec Spec
	}{
		{"ignoreCase", Spec{IgnoreCase: true, Include: []string{"temp"}}},
		{"regex include", Spec{Include: []string{"temp", `.*\.log$`}}},
		{"anchored include", Spec{Include: []string{"^temp"}}},
		{"empty include", Spec{Exclude: []string{"temp"}}},
		{"no patterns", Spec{}},
		{"non-ASCII literal under ignoreCase", Spec{IgnoreCase: true, Include: []string{"日本"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.spec.Rule = "r"
			sel := mustCompile(t, tc.spec)
			if lits, ok := sel.UnionLiterals(); ok || lits != nil {
				t.Errorf("UnionLiterals() = %q, %v; want nil, false", lits, ok)
			}
		})
	}
}

func TestSelectorStatelessConcurrent(t *testing.T) {
	spec := Spec{
		Rule:       "shared",
		IgnoreCase: true,
		Include:    []string{"temp", `\.log`, `.*\.csv$`},
		Exclude:    []string{"raw", "^skip/"},
	}
	sel := mustCompile(t, spec)
	ref := newRefSelector(t, spec)

	subjects := equivalenceSubjects
	want := make([]bool, len(subjects))
	for i, s := range subjects {
		want[i] = ref.match(s)
	}

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			var sc Scratch // per-goroutine, the Selector owns none
			for range 200 {
				for i, s := range subjects {
					sc.Set(s)
					if got := sel.MatchScratch(&sc); got != want[i] {
						t.Errorf("MatchScratch(%q) = %v, want %v", s, got, want[i])
						return
					}
					if got := sel.Match(s); got != want[i] {
						t.Errorf("Match(%q) = %v, want %v", s, got, want[i])
						return
					}
				}
			}
		})
	}
	wg.Wait()
}

// TestCompileLegacyLists pins the transitional translation of a legacy
// [test.X] whitelist/blacklist pair: quoted entries, ignoreCase, subject
// "path", empty entries dropped, and an all-empty whitelist reported as
// admit-nothing rather than widened into "no filter".
func TestCompileLegacyLists(t *testing.T) {
	assertVerdict := func(t *testing.T, sel *Selector, subject string, want bool) {
		t.Helper()
		if got := sel.Match(subject); got != want {
			t.Errorf("Match(%q) = %v, want %v", subject, got, want)
		}
		var sc Scratch // the iterator's path: same verdict through the scratch
		sc.Set(subject)
		if got := sel.MatchScratch(&sc); got != want {
			t.Errorf("MatchScratch(%q) = %v, want %v", subject, got, want)
		}
	}

	t.Run("no lists", func(t *testing.T) {
		sel, admitNone, err := CompileLegacyLists("rule", nil, nil)
		if sel != nil || admitNone || err != nil {
			t.Fatalf("got (%v, %v, %v), want (nil, false, nil)", sel, admitNone, err)
		}
	})

	t.Run("all-empty whitelist admits nothing", func(t *testing.T) {
		for _, whitelist := range [][]string{{""}, {"", ""}} {
			sel, admitNone, err := CompileLegacyLists("rule", whitelist, nil)
			if err != nil {
				t.Fatalf("CompileLegacyLists(%q) failed: %v", whitelist, err)
			}
			if sel != nil || !admitNone {
				t.Errorf("whitelist %q: got (%v, %v), want (nil, true)", whitelist, sel, admitNone)
			}
		}
	})

	t.Run("all-empty blacklist is inert", func(t *testing.T) {
		sel, admitNone, err := CompileLegacyLists("rule", nil, []string{""})
		if err != nil || sel != nil || admitNone {
			t.Fatalf("got (%v, %v, %v), want (nil, false, nil)", sel, admitNone, err)
		}
	})

	t.Run("entries are quoted and folded", func(t *testing.T) {
		sel, admitNone, err := CompileLegacyLists("rule", []string{"", "temp.*"}, nil)
		if err != nil || admitNone {
			t.Fatalf("CompileLegacyLists failed: (%v, %v)", admitNone, err)
		}
		if sel.Subject() != SubjectPath {
			t.Errorf("Subject() = %v, want %v", sel.Subject(), SubjectPath)
		}
		assertVerdict(t, sel, "data/temp.*.txt", true)     // the literal text
		assertVerdict(t, sel, "data/temporary.txt", false) // never as a regex
		assertVerdict(t, sel, "DATA/TEMP.*.TXT", true)     // ignoreCase kept
	})

	t.Run("both lists apply together", func(t *testing.T) {
		sel, _, err := CompileLegacyLists("rule", []string{"temp"}, []string{".log"})
		if err != nil {
			t.Fatalf("CompileLegacyLists failed: %v", err)
		}
		assertVerdict(t, sel, "temp_notes.txt", true)
		assertVerdict(t, sel, "temp.log", false)       // exclude wins
		assertVerdict(t, sel, "UPPER_CASE.TXT", false) // include still restricts
	})

	t.Run("non-ASCII entry keeps engine folding", func(t *testing.T) {
		sel, _, err := CompileLegacyLists("rule", []string{"café"}, nil)
		if err != nil {
			t.Fatalf("CompileLegacyLists failed: %v", err)
		}
		// RE2 simple folding, reached through the engine because the scratch
		// folds ASCII only - the declared divergence from the old ToLower.
		assertVerdict(t, sel, "notes/CAFÉ.txt", true)
		assertVerdict(t, sel, "notes/cafe.txt", false)
	})

	t.Run("invalid UTF-8 entry is a load error", func(t *testing.T) {
		sel, admitNone, err := CompileLegacyLists("rule", []string{"a\xffb"}, nil)
		if err == nil {
			t.Fatal("want an error for an entry holding invalid UTF-8")
		}
		if sel != nil || admitNone {
			t.Errorf("got (%v, %v), want (nil, false) beside the error", sel, admitNone)
		}
	})
}
