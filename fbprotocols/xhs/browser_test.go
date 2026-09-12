package xhs

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func writeOpenTestScript(t *testing.T, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestOpenURLFallbackToPathBrowser(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("platform opener fallback is linux-specific")
	}
	dir := t.TempDir()
	flag := filepath.Join(dir, "opened.flag")
	writeOpenTestScript(t, dir, "xdg-open", "#!/bin/sh\nexit 1\n")
	writeOpenTestScript(t, dir, "firefox", "#!/bin/sh\nprintf '%s\\n' \"$*\" >> "+flag+"\nexit 0\n")
	t.Setenv("PATH", dir)
	t.Setenv("BROWSER", "")

	url := "http://127.0.0.1:1/"
	if err := openURL(url); err != nil {
		t.Fatalf("openURL: %v", err)
	}
	b, err := os.ReadFile(flag)
	if err != nil {
		t.Fatalf("firefox fallback never ran: %v", err)
	}
	if !strings.Contains(string(b), url) {
		t.Fatalf("firefox got %q, want the url", string(b))
	}
}

func TestOpenURLAllCandidatesFail(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("platform opener fallback is linux-specific")
	}
	dir := t.TempDir()
	writeOpenTestScript(t, dir, "xdg-open", "#!/bin/sh\nexit 1\n")
	t.Setenv("PATH", dir)
	t.Setenv("BROWSER", "")

	if err := openURL("http://127.0.0.1:1/"); err == nil {
		t.Fatal("openURL succeeded, want an aggregate failure")
	}
}

func restoreBrowserSeams() (restore func()) {
	savedIs, savedOpen := isDesktopFn, openUrlFunc
	return func() {
		isDesktopFn, openUrlFunc = savedIs, savedOpen
	}
}

func TestOpenLoginBrowserHeadless(t *testing.T) {
	restore := restoreBrowserSeams()
	defer restore()
	isDesktopFn = func() bool { return false }
	openUrlFunc = func(string) error {
		t.Fatal("must not open the browser in a headless environment")
		return nil
	}
	openLoginBrowser("http://127.0.0.1:1/")
}

func TestOpenLoginBrowserOpenFailure(t *testing.T) {
	restore := restoreBrowserSeams()
	defer restore()
	isDesktopFn = func() bool { return true }
	openUrlFunc = func(string) error { return errors.New("engine missing") }
	openLoginBrowser("http://127.0.0.1:1/")
}

func TestOpenLoginBrowserOnce(t *testing.T) {
	restore := restoreBrowserSeams()
	defer restore()
	ui.mu.Lock()
	oldOpened := ui.opened
	ui.opened = false
	ui.mu.Unlock()
	t.Cleanup(func() {
		ui.mu.Lock()
		ui.opened = oldOpened
		ui.mu.Unlock()
	})

	isDesktopFn = func() bool { return true }
	n := 0
	openUrlFunc = func(string) error { n++; return nil }

	openLoginBrowserOnce("http://127.0.0.1:1/")
	openLoginBrowserOnce("http://127.0.0.1:1/")
	if n != 1 {
		t.Fatalf("browser opened %d times, want exactly 1 per UI lifetime", n)
	}
}
