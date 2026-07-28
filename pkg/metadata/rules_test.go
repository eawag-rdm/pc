package metadata

import (
	"strings"
	"testing"
)

func TestRequired(t *testing.T) {
	if Required().Fn(nil) == "" {
		t.Error("Required should fail on no values")
	}
	if msg := Required().Fn([]string{"x"}); msg != "" {
		t.Errorf("Required should pass with a value, got %q", msg)
	}
}

func TestEquals(t *testing.T) {
	r := Equals("public")
	if msg := r.Fn([]string{"public"}); msg != "" {
		t.Errorf("expected pass, got %q", msg)
	}
	if r.Fn([]string{"same_organization"}) == "" {
		t.Error("expected fail for a non-matching value")
	}
	if msg := r.Fn(nil); msg != "" {
		t.Errorf("Equals should pass vacuously on an absent field, got %q", msg)
	}
	if r.Fn([]string{"public", "secret"}) == "" {
		t.Error("expected fail when one of several values differs")
	}
}

func TestTitleFormat(t *testing.T) {
	if msg := TitleFormat().Fn([]string{"Data for: Explaining Crop Residue Management"}); msg != "" {
		t.Errorf("expected pass, got %q", msg)
	}
	if TitleFormat().Fn([]string{"Public perception of microplastics pollution"}) == "" {
		t.Error("expected fail for a title without the 'Data for:' prefix")
	}
}

func TestAuthorFormat(t *testing.T) {
	r := AuthorFormat()

	pass := [][]string{
		{"Kollmann, Josianne <josianne.kollmann@eawag.ch>"},
		{"Kollmann, Josianne <j@eawag.ch>", "Reckels, Sophie", "Contzen, Nadja"},
		{"Eawag: Swiss Federal Institute of Aquatic Science"},
		{"Eawag: Swiss Federal Institute", "Reckels, Sophie"},
	}
	for _, in := range pass {
		if msg := r.Fn(in); msg != "" {
			t.Errorf("expected pass for %v, got %q", in, msg)
		}
	}

	fail := [][]string{
		{"Contzen, Nadja"},                       // first author, no email
		{"Nadja Contzen <n@x.ch>"},               // first author, no comma
		{"A, B <a@b.ch>", "Sophie"},              // subsequent author, bare name
		{"A, B <a@b.ch>", "C, D <not-an-email>"}, // subsequent author, malformed email
		{"foo: bar"},                             // colon, but not an institution
	}
	for _, in := range fail {
		if r.Fn(in) == "" {
			t.Errorf("expected fail for %v", in)
		}
	}
}

func TestDateExpired(t *testing.T) {
	if msg := DateExpired().Fn([]string{"2000-01-01"}); msg != "" {
		t.Errorf("a past date should pass, got %q", msg)
	}
	if msg := DateExpired().Fn(nil); msg != "" {
		t.Errorf("an absent embargo should pass, got %q", msg)
	}

	// Future and unparseable values are distinct problems with distinct messages.
	future := DateExpired().Fn([]string{"2999-01-01"})
	if !strings.Contains(future, "not yet expired") {
		t.Errorf("a future date should report 'not yet expired', got %q", future)
	}
	invalid := DateExpired().Fn([]string{"not-a-date"})
	if !strings.Contains(invalid, "not a recognized date") {
		t.Errorf("an unparseable date should report 'not a recognized date', got %q", invalid)
	}
	both := DateExpired().Fn([]string{"not-a-date", "2999-01-01"})
	if !strings.Contains(both, "not a recognized date") || !strings.Contains(both, "not yet expired") {
		t.Errorf("mixed values should report both problems, got %q", both)
	}
}
