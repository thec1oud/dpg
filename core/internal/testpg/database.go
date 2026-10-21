//go:build integration

package testpg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
)

// Database returns the connection string of an empty database of its own
// for the test. With DPG_TEST_POSTGRES set (a superuser's connection
// string) it creates the database on that server and drops it when the
// test ends, so a suite of many small tests needn't start a container
// each; without it, it starts a container (Start).
func Database(t *testing.T) string {
	t.Helper()
	admin := os.Getenv("DPG_TEST_POSTGRES")
	if admin == "" {
		return Start(t)
	}
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		t.Fatalf("testpg: %v", err)
	}
	name := "dpgtest_" + hex.EncodeToString(b)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("testpg: connect to DPG_TEST_POSTGRES: %v", err)
	}
	defer conn.Close(ctx)
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("testpg: create database: %v", err)
	}
	t.Cleanup(func() {
		conn, err := pgx.Connect(context.Background(), admin)
		if err != nil {
			t.Errorf("testpg: connect to drop %s: %v", name, err)
			return
		}
		defer conn.Close(context.Background())
		// FORCE ends the connections a failed test left open.
		if _, err := conn.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)"); err != nil {
			t.Errorf("testpg: drop database: %v", err)
		}
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatalf("testpg: DPG_TEST_POSTGRES: %v", err)
	}
	u.Path = "/" + name
	return u.String()
}
