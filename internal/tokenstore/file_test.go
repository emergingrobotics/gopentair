package tokenstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/emergingrobotics/gopentair/internal/auth"
)

func TestFileRoundTrip(t *testing.T) {
	ctx := context.Background()
	p := filepath.Join(t.TempDir(), "sub", "tokens.json")
	f := NewFile(p)
	if _, err := f.Load(ctx, "us:a"); !errors.Is(err, auth.ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
	s := &auth.Session{IDToken: "i", AccessToken: "a", RefreshToken: "r", IDExpiry: time.Unix(100, 0).UTC()}
	if err := f.Save(ctx, "us:a", s); err != nil {
		t.Fatal(err)
	}
	if err := f.Save(ctx, "us:b", &auth.Session{RefreshToken: "rb"}); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode().Perm())
	}
	got, err := f.Load(ctx, "us:a")
	if err != nil || got.RefreshToken != "r" || !got.IDExpiry.Equal(s.IDExpiry) {
		t.Fatalf("got %+v %v", got, err)
	}
	if err := f.Delete(ctx, "us:a"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.Load(ctx, "us:b"); err != nil {
		t.Fatal("other account lost")
	}
}

func TestFileRejectsLoosePermissions(t *testing.T) {
	p := filepath.Join(t.TempDir(), "tokens.json")
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := NewFile(p).Load(context.Background(), "k"); err == nil {
		t.Fatal("expected permission error")
	}
}
