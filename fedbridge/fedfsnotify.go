//go:build matrixlite
package main

import (
	"encoding/json"
	"flag"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/bep/debounce"
	"github.com/fsnotify/fsnotify"
)

var syncDir string

type syncWatch struct {
	watcher   *fsnotify.Watcher
	root      string
	pending   map[string]fsnotify.Op
	mu        sync.Mutex
	debounced func(func())
}

func init() {
	flag.StringVar(&syncDir, "sync-dir", "", "enable file sync: watch directory for changes")
	starters = append(starters, startFilesync)
}

func startFilesync() {
	if syncDir == "" {
		return
	}
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("filesync: new watcher: %v", err)
		return
	}
	w := &syncWatch{
		watcher:   watcher,
		root:      syncDir,
		pending:   make(map[string]fsnotify.Op),
		debounced: debounce.New(100 * time.Millisecond),
	}
	if err := watcher.Add(syncDir); err != nil {
		log.Printf("filesync: add %s: %v", syncDir, err)
		watcher.Close()
		return
	}
	w.addRecursive(syncDir)
	go w.loop()
	log.Printf("filesync: watching %s", syncDir)
}

func (w *syncWatch) loop() {
	for {
		select {
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			w.handle(event)
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("filesync: %v", err)
		}
	}
}

func (w *syncWatch) handle(event fsnotify.Event) {
	if event.Op&fsnotify.Chmod != 0 {
		return
	}
	w.mu.Lock()
	w.pending[event.Name] |= event.Op
	w.mu.Unlock()
	w.debounced(w.flush)
}

func (w *syncWatch) flush() {
	w.mu.Lock()
	pending := w.pending
	w.pending = make(map[string]fsnotify.Op)
	w.mu.Unlock()

	for path, op := range pending {
		if op&(fsnotify.Remove|fsnotify.Rename) != 0 {
			w.watcher.Remove(path)
			publish(channel_name, w.marshalEvent("remove", w.rel(path)))
			continue
		}
		stat, err := os.Stat(path)
		if err != nil {
			continue
		}
		if stat.IsDir() {
			if op&fsnotify.Create != 0 {
				w.addRecursive(path)
			}
			continue
		}
		ev := "write"
		if op&fsnotify.Create != 0 {
			ev = "create"
		}
		publish(channel_name, w.marshalEvent(ev, w.rel(path)))
	}
}

func (w *syncWatch) rel(path string) string {
	rel, err := filepath.Rel(w.root, path)
	if err != nil {
		return path
	}
	return rel
}

func (w *syncWatch) addRecursive(dir string) {
	filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
		if err != nil {
			log.Printf("filesync: walk %s: %v", path, err)
			return nil
		}
		if fi == nil {
			return nil
		}
		if fi.IsDir() {
			if err := w.watcher.Add(path); err != nil {
				log.Printf("filesync: watch %s: %v", path, err)
			}
		}
		return nil
	})
}

func (w *syncWatch) marshalEvent(event, path string) []byte {
	b, _ := json.Marshal(struct {
		Type  string `json:"type"`
		Event string `json:"event"`
		Path  string `json:"path"`
	}{
		Type:  "filesync",
		Event: event,
		Path:  path,
	})
	return b
}
