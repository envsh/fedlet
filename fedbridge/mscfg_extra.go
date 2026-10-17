package main

import (
	"flag"
	"log"
	"sync"
)

// mscfgExtraSpec 描述一个"没有命令行 flag 的设置项",让刻意不暴露 CLI 开关的
// 后端(chargectl)仍可通过 ~/.config/fedlet/config.json、FEDLET_* 环境变量与
// 网页配置页配置。
//
// verify/apply 收到的是"全部额外设置"的快照(当前值覆盖上本次改动),
// 因此像 chargectl 的 pause/resume 这类成对设置能做整体校验,而不是逐字段。
type mscfgExtraSpec struct {
	dft    string
	typ    string
	verify func(vals map[string]string) error
	apply  func(vals map[string]string) error
}

var (
	mscfgExtrasMu sync.Mutex
	mscfgExtras   = map[string]mscfgExtraSpec{}
)

// registerMscfgExtra 注册一个无 flag 设置;重名保留首个。
func registerMscfgExtra(name string, spec mscfgExtraSpec) {
	mscfgExtrasMu.Lock()
	defer mscfgExtrasMu.Unlock()
	if _, ok := mscfgExtras[name]; ok {
		return
	}
	if spec.typ == "" {
		spec.typ = "string"
	}
	mscfgExtras[name] = spec
}

func mscfgExtraExists(name string) bool {
	mscfgExtrasMu.Lock()
	defer mscfgExtrasMu.Unlock()
	_, ok := mscfgExtras[name]
	return ok
}

func mscfgExtraSpecOf(name string) (mscfgExtraSpec, bool) {
	mscfgExtrasMu.Lock()
	defer mscfgExtrasMu.Unlock()
	spec, ok := mscfgExtras[name]
	return spec, ok
}

// mscfgKnown 报告 name 是真实 flag 还是已注册的额外设置。
func mscfgKnown(name string) bool {
	return flag.CommandLine.Lookup(name) != nil || mscfgExtraExists(name)
}

// mscfgExtraSnapshot 返回全部额外设置的"投影快照":当前 liveCfg 值(缺失回退默认),
// 再叠加 updates 中涉及的键。verify/apply 都基于它。
func mscfgExtraSnapshot(updates map[string]string) map[string]string {
	mscfgExtrasMu.Lock()
	defer mscfgExtrasMu.Unlock()
	out := make(map[string]string, len(mscfgExtras))
	for name, spec := range mscfgExtras {
		v := liveGet(name)
		if v == "" {
			v = spec.dft
		}
		out[name] = v
	}
	for name, v := range updates {
		if _, ok := mscfgExtras[name]; ok {
			out[name] = v
		}
	}
	return out
}

// mscfgExtraVerify 对本次改动的额外设置做整体校验,返回首个出错项名与错误
// (供 PATCH 以 400 返回中文范围/回滞提示)。
func mscfgExtraVerify(updates, snapshot map[string]string) (string, error) {
	mscfgExtrasMu.Lock()
	defer mscfgExtrasMu.Unlock()
	for name := range updates {
		spec, ok := mscfgExtras[name]
		if !ok || spec.verify == nil {
			continue
		}
		if err := spec.verify(snapshot); err != nil {
			return name, err
		}
	}
	return "", nil
}

// mscfgExtraApply 依据已校验的快照运行各 apply 钩子;错误只记日志。
func mscfgExtraApply(snapshot map[string]string) {
	mscfgExtrasMu.Lock()
	specs := make(map[string]mscfgExtraSpec, len(mscfgExtras))
	for k, v := range mscfgExtras {
		specs[k] = v
	}
	mscfgExtrasMu.Unlock()
	for name, spec := range specs {
		if spec.apply == nil {
			continue
		}
		if err := spec.apply(snapshot); err != nil {
			log.Printf("config: 应用 %s=%v 失败: %v", name, snapshot[name], err)
		}
	}
}
