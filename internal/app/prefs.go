package app

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
)

// The UI is loaded from memory, where browser storage isn't reliable, so
// its settings (theme, column widths, …) are kept in a small JSON file.

var prefsMu sync.Mutex

// SetPrefsFile sets where UI preferences are stored.
func (a *App) SetPrefsFile(path string) { a.prefsPath = path }

// Prefs returns all stored UI preferences.
func (a *App) Prefs() (map[string]json.RawMessage, error) {
	prefsMu.Lock()
	defer prefsMu.Unlock()
	return a.readPrefs(), nil
}

// SetPref stores one UI preference.
func (a *App) SetPref(key string, value json.RawMessage) error {
	if a.prefsPath == "" {
		return nil
	}
	prefsMu.Lock()
	defer prefsMu.Unlock()
	m := a.readPrefs()
	m[key] = value
	b, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	os.MkdirAll(filepath.Dir(a.prefsPath), 0o755)
	return os.WriteFile(a.prefsPath, b, 0o644)
}

func (a *App) readPrefs() map[string]json.RawMessage {
	m := map[string]json.RawMessage{}
	if a.prefsPath != "" {
		if b, err := os.ReadFile(a.prefsPath); err == nil {
			json.Unmarshal(b, &m)
		}
	}
	return m
}

// SetZoomFunc installs the native zoom hook (WebView2 zoom factor).
func (a *App) SetZoomFunc(fn func(float64)) { a.zoomFn = fn }

// ClampScale limits the interface scale to a sensible range.
func ClampScale(s float64) float64 {
	if s < 0.75 || s != s {
		return 0.75
	}
	if s > 2 {
		return 2
	}
	return s
}

// SetScale changes the interface size. It returns true when the window
// applied it natively; otherwise the page scales itself.
func (a *App) SetScale(scale float64) (bool, error) {
	scale = ClampScale(scale)
	b, _ := json.Marshal(scale)
	if err := a.SetPref("scale", b); err != nil {
		return false, err
	}
	if a.zoomFn == nil {
		return false, nil
	}
	a.zoomFn(scale)
	return true, nil
}

// SavedScale returns the stored interface scale (1 when unset).
func (a *App) SavedScale() float64 {
	prefsMu.Lock()
	defer prefsMu.Unlock()
	var s float64
	if v, ok := a.readPrefs()["scale"]; ok && json.Unmarshal(v, &s) == nil && s > 0 {
		return ClampScale(s)
	}
	return 1
}
