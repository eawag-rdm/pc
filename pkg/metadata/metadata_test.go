package metadata

import "testing"

func TestFieldDropsBlankValues(t *testing.T) {
	m := &Metadata{}
	e := m.Entity("package", "p")
	e.Field("a", []string{"  ", "", "keep"}, Required())
	got := e.Fields["a"]
	if len(got) != 1 || got[0] != "keep" {
		t.Errorf("blank values not dropped, got %v", got)
	}
}

func TestFieldRegistersChecksWithoutRunning(t *testing.T) {
	m := &Metadata{}
	e := m.Entity("package", "p")
	e.Field("status", []string{"draft"}, Required(), Equals("complete"))
	if len(e.checks) != 2 {
		t.Fatalf("expected 2 registered checks, got %d", len(e.checks))
	}
	// Registration must not mutate the field or run the checks.
	if e.Fields["status"][0] != "draft" {
		t.Error("registration must not mutate field values")
	}
}

func TestEntitiesRegisteredInOrder(t *testing.T) {
	m := &Metadata{}
	m.Entity("package", "p")
	m.Entity("resource", "r")
	if len(m.Entities) != 2 {
		t.Fatalf("expected 2 entities, got %d", len(m.Entities))
	}
	if m.Entities[0].Kind != "package" || m.Entities[1].Kind != "resource" {
		t.Error("entities not registered in order")
	}
}

func TestRunChecksReportsFailures(t *testing.T) {
	m := &Metadata{}
	e := m.Entity("package", "p")
	e.Field("status", []string{"draft"}, Equals("complete"))
	msgs := RunChecks(m)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 failure, got %d", len(msgs))
	}
	if msgs[0].TestName != "Equals" {
		t.Errorf("unexpected TestName %q", msgs[0].TestName)
	}
	if msgs[0].Source != e {
		t.Error("message Source should be the failing entity")
	}
}

func TestRunChecksPassesClean(t *testing.T) {
	m := &Metadata{}
	m.Entity("resource", "r").
		Field("restricted_level", []string{"public"}, Required(), Equals("public"))
	if msgs := RunChecks(m); len(msgs) != 0 {
		t.Errorf("expected no failures, got %v", msgs)
	}
}
