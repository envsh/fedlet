package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"sort"
	"strings"
)

func init() {
	http.HandleFunc("/api/config", handleMultiSourceConfig)
}

type mscfgEntry struct {
	Value   string `json:"value"`
	Default string `json:"default"`
	Source  string `json:"source"`
	Applies string `json:"applies"`
}

func mscfgIsSecret(name string) bool {
	lower := strings.ToLower(name)
	if strings.Contains(lower, "password") || strings.Contains(lower, "secret") {
		return true
	}
	if strings.HasSuffix(lower, "auth") {
		return true
	}
	for _, seg := range strings.Split(lower, "-") {
		switch seg {
		case "auth", "token", "key", "passwd":
			return true
		}
	}
	return false
}

func mscfgStringify(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

// mscfgHotFlags 列出已改造为"用时读取"、无需重启即可生效的 flag。
// 其余 flag 仍标记 restart(改动已持久化,但需重启才生效)。
var mscfgHotFlags = map[string]bool{
	"sysinfo-interval": true,
	"misskey-timeline": true,
}

func mscfgApplies(name string) string {
	if mscfgHotFlags[name] {
		return "hot"
	}
	return "restart"
}

// mscfgValidateValue 试探性解析 flag 值:成功则恢复原值(真正的应用交给
// applyLive 统一完成),失败返回错误。用于 PATCH 前的类型校验。
func mscfgValidateValue(name, val string) error {
	f := flag.CommandLine.Lookup(name)
	if f == nil {
		return fmt.Errorf("unknown flag %q", name)
	}
	old := f.Value.String()
	if err := f.Value.Set(val); err != nil {
		return err
	}
	_ = f.Value.Set(old)
	return nil
}

func handleMultiSourceConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		mscfgHandleGet(w, r)
	case http.MethodPatch, http.MethodPut:
		mscfgHandlePatch(w, r)
	default:
		w.Header().Set("Allow", "GET, PATCH, PUT")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func mscfgHandleGet(w http.ResponseWriter, r *http.Request) {
	out := map[string]mscfgEntry{}
	flag.CommandLine.VisitAll(func(f *flag.Flag) {
		val := f.Value.String()
		if mscfgIsSecret(f.Name) {
			val = maskSecret(val)
		}
		out[f.Name] = mscfgEntry{
			Value:   val,
			Default: f.DefValue,
			Source:  mscfgGetSource(f.Name),
			Applies: mscfgApplies(f.Name),
		}
	})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func mscfgHandlePatch(w http.ResponseWriter, r *http.Request) {
	var req map[string]any
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	updates := map[string]string{}
	invalid := map[string]string{}
	var updated, restart, unknown, hot []string
	for name, raw := range req {
		if flag.CommandLine.Lookup(name) == nil {
			unknown = append(unknown, name)
			continue
		}
		val := mscfgStringify(raw)
		if err := mscfgValidateValue(name, val); err != nil {
			invalid[name] = err.Error()
			continue
		}
		updates[name] = val
		updated = append(updated, name)
		if mscfgApplies(name) == "hot" {
			hot = append(hot, name)
		} else {
			restart = append(restart, name)
		}
	}
	// 任一已知 flag 的值非法 → 整单拒绝,不落盘、不热应用。
	if len(invalid) > 0 {
		sort.Strings(unknown)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"invalid": invalid,
			"unknown": unknown,
		})
		return
	}
	if len(updates) > 0 {
		applyLive(updates)
		if err := persistMultiSourceConfig(updates); err != nil {
			http.Error(w, "persist failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	sort.Strings(updated)
	sort.Strings(restart)
	sort.Strings(unknown)
	sort.Strings(hot)
	code := http.StatusOK
	if len(unknown) > 0 {
		code = http.StatusMultiStatus
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"updated":          updated,
		"hot_applied":      hot,
		"restart_required": restart,
		"unknown":          unknown,
	})
}
