// Package history stores test results as JSON Lines (spec pkt 13): one object
// per line, appended with O_APPEND, rotated by line count, tolerant of
// damaged lines.
package history

import (
	"bufio"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// maxLine is the largest line written (spec pkt 13: < 4 KiB keeps O_APPEND atomic).
const maxLine = 4096

// Record is the common part of every history line. The remaining fields are
// kept as raw JSON, so records of any type pass through unchanged.
type Record struct {
	Timestamp time.Time
	Type      string
	Interface string
	Raw       json.RawMessage
}

// Store is one history file.
type Store struct{ Path string }

// Append writes v as one line.
func (s Store) Append(v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if len(data) >= maxLine {
		return fmt.Errorf("history record is %d bytes, limit %d", len(data), maxLine-1)
	}
	if err := os.MkdirAll(filepath.Dir(s.Path), 0o750); err != nil {
		return err
	}
	f, err := os.OpenFile(s.Path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o640)
	if err != nil {
		return err
	}
	// One Write call: with O_APPEND concurrent writers cannot interleave.
	_, err = f.Write(append(data, '\n'))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

// Filter selects records. Zero values mean "no restriction".
type Filter struct {
	Since     time.Time
	Interface string
	Last      int // keep only the newest N (0 = all)
}

// Read returns matching records oldest first, and the number of damaged
// lines that were skipped. A missing file is an empty history.
func (s Store) Read(f Filter) ([]Record, int, error) {
	file, err := os.Open(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, err
	}
	defer file.Close()
	var out []Record
	skipped := 0
	r := bufio.NewReaderSize(file, 64*1024)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			rec, ok := parseLine(line)
			switch {
			case !ok:
				skipped++
			case !f.Since.IsZero() && rec.Timestamp.Before(f.Since):
			case f.Interface != "" && rec.Interface != f.Interface:
			default:
				out = append(out, rec)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, skipped, err
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Timestamp.Before(out[j].Timestamp) })
	if f.Last > 0 && len(out) > f.Last {
		out = out[len(out)-f.Last:]
	}
	return out, skipped, nil
}

func parseLine(line []byte) (Record, bool) {
	line = []byte(strings.TrimSpace(string(line)))
	if len(line) == 0 {
		return Record{}, false
	}
	var head struct {
		Timestamp time.Time `json:"timestamp"`
		Type      string    `json:"type"`
		Interface string    `json:"interface"`
	}
	if err := json.Unmarshal(line, &head); err != nil || head.Timestamp.IsZero() || head.Type == "" {
		return Record{}, false
	}
	return Record{Timestamp: head.Timestamp, Type: head.Type, Interface: head.Interface, Raw: line}, true
}

// Rotate keeps the last max lines when the file has more (spec pkt 13.4):
// they go to a temporary file that is renamed over the original.
// Damaged lines are dropped in the process.
func (s Store) Rotate(max int) error {
	if max <= 0 {
		return errors.New("history: max lines must be positive")
	}
	in, err := os.Open(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var lines [][]byte
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	total := 0
	for sc.Scan() {
		total++
		lines = append(lines, append([]byte(nil), sc.Bytes()...))
		if len(lines) > max {
			lines = lines[1:]
		}
	}
	in.Close()
	if err := sc.Err(); err != nil {
		return err
	}
	if total <= max {
		return nil
	}
	tmp, err := os.CreateTemp(filepath.Dir(s.Path), ".history-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	w := bufio.NewWriter(tmp)
	for _, l := range lines {
		w.Write(l)
		w.WriteByte('\n')
	}
	if err := w.Flush(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o640); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.Path)
}

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// NewID returns a ULID: 48-bit millisecond time and 80 random bits, 26 characters.
func NewID(now time.Time) string {
	var b [16]byte
	ms := uint64(now.UnixMilli())
	for i := 0; i < 6; i++ {
		b[i] = byte(ms >> (8 * (5 - i)))
	}
	rand.Read(b[6:])
	// 128 bits -> 26 base32 characters (first one holds 3 bits).
	var out [26]byte
	var acc uint32
	bits, pos := 0, 25
	for i := 15; i >= 0; i-- {
		acc |= uint32(b[i]) << bits
		bits += 8
		for bits >= 5 && pos >= 0 {
			out[pos] = crockford[acc&31]
			acc >>= 5
			bits -= 5
			pos--
		}
	}
	if pos >= 0 {
		out[pos] = crockford[acc&31]
	}
	return string(out[:])
}
