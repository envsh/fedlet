package fbshared

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// InsertFlatFields 单个处理:把 extra 逐键并入一条原始 JSON 条目的 0 级(条目顶级)
// 并返回新字节。UseNumber 保大整数字面量;条目自身字段不碰;已有同名键不覆盖;
// 不做嵌套、不做批处理。
func InsertFlatFields(raw json.RawMessage, extra map[string]any) (json.RawMessage, error) {
	var m map[string]any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("fbshared: insert flat fields: %w", err)
	}
	for k, v := range extra {
		if _, dup := m[k]; !dup {
			m[k] = v
		}
	}
	return json.Marshal(m)
}