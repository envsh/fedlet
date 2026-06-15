package matrixlite

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type State struct {
	Server       string `json:"server"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token,omitempty"`
	UserID       string `json:"user_id"`
	DeviceID     string `json:"device_id"`
	NextBatch    string `json:"next_batch,omitempty"`
	SlidingPos   string `json:"sliding_pos,omitempty"`
	UseSliding   bool   `json:"use_sliding"`
}

func (s *State) Valid() bool {
	return s.AccessToken != "" && s.Server != ""
}

func statePath() string {
	dir := filepath.Join(os.Getenv("HOME"), ".config", "fedlet")
	return filepath.Join(dir, "matrixlite-state.json")
}

func (s *State) Load() error {
	raw, err := os.ReadFile(statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	return json.Unmarshal(raw, s)
}

func (s *State) Save() error {
	path := statePath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	raw, err := json.Marshal(s)
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, raw, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
