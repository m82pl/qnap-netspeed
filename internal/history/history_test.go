package history

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type rec struct {
	Timestamp time.Time `json:"timestamp"`
	Type      string    `json:"type"`
	Interface string    `json:"interface,omitempty"`
	N         int       `json:"n"`
}

var t0 = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func newStore(t *testing.T) Store {
	return Store{Path: filepath.Join(t.TempDir(), "data", "wan.jsonl")}
}

func TestAppendReadOrderAndFilters(t *testing.T) {
	s := newStore(t)
	for i, ifc := range []string{"eth0", "eth1", "eth0", "eth1"} {
		if err := s.Append(rec{t0.Add(time.Duration(i) * time.Hour), "WAN", ifc, i}); err != nil {
			t.Fatal(err)
		}
	}
	all, skipped, err := s.Read(Filter{})
	if err != nil || skipped != 0 || len(all) != 4 {
		t.Fatalf("all: %d records, skipped %d, err %v", len(all), skipped, err)
	}
	last2, _, _ := s.Read(Filter{Last: 2})
	if len(last2) != 2 || !last2[1].Timestamp.Equal(t0.Add(3*time.Hour)) {
		t.Errorf("last 2 wrong: %+v", last2)
	}
	eth1, _, _ := s.Read(Filter{Interface: "eth1"})
	if len(eth1) != 2 {
		t.Errorf("iface filter: %d", len(eth1))
	}
	since, _, _ := s.Read(Filter{Since: t0.Add(2 * time.Hour)})
	if len(since) != 2 {
		t.Errorf("since filter: %d", len(since))
	}
}

func TestReadMissingFile(t *testing.T) {
	recs, skipped, err := newStore(t).Read(Filter{})
	if err != nil || len(recs) != 0 || skipped != 0 {
		t.Fatalf("missing file: %v %d %d", err, len(recs), skipped)
	}
}

func TestReadSkipsDamagedLines(t *testing.T) {
	s := newStore(t)
	s.Append(rec{t0, "WAN", "eth0", 1})
	f, _ := os.OpenFile(s.Path, os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString("{\"timestamp\":\"2026-11-02T1\n") // torn write
	f.WriteString("not json at all\n")               // garbage
	f.WriteString("{\"type\":\"WAN\"}\n")            // no timestamp
	f.WriteString("\x00\x00\x00\n")                  // power-loss zeros
	f.Close()
	s.Append(rec{t0.Add(time.Hour), "WAN", "eth0", 2})
	recs, skipped, err := s.Read(Filter{})
	if err != nil || len(recs) != 2 || skipped != 4 {
		t.Fatalf("got %d records, %d skipped, err %v", len(recs), skipped, err)
	}
}

func TestReadLastLineWithoutNewline(t *testing.T) {
	s := newStore(t)
	os.MkdirAll(filepath.Dir(s.Path), 0o755)
	os.WriteFile(s.Path, []byte(`{"timestamp":"2026-11-02T12:00:00Z","type":"WAN"}`), 0o644)
	recs, _, _ := s.Read(Filter{})
	if len(recs) != 1 {
		t.Fatalf("want 1 record, got %d", len(recs))
	}
}

func TestAppendRejectsOversizeRecord(t *testing.T) {
	s := newStore(t)
	err := s.Append(map[string]string{"x": strings.Repeat("a", 5000)})
	if err == nil {
		t.Fatal("want error for a record over 4 KiB")
	}
	if _, statErr := os.Stat(s.Path); statErr == nil {
		t.Error("file must not be created for a rejected record")
	}
}

func TestConcurrentAppend(t *testing.T) {
	s := newStore(t)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := s.Append(rec{t0.Add(time.Duration(i) * time.Second), "WAN", "eth0", i}); err != nil {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	recs, skipped, _ := s.Read(Filter{})
	if len(recs) != 20 || skipped != 0 {
		t.Fatalf("%d records, %d skipped", len(recs), skipped)
	}
}

func TestRotate(t *testing.T) {
	s := newStore(t)
	for i := 0; i < 10; i++ {
		s.Append(rec{t0.Add(time.Duration(i) * time.Minute), "WAN", "", i})
	}
	if err := s.Rotate(10); err != nil { // nothing to do
		t.Fatal(err)
	}
	if recs, _, _ := s.Read(Filter{}); len(recs) != 10 {
		t.Fatalf("rotate at limit changed the file: %d", len(recs))
	}
	if err := s.Rotate(4); err != nil {
		t.Fatal(err)
	}
	recs, _, _ := s.Read(Filter{})
	if len(recs) != 4 || !recs[0].Timestamp.Equal(t0.Add(6*time.Minute)) {
		t.Fatalf("after rotate: %d records, first %v", len(recs), recs[0].Timestamp)
	}
	entries, _ := os.ReadDir(filepath.Dir(s.Path))
	if len(entries) != 1 {
		t.Errorf("temporary file left behind: %v", entries)
	}
	if err := s.Rotate(0); err == nil {
		t.Error("Rotate(0) must fail")
	}
}

func TestRotateMissingFile(t *testing.T) {
	if err := newStore(t).Rotate(100); err != nil {
		t.Fatal(err)
	}
}

func TestNewID(t *testing.T) {
	a, b := NewID(t0), NewID(t0.Add(time.Millisecond))
	if len(a) != 26 || len(b) != 26 {
		t.Fatalf("length: %q %q", a, b)
	}
	if a >= b {
		t.Errorf("IDs must sort by time: %q >= %q", a, b)
	}
	if a == NewID(t0) {
		t.Error("random part must differ")
	}
	for _, c := range a {
		if !strings.ContainsRune(crockford, c) {
			t.Fatalf("bad character %q in %s", c, a)
		}
	}
	// Known value: the time prefix of 2026-11-02T12:00:00Z.
	if got := NewID(time.Unix(0, 0))[:10]; got != "0000000000" {
		t.Errorf("epoch prefix: %s", got)
	}
}

func TestFilesAreWorldReadable(t *testing.T) { // `qnetspeed history` must work without root
	s := newStore(t)
	for i := 0; i < 6; i++ {
		s.Append(rec{t0.Add(time.Duration(i) * time.Minute), "WAN", "", i})
	}
	check := func(when string) {
		fi, err := os.Stat(s.Path)
		if err != nil || fi.Mode().Perm()&0o044 != 0o044 {
			t.Errorf("%s: %v %v", when, fi.Mode(), err)
		}
	}
	check("after append")
	if err := s.Rotate(3); err != nil {
		t.Fatal(err)
	}
	check("after rotate")
}
