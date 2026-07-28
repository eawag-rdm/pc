package metadata

import (
	"regexp"
	"strings"
	"time"
)

// checkEach is the shared core for simple per-value rules (DRY). It applies
// pred to each value and returns a problem message naming the offending
// values, or "" if all pass. A field with no values passes vacuously here —
// use Required to assert presence.
func checkEach(values []string, problem string, pred func(string) bool) string {
	var bad []string
	for _, v := range values {
		if !pred(v) {
			bad = append(bad, v)
		}
	}
	if len(bad) == 0 {
		return ""
	}
	return problem + ": " + strings.Join(bad, "; ")
}

// Required fails when a field has no values.
func Required() Rule {
	return Rule{Name: "Required", Fn: func(values []string) string {
		if len(values) == 0 {
			return "required, but not set"
		}
		return ""
	}}
}

// Equals fails for any value that does not equal want. Absent fields pass
// vacuously; pair with Required to also assert presence.
func Equals(want string) Rule {
	return Rule{Name: "Equals", Fn: func(values []string) string {
		return checkEach(values, "must be \""+want+"\", got", func(s string) bool {
			return s == want
		})
	}}
}

// matchRule builds a regex-backed Rule for simple per-value format checks.
func matchRule(name string, re *regexp.Regexp) Rule {
	return Rule{Name: name, Fn: func(values []string) string {
		return checkEach(values, "invalid format", re.MatchString)
	}}
}

// titleRe: a publication data package title must read "Data for: ...".
var titleRe = regexp.MustCompile(`^Data for: \S`)

// TitleFormat checks the title follows "Data for: [title of paper]".
func TitleFormat() Rule { return matchRule("TitleFormat", titleRe) }

// Author-format patterns, ported from the CKAN-side validator
// eaw_schema_validate_author_format.
var (
	authorInstitution  = regexp.MustCompile(`^[A-Z][a-zA-Z]+: .+$`)
	authorWithEmail    = regexp.MustCompile(`^[^,]+,\s*[^<]+\s*<[^@]+@[^>]+>$`)
	authorWithoutEmail = regexp.MustCompile(`^[^,]+,\s*.+$`)
)

// AuthorFormat checks the Eawag position-aware author rules:
//   - the first author must be "Lastname, Firstname <email@domain>" or an
//     institution "Abbrev: Full Name";
//   - subsequent authors may omit the email ("Lastname, Firstname").
//
// It reports the first offending author.
func AuthorFormat() Rule {
	return Rule{Name: "AuthorFormat", Fn: func(values []string) string {
		for i, author := range values {
			author = strings.TrimSpace(author)
			if author == "" {
				continue
			}
			if msg := checkAuthor(author, i == 0); msg != "" {
				return msg
			}
		}
		return ""
	}}
}

// checkAuthor validates one author entry; "" means valid.
func checkAuthor(author string, isFirst bool) string {
	switch {
	// Valid institution — accepted in any position.
	case authorInstitution.MatchString(author):
		return ""
	// A colon, but not a valid institution.
	case strings.Contains(author, ":"):
		return `institution must read "Abbrev: Full Name" ` +
			`(abbreviation 2+ letters, first uppercase): ` + author
	// Email markers but no comma — missing comma.
	case strings.ContainsAny(author, "<@") && !strings.Contains(author, ","):
		if isFirst {
			return `first author must be "Lastname, Firstname <email@domain>" ` +
				`or "Abbrev: Full Name": ` + author
		}
		return `author must be "Lastname, Firstname <email@domain>" ` +
			`or "Lastname, Firstname": ` + author
	// A comma and email markers — the email must be well-formed.
	case strings.ContainsAny(author, "<@"):
		if !authorWithEmail.MatchString(author) {
			return `author email invalid, use "Lastname, Firstname <email@domain>": ` + author
		}
		return ""
	// A comma, no email.
	case strings.Contains(author, ","):
		if authorWithoutEmail.MatchString(author) {
			if isFirst {
				return `first author must include an email: ` +
					`"Lastname, Firstname <email@domain>": ` + author
			}
			return ""
		}
		fallthrough
	// A bare name — no comma, no email, no colon.
	default:
		if isFirst {
			return `first author must be "Lastname, Firstname <email@domain>" ` +
				`or "Abbrev: Full Name": ` + author
		}
		return `author must be "Lastname, Firstname" or "Abbrev: Full Name": ` + author
	}
}

// dateLayouts accepted when parsing date fields.
var dateLayouts = []string{
	"2006-01-02",
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02T15:04:05.999999",
	"2006-01-02 15:04:05",
}

func parseDate(s string) (time.Time, bool) {
	for _, layout := range dateLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// DateExpired checks that a date field, if set, is a valid date in the past —
// e.g. an embargo that must already have ended. Absent fields pass: the rule
// is conditional on the field being set. Unparseable values and dates still in
// the future are reported as distinct problems so the user knows whether to
// fix the value or wait out the embargo.
func DateExpired() Rule {
	return Rule{Name: "DateExpired", Fn: func(values []string) string {
		var invalid, future []string
		for _, v := range values {
			t, ok := parseDate(v)
			switch {
			case !ok:
				invalid = append(invalid, v)
			case t.After(time.Now()):
				future = append(future, v)
			}
		}
		var problems []string
		if len(invalid) > 0 {
			problems = append(problems, "not a recognized date: "+strings.Join(invalid, "; "))
		}
		if len(future) > 0 {
			problems = append(problems, "embargo not yet expired: "+strings.Join(future, "; "))
		}
		return strings.Join(problems, "; ")
	}}
}
