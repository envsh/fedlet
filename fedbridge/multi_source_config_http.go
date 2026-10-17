package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"time"
)

func init() {
	http.HandleFunc("/api/config", handleMultiSourceConfig)
}

type mscfgEntry struct {
	Value   string `json:"value"`
	Default string `json:"default"`
	Source  string `json:"source"`
	Applies string `json:"applies"`
	Type    string `json:"type"`
}

// mscfgType 判定 flag 的值类型,供前端选择控件。优先用 flag.Getter 拿真实
// Go 类型(标准库 stringValue/boolValue/intValue/durationValue 均实现 Getter);
// 对不实现 Getter 的(如 flag.Func 或自定义 Value)回退到反射类型名。
func mscfgType(v flag.Value) string {
	if g, ok := v.(flag.Getter); ok {
		switch g.Get().(type) {
		case time.Duration: // 必须最早判定(底层是 int64)
			return "duration"
		case bool:
			return "bool"
		case int, int64, uint, uint64, float64:
			return "number"
		case string:
			return "string"
		}
	}
	t := reflect.TypeOf(v)
	if t != nil && t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t != nil {
		switch strings.TrimSuffix(t.Name(), "Value") {
		case "bool":
			return "bool"
		case "int", "int64", "uint", "uint64", "float64":
			return "number"
		case "duration":
			return "duration"
		case "string":
			return "string"
		}
	}
	return "string"
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
	if mscfgHotFlags[name] || mscfgExtraExists(name) {
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
			val = toDisplaySecret(val)
		}
		out[f.Name] = mscfgEntry{
			Value:   val,
			Default: f.DefValue,
			Source:  mscfgGetSource(f.Name),
			Applies: mscfgApplies(f.Name),
			Type:    mscfgType(f.Value),
		}
	})
	for name, val := range mscfgExtraSnapshot(nil) {
		spec, _ := mscfgExtraSpecOf(name)
		out[name] = mscfgEntry{
			Value:   val,
			Default: spec.dft,
			Source:  mscfgGetSource(name),
			Applies: "hot",
			Type:    spec.typ,
		}
	}
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
	extras := map[string]string{}
	invalid := map[string]string{}
	var updated, restart, unknown, hot []string
	for name, raw := range req {
		val := mscfgStringify(raw)
		switch {
		case flag.CommandLine.Lookup(name) != nil:
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
		case mscfgExtraExists(name):
			extras[name] = val
		default:
			unknown = append(unknown, name)
		}
	}
	// 额外设置:成对整体校验,非法则整单拒绝(带中文范围/回滞提示)。
	if len(extras) > 0 && len(invalid) == 0 {
		snap := mscfgExtraSnapshot(extras)
		if bad, err := mscfgExtraVerify(extras, snap); err != nil {
			invalid[bad] = err.Error()
		} else {
			for name, val := range extras {
				updates[name] = val
				updated = append(updated, name)
				hot = append(hot, name)
			}
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
		mscfgExtraApply(mscfgExtraSnapshot(updates))
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
