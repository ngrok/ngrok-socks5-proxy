package allowlist

import (
	"testing"
)

func TestParseRequiresPatterns(t *testing.T) {
	_, err := Parse(nil)
	if err == nil {
		t.Fatal("expected error for empty patterns")
	}
}

func TestParseRejectsGlobalWildcards(t *testing.T) {
	for _, pattern := range []string{"*", "*.*", "*:*"} {
		_, err := Parse([]string{pattern})
		if err == nil {
			t.Fatalf("expected error for global wildcard %q", pattern)
		}
	}
}

func TestParseRejectsShallowWildcard(t *testing.T) {
	_, err := Parse([]string{"*.com"})
	if err == nil {
		t.Fatal("expected error for shallow wildcard *.com")
	}
}

func TestParseRejectsMidWildcard(t *testing.T) {
	_, err := Parse([]string{"foo.*.com"})
	if err == nil {
		t.Fatal("expected error for mid-wildcard")
	}
}

func TestExactMatch(t *testing.T) {
	al, err := Parse([]string{"crm.corp.local"})
	if err != nil {
		t.Fatal(err)
	}

	if !al.IsAllowed("crm.corp.local", "443") {
		t.Error("expected crm.corp.local:443 to be allowed")
	}
	if !al.IsAllowed("crm.corp.local", "80") {
		t.Error("expected crm.corp.local:80 to be allowed (no port restriction)")
	}
	if al.IsAllowed("other.corp.local", "443") {
		t.Error("expected other.corp.local to be denied")
	}
}

func TestExactMatchWithPort(t *testing.T) {
	al, err := Parse([]string{"db.internal:5432"})
	if err != nil {
		t.Fatal(err)
	}

	if !al.IsAllowed("db.internal", "5432") {
		t.Error("expected db.internal:5432 to be allowed")
	}
	if al.IsAllowed("db.internal", "3306") {
		t.Error("expected db.internal:3306 to be denied")
	}
}

func TestWildcardMatch(t *testing.T) {
	al, err := Parse([]string{"*.corp.local"})
	if err != nil {
		t.Fatal(err)
	}

	if !al.IsAllowed("crm.corp.local", "443") {
		t.Error("expected crm.corp.local to be allowed")
	}
	if !al.IsAllowed("sso.corp.local", "443") {
		t.Error("expected sso.corp.local to be allowed")
	}
	if !al.IsAllowed("deep.sub.corp.local", "443") {
		t.Error("expected deep.sub.corp.local to be allowed")
	}
	if al.IsAllowed("corp.local", "443") {
		t.Error("expected corp.local (no subdomain) to be denied")
	}
	if al.IsAllowed("evil.com", "443") {
		t.Error("expected evil.com to be denied")
	}
}

func TestCaseInsensitive(t *testing.T) {
	al, err := Parse([]string{"CRM.Corp.Local"})
	if err != nil {
		t.Fatal(err)
	}

	if !al.IsAllowed("crm.corp.local", "443") {
		t.Error("expected case-insensitive match")
	}
	if !al.IsAllowed("CRM.CORP.LOCAL", "443") {
		t.Error("expected case-insensitive match")
	}
}

func TestMultipleRules(t *testing.T) {
	al, err := Parse([]string{"*.corp.local", "sso.partner.com", "db.internal:5432"})
	if err != nil {
		t.Fatal(err)
	}

	if !al.IsAllowed("crm.corp.local", "443") {
		t.Error("expected crm.corp.local to match wildcard")
	}
	if !al.IsAllowed("sso.partner.com", "443") {
		t.Error("expected sso.partner.com to match exact")
	}
	if !al.IsAllowed("db.internal", "5432") {
		t.Error("expected db.internal:5432 to match")
	}
	if al.IsAllowed("db.internal", "3306") {
		t.Error("expected db.internal:3306 to be denied")
	}
	if al.IsAllowed("evil.com", "80") {
		t.Error("expected evil.com to be denied")
	}
}
