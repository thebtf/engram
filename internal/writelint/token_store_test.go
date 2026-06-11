// Package writelint — T031 RED/GREEN tests: TokenStore TTL + concurrent safety.
// goleak verifies no goroutine leak after Close.
package writelint_test

import (
	"fmt"
	"sync"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/thebtf/engram/internal/writelint"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// TestTokenStore_PutGet_T031 asserts Put→Get round-trip.
func TestTokenStore_PutGet_T031(t *testing.T) {
	ts := writelint.NewTokenStore(writelint.TokenStoreConfig{
		TTL:            5 * time.Second,
		JanitorInterval: 60 * time.Second,
	})
	defer ts.Close()

	if err := ts.Put("tok1", "payload1", 5*time.Second); err != nil {
		t.Fatalf("Put: %v", err)
	}
	val, ok, expired := ts.Get("tok1")
	if !ok {
		t.Fatal("Get: expected ok=true after Put")
	}
	if expired {
		t.Fatal("Get: expected expired=false immediately after Put")
	}
	if val != "payload1" {
		t.Fatalf("Get: expected payload1, got %q", val)
	}
}

// TestTokenStore_TTLExpiry_T031 asserts Get returns expired=true after TTL.
func TestTokenStore_TTLExpiry_T031(t *testing.T) {
	ts := writelint.NewTokenStore(writelint.TokenStoreConfig{
		TTL:            50 * time.Millisecond,
		JanitorInterval: 60 * time.Second,
	})
	defer ts.Close()

	if err := ts.Put("tok_expire", "data", 50*time.Millisecond); err != nil {
		t.Fatalf("Put: %v", err)
	}
	time.Sleep(80 * time.Millisecond)
	val, ok, expired := ts.Get("tok_expire")
	if !ok {
		t.Fatal("Get after expiry: expected ok=true (key still exists, just expired)")
	}
	if !expired {
		t.Fatalf("Get after expiry: expected expired=true, got val=%q", val)
	}
}

// TestTokenStore_MissingKey_T031 asserts Get returns ok=false for unknown key.
func TestTokenStore_MissingKey_T031(t *testing.T) {
	ts := writelint.NewTokenStore(writelint.TokenStoreConfig{
		TTL:            5 * time.Second,
		JanitorInterval: 60 * time.Second,
	})
	defer ts.Close()

	_, ok, _ := ts.Get("nonexistent")
	if ok {
		t.Fatal("Get nonexistent key: expected ok=false")
	}
}

// TestTokenStore_JanitorPurge_T031 asserts the janitor removes expired entries.
func TestTokenStore_JanitorPurge_T031(t *testing.T) {
	ts := writelint.NewTokenStore(writelint.TokenStoreConfig{
		TTL:            50 * time.Millisecond,
		JanitorInterval: 80 * time.Millisecond,
	})
	defer ts.Close()

	if err := ts.Put("purge_me", "gone", 50*time.Millisecond); err != nil {
		t.Fatalf("Put: %v", err)
	}
	// Wait for expiry + janitor cycle
	time.Sleep(200 * time.Millisecond)
	// After purge, Get should return ok=false (entry removed)
	_, ok, _ := ts.Get("purge_me")
	if ok {
		t.Fatal("after janitor purge: expected ok=false for expired+purged key")
	}
}

// TestTokenStore_Concurrent_T031 asserts race-free Put/Get under concurrency.
func TestTokenStore_Concurrent_T031(t *testing.T) {
	ts := writelint.NewTokenStore(writelint.TokenStoreConfig{
		TTL:            5 * time.Second,
		JanitorInterval: 60 * time.Second,
	})
	defer ts.Close()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			key := fmt.Sprintf("key-%d", n)
			if err := ts.Put(key, fmt.Sprintf("val-%d", n), 5*time.Second); err != nil {
				t.Errorf("Put %s: %v", key, err)
				return
			}
			val, ok, expired := ts.Get(key)
			if !ok || expired {
				t.Errorf("Get %s: ok=%v expired=%v", key, ok, expired)
				return
			}
			expected := fmt.Sprintf("val-%d", n)
			if val != expected {
				t.Errorf("Get %s: expected %q got %q", key, expected, val)
			}
		}(i)
	}
	wg.Wait()
}

// TestTokenStore_NoLeakAfterClose_T031 verifies no goroutine leak.
// goleak.VerifyTestMain in TestMain handles this implicitly; this test
// explicitly calls Close and asserts the janitor goroutine has stopped.
func TestTokenStore_NoLeakAfterClose_T031(t *testing.T) {
	ts := writelint.NewTokenStore(writelint.TokenStoreConfig{
		TTL:            5 * time.Second,
		JanitorInterval: 50 * time.Millisecond,
	})
	// Close immediately — janitor must stop.
	ts.Close()
	// Give goroutine scheduler a moment to settle
	time.Sleep(100 * time.Millisecond)
	// goleak.VerifyTestMain will catch any lingering goroutines after all tests.
}
