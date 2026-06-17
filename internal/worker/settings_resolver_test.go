package worker

import (
	"context"
	"fmt"
	"testing"

	"gorm.io/gorm"

	"github.com/thebtf/engram/pkg/models"
)

// fakeSettingsReader is an in-memory settingsReader for adapter unit tests (no DB).
// A key mapped to a nil row + non-nil err simulates a store error; a missing key
// returns gorm.ErrRecordNotFound to mirror SettingsStore.Get's not-found behavior.
type fakeSettingsReader struct {
	rows map[string]*models.ModelSetting
	err  error // when non-nil, every Get returns this error
}

func (f fakeSettingsReader) Get(_ context.Context, key string) (*models.ModelSetting, error) {
	if f.err != nil {
		return nil, f.err
	}
	row, ok := f.rows[key]
	if !ok {
		return nil, fmt.Errorf("get setting %q: %w", key, gorm.ErrRecordNotFound)
	}
	return row, nil
}

// TestSettingsResolver_Get pins the CR-2 (#259) worker-adapter contract: a plain row
// resolves to (value,true); an encrypted row, a missing key, and a store error all
// resolve to ("",false) so the client falls through to env/default.
func TestSettingsResolver_Get(t *testing.T) {
	ctx := context.Background()

	t.Run("plain row resolves to value", func(t *testing.T) {
		r := &settingsResolver{reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{
			"reranker.url": {Key: "reranker.url", Value: "https://store.example.test", Encrypted: false},
		}}}
		got, ok := r.Get(ctx, "reranker.url")
		if !ok || got != "https://store.example.test" {
			t.Fatalf("Get = (%q, %v), want (%q, true)", got, ok, "https://store.example.test")
		}
	})

	t.Run("encrypted row reports absent (CR-2 serves non-secret only)", func(t *testing.T) {
		r := &settingsResolver{reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{
			"reranker.api_key": {Key: "reranker.api_key", Encrypted: true, EncryptedValue: []byte{0x01, 0x02}},
		}}}
		got, ok := r.Get(ctx, "reranker.api_key")
		if ok || got != "" {
			t.Fatalf("Get(encrypted) = (%q, %v), want (\"\", false)", got, ok)
		}
	})

	t.Run("missing key reports absent", func(t *testing.T) {
		r := &settingsResolver{reader: fakeSettingsReader{rows: map[string]*models.ModelSetting{}}}
		got, ok := r.Get(ctx, "absent.key")
		if ok || got != "" {
			t.Fatalf("Get(missing) = (%q, %v), want (\"\", false)", got, ok)
		}
	})

	t.Run("store error reports absent (never breaks client init)", func(t *testing.T) {
		r := &settingsResolver{reader: fakeSettingsReader{err: fmt.Errorf("connection refused")}}
		got, ok := r.Get(ctx, "reranker.url")
		if ok || got != "" {
			t.Fatalf("Get(error) = (%q, %v), want (\"\", false)", got, ok)
		}
	})

	t.Run("nil reader reports absent", func(t *testing.T) {
		r := newSettingsResolver(nil)
		got, ok := r.Get(ctx, "reranker.url")
		if ok || got != "" {
			t.Fatalf("Get(nil reader) = (%q, %v), want (\"\", false)", got, ok)
		}
	})
}
