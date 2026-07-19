package store

import (
	"strings"
	"sync"
	"testing"
)

func TestAddValidatesInput(t *testing.T) {
	s := New(3)

	if _, err := s.Add(""); err != ErrEmptyMessage {
		t.Fatalf("expected ErrEmptyMessage, got %v", err)
	}

	long := strings.Repeat("x", MaxMessageLength+1)
	if _, err := s.Add(long); err != ErrMessageTooLong {
		t.Fatalf("expected ErrMessageTooLong, got %v", err)
	}
}

func TestAddAndList(t *testing.T) {
	s := New(3)

	p1, err := s.Add("first")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p1.ID != 1 {
		t.Fatalf("expected first ID to be 1, got %d", p1.ID)
	}

	got := s.List()
	if len(got) != 1 || got[0].Message != "first" {
		t.Fatalf("unexpected list contents: %+v", got)
	}
}

func TestCapacityEvictionOrder(t *testing.T) {
	s := New(3)
	s.Add("a")
	s.Add("b")
	s.Add("c")
	s.Add("d") // capacity is 3: "a" must be evicted

	got := s.List()
	if len(got) != 3 {
		t.Fatalf("expected 3 items after eviction, got %d", len(got))
	}
	// Most recent first.
	want := []string{"d", "c", "b"}
	for i, w := range want {
		if got[i].Message != w {
			t.Fatalf("expected %v most-recent-first, got %+v", want, got)
		}
	}
}

func TestConcurrentAddIsRaceFree(t *testing.T) {
	s := New(1000)
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := s.Add("concurrent"); err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	got := s.List()
	if len(got) != 100 {
		t.Fatalf("expected 100 items, got %d", len(got))
	}
}
