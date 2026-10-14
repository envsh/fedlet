package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func resetMscfgState() {
	mscfgSourcesMu.Lock()
	mscfgSources = map[string]string{}
	mscfgSourcesMu.Unlock()
}

func TestMscfgEnvTransform(t *testing.T) {
	name, val := mscfgEnvTransform("FEDLET_NTFYSH_TOPIC", "abc")
	if name != "ntfysh-topic" || val != "abc" {
		t.Fatalf("got %q=%v", name, val)
	}
}

func TestMscfgIsSecret(t *testing.T) {
	cases := map[string]bool{
		"ntfysh-topic":     false,
		"misskey-timeline": false,
		"mozpush-uaid":     false,
		"imap-password":    true,
		"p2put-token":      true,
		"api-secret":       true,
	}
	for k, want := range cases {
		if got := mscfgIsSecret(k); got != want {
			t.Errorf("%s: got %v want %v", k, got, want)
		}
	}
}

func TestApplyMultiSourceConfig(t *testing.T) {
	resetMscfgState()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(`{"test-a":"from-file","test-b":"from-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var a, b, c string
	fs := flag.NewFlagSet("t", flag.ContinueOnError)
	fs.StringVar(&a, "test-a", "def", "")
	fs.StringVar(&b, "test-b", "def", "")
	fs.StringVar(&c, "test-c", "def", "")
	if err := fs.Parse([]string{"-test-a=from-cli"}); err != nil {
		t.Fatal(err)
	}
	applyMultiSourceConfig(fs, path, []string{"FEDLET_TEST_B=from-env", "FEDLET_TEST_C=from-env"})
	if a != "from-cli" {
		t.Fatalf("a=%q want from-cli", a)
	}
	if b != "from-env" {
		t.Fatalf("b=%q want from-env", b)
	}
	if c != "from-env" {
		t.Fatalf("c=%q want from-env", c)
	}
	if mscfgGetSource("test-a") != "flag" || mscfgGetSource("test-b") != "env" {
		t.Fatalf("sources: a=%s b=%s", mscfgGetSource("test-a"), mscfgGetSource("test-b"))
	}
}

func TestPersistMultiSourceConfig(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if err := persistMultiSourceConfig(map[string]string{"ntfysh-topic": "abc"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(multiSourceConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"ntfysh-topic": "abc"`) {
		t.Fatalf("config.json = %s", b)
	}
}

func TestLiveCfgApplyAndGet(t *testing.T) {
	liveCfg.Store(&map[string]string{})
	applyLive(map[string]string{"ntfysh-topic": "abc"})
	if got := liveGet("ntfysh-topic"); got != "abc" {
		t.Fatalf("liveGet = %q, want abc", got)
	}
	applyLive(map[string]string{"ntfysh-topic": "xyz"})
	if got := liveGet("ntfysh-topic"); got != "xyz" {
		t.Fatalf("liveGet after update = %q, want xyz", got)
	}
}

func TestLiveDuration(t *testing.T) {
	liveCfg.Store(&map[string]string{"sysinfo-interval": "10s"})
	if got := liveDuration("sysinfo-interval", 123*time.Second); got != 10*time.Second {
		t.Fatalf("liveDuration = %s, want 10s", got)
	}
	liveCfg.Store(&map[string]string{"sysinfo-interval": "bogus"})
	if got := liveDuration("sysinfo-interval", 123*time.Second); got != 123*time.Second {
		t.Fatalf("liveDuration fallback = %s, want 123s", got)
	}
}

func TestMscfgApplies(t *testing.T) {
	if got := mscfgApplies("sysinfo-interval"); got != "hot" {
		t.Errorf("sysinfo-interval = %q, want hot", got)
	}
	if got := mscfgApplies("ntfysh-topic"); got != "restart" {
		t.Errorf("ntfysh-topic = %q, want restart", got)
	}
}

func TestMakeMscfgGetter(t *testing.T) {
	liveCfg.Store(&map[string]string{
		"s": "hello", "d": "10s", "b": "true", "i": "42", "f": "3.5",
		"bad-d": "bogus", "bad-b": "maybe", "bad-i": "notint", "bad-f": "xyz",
	})
	if g := makeMscfgGetter("s", "def"); g() != "hello" {
		t.Errorf("string got %q", g())
	}
	if g := makeMscfgGetter("d", time.Second); g() != 10*time.Second {
		t.Errorf("duration got %s", g())
	}
	if g := makeMscfgGetter("b", false); g() != true {
		t.Errorf("bool got %v", g())
	}
	if g := makeMscfgGetter("i", 0); g() != 42 {
		t.Errorf("int got %d", g())
	}
	if g := makeMscfgGetter("f", 0.0); g() != 3.5 {
		t.Errorf("float got %v", g())
	}
	if g := makeMscfgGetter("missing", "dft"); g() != "dft" {
		t.Errorf("unset got %q", g())
	}
	if g := makeMscfgGetter("bad-d", time.Minute); g() != time.Minute {
		t.Errorf("bad duration got %s", g())
	}
	if g := makeMscfgGetter("bad-b", true); g() != true {
		t.Errorf("bad bool got %v", g())
	}
	if g := makeMscfgGetter("bad-i", 7); g() != 7 {
		t.Errorf("bad int got %d", g())
	}
	if g := makeMscfgGetter("bad-f", 1.0); g() != 1.0 {
		t.Errorf("bad float got %v", g())
	}
}
