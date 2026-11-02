package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"time"

	"github.com/m82pl/qnap-netspeed/internal/config"
)

// Entry is an active problem: one that was written to System Logs and has
// not been closed by a "back to normal" entry yet.
type Entry struct {
	Since        time.Time `json:"since"`
	LastNotified time.Time `json:"last_notified"`
	LastSeen     time.Time `json:"last_seen"`
	Level        int       `json:"level"`
	Message      string    `json:"message"`
	Recovery     string    `json:"recovery,omitempty"`
}

// State is data/state.json.
type State struct {
	Schema int               `json:"schema"`
	Notify map[string]*Entry `json:"notify"`
}

// ErrCorrupt is returned together with an empty State when state.json cannot
// be read as JSON. The caller carries on (an empty state at worst repeats one
// notification) and the next Save replaces the file.
var ErrCorrupt = errors.New("state.json is damaged")

// LoadState reads path. A missing file is an empty state.
func LoadState(path string) (*State, error) {
	st := &State{Schema: 1, Notify: map[string]*Entry{}}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return st, nil
	}
	if err != nil {
		return st, err
	}
	var in State
	if err := json.Unmarshal(data, &in); err != nil {
		return st, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	for k, e := range in.Notify {
		if e != nil {
			st.Notify[k] = e
		}
	}
	return st, nil
}

// Save writes the state atomically; the file is readable by everyone like the
// history files (it holds no secrets).
func (s *State) Save(path string) error {
	s.Schema = 1
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(path, append(data, '\n'), 0o644)
}

// Active returns the keys of the active problems, sorted.
func (s *State) Active() []string {
	keys := make([]string, 0, len(s.Notify))
	for k := range s.Notify {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
