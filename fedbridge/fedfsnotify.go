//go:build matrixlite

package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
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
	go func() {
		for !flag.Parsed() {
			time.Sleep(5 * time.Millisecond)
		}
		startFilesync()
	}()
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
			publish(channel_name, w.marshalEvent("remove", w.rel(path), 0, "", "", ""))
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
		sha := fileSHA256(path)
		chk := fileChunk(path)
		mimeVal := fileMIME(path)
		publish(channel_name, w.marshalEvent(ev, w.rel(path), stat.Size(), mimeVal, sha, chk))
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

func (w *syncWatch) marshalEvent(event, path string, size int64, mimeVal, sha256hex, chunk0b64 string) []byte {
	b, _ := json.Marshal(struct {
		Type   string `json:"type"`
		Event  string `json:"event"`
		Path   string `json:"path"`
		Size   int64  `json:"size"`
		Mime   string `json:"mime"`
		SHA256 string `json:"sha256"`
		Chunk0 string `json:"chunk0"`
	}{
		Type:   "filesync",
		Event:  event,
		Path:   path,
		Size:   size,
		Mime:   mimeVal,
		SHA256: sha256hex,
		Chunk0: chunk0b64,
	})
	return b
}

func fileSHA256(path string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}

func fileChunk(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, _ := f.Stat()
	if fi == nil || fi.Size() == 0 {
		return ""
	}
	size := fi.Size()

	maxChunk := size
	if maxChunk > 64*1024 {
		maxChunk = 64 * 1024
	}
	minChunk := int64(8 * 1024)
	if size < minChunk {
		minChunk = size
	}

	var chunkSize int64
	if maxChunk >= minChunk {
		chunkSize = rand.Int63n(maxChunk-minChunk+1) + minChunk
	} else {
		chunkSize = size
	}

	maxOffset := size - chunkSize
	offset := int64(0)
	if maxOffset > 0 {
		offset = rand.Int63n(maxOffset + 1)
	}

	f.Seek(offset, 0)
	buf := make([]byte, chunkSize)
	n, _ := io.ReadFull(f, buf)
	if n == 0 {
		return ""
	}
	return base64.StdEncoding.EncodeToString(buf[:n])
}

func fileMIME(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	if ext != "" {
		m := mime.TypeByExtension(ext)
		if m != "" {
			return m
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer f.Close()
	buf := make([]byte, 512)
	n, _ := io.ReadFull(f, buf)
	if n == 0 {
		return "application/octet-stream"
	}
	return http.DetectContentType(buf[:n])
}
