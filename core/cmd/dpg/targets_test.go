package main

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestWithMissingDatabaseHint(t *testing.T) {
	missing := fmt.Errorf("executor: connect: %w", &pgconn.PgError{Code: "3D000", Message: `database "newdb" does not exist`})
	got := withMissingDatabaseHint(missing)
	if !strings.Contains(got.Error(), "DPG does not create databases") {
		t.Errorf("expected a hint for SQLSTATE 3D000, got %q", got)
	}
	if !errors.Is(got, missing) {
		t.Error("the hint must wrap the original error")
	}

	other := fmt.Errorf("executor: connect: %w", &pgconn.PgError{Code: "28P01"})
	if got := withMissingDatabaseHint(other); got != other {
		t.Errorf("expected other errors unchanged, got %q", got)
	}
}
