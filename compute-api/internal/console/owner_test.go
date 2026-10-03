package console

import "testing"

func TestOwnerIsTheBillingEntity(t *testing.T) {
	if got := (Identity{UserID: 1042, TokenID: 771}).Owner(); got != "user:1042" {
		t.Fatalf("user key: %q", got)
	}
	if got := (Identity{UserID: 1042, TenantID: 9, TokenID: 771}).Owner(); got != "tenant:9" {
		t.Fatalf("tenant key: %q", got)
	}
}
