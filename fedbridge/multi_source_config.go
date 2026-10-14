package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	kjson "github.com/knadh/koanf/parsers/json"
	"github.com/knadh/koanf/providers/env/v2"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
)

const mscfgEnvPrefix = "FEDLET_"

var (
	mscfgSources   = map[string]string{} // flag 名 -> flag|env|file|default
	mscfgSourcesMu sync.Mutex
)

// liveCfg 保存运行期可热改的配置快照(flag 名 -> 字符串值)。
// 写侧由 HTTP PATCH 经 applyLive copy-on-write 更新,读侧在"用时"原子读取,
// 因此后端可无重启、无 Stop/Start 地拿到最新值。
var liveCfg atomic.Pointer[map[string]string]

// seedFromFlags 在 loadMultiSourceConfig 之后调用,把合并后的全部 flag 现值
// 快照进 liveCfg,作为 getter 的初始数据源。
func seedFromFlags(fs *flag.FlagSet) {
	m := map[string]string{}
	fs.VisitAll(func(f *flag.Flag) { m[f.Name] = f.Value.String() })
	liveCfg.Store(&m)
}

// liveGet 原子读取当前配置值;未初始化时返回空串。
func liveGet(name string) string {
	if m := liveCfg.Load(); m != nil {
		return (*m)[name]
	}
	return ""
}

// liveDuration 读取 duration 型配置;空值或非法时回退默认。
func liveDuration(name string, d time.Duration) time.Duration {
	if s := liveGet(name); s != "" {
		if v, err := time.ParseDuration(s); err == nil && v > 0 {
			return v
		}
	}
	return d
}

// makeMscfgGetter 返回一个"用时读取"配置的 getter,供后端 Start 注入。
// 每次调用从 liveCfg 原子读最新字符串,并按 dft 的类型解析:
//
//	string         → 原样返回
//	time.Duration  → time.ParseDuration(空/非正/非法回退 dft)
//	bool           → strconv.ParseBool
//	int            → strconv.Atoi
//	float64        → strconv.ParseFloat
//
// 未设置或解析失败时回退 dft。
func makeMscfgGetter[T any](name string, dft T) func() T {
	return func() T {
		s := liveGet(name)
		if s == "" {
			return dft
		}
		switch any(dft).(type) {
		case string:
			return any(s).(T)
		case time.Duration:
			return any(liveDuration(name, any(dft).(time.Duration))).(T)
		case bool:
			if v, err := strconv.ParseBool(s); err == nil {
				return any(v).(T)
			}
		case int:
			if v, err := strconv.Atoi(s); err == nil {
				return any(v).(T)
			}
		case float64:
			if v, err := strconv.ParseFloat(s, 64); err == nil {
				return any(v).(T)
			}
		}
		return dft
	}
}

// applyLive 热应用一批改动:先 flag.Set 保持 GET /api/config 显示一致,
// 再 copy-on-write 替换 atomic 快照,供各 getter 即时读到新值。
func applyLive(updates map[string]string) {
	for name, val := range updates {
		if f := flag.CommandLine.Lookup(name); f != nil {
			_ = f.Value.Set(val)
		}
	}
	cur := map[string]string{}
	if m := liveCfg.Load(); m != nil {
		for k, v := range *m {
			cur[k] = v
		}
	}
	for k, v := range updates {
		cur[k] = v
	}
	liveCfg.Store(&cur)
}

func mscfgSetSource(name, src string) {
	mscfgSourcesMu.Lock()
	mscfgSources[name] = src
	mscfgSourcesMu.Unlock()
}

func mscfgGetSource(name string) string {
	mscfgSourcesMu.Lock()
	defer mscfgSourcesMu.Unlock()
	if s, ok := mscfgSources[name]; ok {
		return s
	}
	return "default"
}

func multiSourceConfigPath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	return filepath.Join(home, ".config", "fedlet", "config.json")
}

func mscfgEnvTransform(k, v string) (string, any) {
	name := strings.TrimPrefix(k, mscfgEnvPrefix)
	name = strings.ToLower(name)
	name = strings.ReplaceAll(name, "_", "-")
	if name == "" {
		return "", nil
	}
	return name, v
}

func loadMultiSourceConfig() {
	applyMultiSourceConfig(flag.CommandLine, multiSourceConfigPath(), os.Environ())
}

func applyMultiSourceConfig(fs *flag.FlagSet, path string, environ []string) {
	explicit := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

	k := koanf.New(".")
	if path != "" {
		if _, err := os.Stat(path); err == nil {
			if err := k.Load(file.Provider(path), kjson.Parser()); err != nil {
				log.Printf("config: 解析 %s 失败: %v", path, err)
			}
		}
	}

	envKeys := map[string]bool{}
	ke := koanf.New(".")
	if err := ke.Load(env.Provider(".", env.Opt{
		Prefix:        mscfgEnvPrefix,
		TransformFunc: mscfgEnvTransform,
		EnvironFunc:   func() []string { return environ },
	}), nil); err != nil {
		log.Printf("config: 读取环境变量失败: %v", err)
	} else {
		for _, key := range ke.Keys() {
			_ = k.Set(key, ke.Get(key))
			envKeys[key] = true
		}
	}

	for _, key := range k.Keys() {
		if fs.Lookup(key) == nil {
			continue
		}
		if explicit[key] {
			mscfgSetSource(key, "flag")
			continue
		}
		val := k.Get(key)
		if val == nil {
			continue
		}
		if err := fs.Set(key, fmt.Sprint(val)); err != nil {
			log.Printf("config: 应用 %s=%v 失败: %v", key, val, err)
			continue
		}
		if envKeys[key] {
			mscfgSetSource(key, "env")
		} else {
			mscfgSetSource(key, "file")
		}
	}
}

func persistMultiSourceConfig(updates map[string]string) error {
	path := multiSourceConfigPath()
	if path == "" {
		return fmt.Errorf("无法确定配置路径")
	}
	cur := map[string]any{}
	if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
		_ = json.Unmarshal(b, &cur)
	}
	for k, v := range updates {
		cur[k] = v
	}
	out, err := json.MarshalIndent(cur, "", "  ")
	if err != nil {
		return err
	}
	out = append(out, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o600)
}
