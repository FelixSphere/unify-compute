package demand

import (
	"context"
	"testing"
)

type fakeStore struct{ rows [][2]string }

func (f *fakeStore) RecordGPUDemand(_ context.Context, by, sku string) error {
	f.rows = append(f.rows, [2]string{by, sku})
	return nil
}

func TestRecordWritesTheRowWithoutAMailer(t *testing.T) {
	f := &fakeStore{}
	s := &Service{Store: f}
	s.Record(context.Background(), "account:3", "a100-80gb-x1-neo")
	if len(f.rows) != 1 || f.rows[0] != [2]string{"account:3", "a100-80gb-x1-neo"} {
		t.Fatalf("rows %v", f.rows)
	}
}

func TestAccountIDReadsTheOwnerString(t *testing.T) {
	if accountID("account:42") != 42 || accountID("sk-key") != 0 || accountID("") != 0 {
		t.Fatal("owner parsing")
	}
}
