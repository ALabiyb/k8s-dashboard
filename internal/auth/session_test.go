package auth

import (
	"testing"
	"time"
)

func TestSessionStore_CreateGetDelete(t *testing.T) {
	s := NewSessionStore()
	id := s.Create(&Session{
		Username:  "lsaid",
		Role:      RoleAdmin,
		Groups:    []string{"k8s-cluster-admins"},
		ExpiresAt: time.Now().Add(1 * time.Hour),
	})
	if id == "" {
		t.Fatal("Create returned empty id")
	}
	got := s.Get(id)
	if got == nil {
		t.Fatal("Get returned nil for freshly-created id")
	}
	if got.Username != "lsaid" || got.Role != RoleAdmin {
		t.Errorf("Get returned %+v, wanted lsaid/admin", got)
	}
	s.Delete(id)
	if s.Get(id) != nil {
		t.Error("Get returned non-nil after Delete")
	}
}

func TestSessionStore_ExpiredIsPurged(t *testing.T) {
	s := NewSessionStore()
	id := s.Create(&Session{
		Username:  "expired",
		ExpiresAt: time.Now().Add(-time.Second), // already expired
	})
	if s.Get(id) != nil {
		t.Error("Get returned an expired session")
	}
}

func TestSessionStore_GC(t *testing.T) {
	s := NewSessionStore()
	live := s.Create(&Session{
		Username:  "live",
		ExpiresAt: time.Now().Add(1 * time.Hour),
	})
	_ = s.Create(&Session{
		Username:  "dead",
		ExpiresAt: time.Now().Add(-time.Second),
	})
	if got := s.GC(); got != 1 {
		t.Errorf("GC returned %d, wanted 1", got)
	}
	if s.Get(live) == nil {
		t.Error("GC also purged the live session")
	}
}

func TestSessionStore_EmptyID(t *testing.T) {
	s := NewSessionStore()
	if s.Get("") != nil {
		t.Error("Get on empty id should return nil")
	}
}

func TestSessionStore_Update(t *testing.T) {
	s := NewSessionStore()
	id := s.Create(&Session{Username: "u", ExpiresAt: time.Now().Add(time.Hour)})
	s.Update(id, &Session{Username: "u", AccessToken: "new-token", ExpiresAt: time.Now().Add(time.Hour)})
	got := s.Get(id)
	if got == nil || got.AccessToken != "new-token" {
		t.Errorf("Update did not persist AccessToken: got %+v", got)
	}
	// Update on unknown id must not panic or insert.
	s.Update("no-such-id", &Session{Username: "ghost", ExpiresAt: time.Now().Add(time.Hour)})
	if s.Get("no-such-id") != nil {
		t.Error("Update on unknown id inserted a session")
	}
}
