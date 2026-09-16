package bilibili

import (
	"encoding/json"
	"testing"
)

func TestParseFeedItemStringPubTS(t *testing.T) {
	body := `{"code":0,"data":{"has_more":true,"items":[{"id_str":"1","type":"DYNAMIC_TYPE_AV","modules":{"module_author":{"name":"海客新闻","mid":549748944,"pub_ts":"1789454019"},"module_dynamic":{"major":{"type":"MAJOR_TYPE_MAJOR","archive":{"aid":"117278897211332","bvid":"","title":"t","desc":"d","pic":"p","stat":{"view":"123","like":"4"}}}}}}]}}`
	var data feedData
	if err := json.Unmarshal(envelopeData(t, body), &data); err != nil {
		t.Fatalf("unmarshal feed data: %v", err)
	}
	if !data.HasMore {
		t.Fatalf("has_more: got false want true")
	}
	if len(data.Items) != 1 {
		t.Fatalf("items: got %d want 1", len(data.Items))
	}
	it := data.Items[0]
	if uint64(it.Modules.ModuleAuthor.PubTime) != 1789454019 {
		t.Fatalf("pub_ts string form: got %d", it.Modules.ModuleAuthor.PubTime)
	}
	if it.Modules.ModuleAuthor.Mid != 549748944 {
		t.Fatalf("mid: got %d", it.Modules.ModuleAuthor.Mid)
	}
	a := it.Modules.ModuleDynamic.Major.Archive
	if a == nil || uint64(a.Aid) != 117278897211332 {
		t.Fatalf("aid string form: got %+v", a)
	}
	if got := feedURLFor(&it); got != "https://www.bilibili.com/video/av117278897211332" {
		t.Fatalf("feedURLFor string aid: got %q", got)
	}
	if uint64(a.Stat.View) != 123 || uint64(a.Stat.Like) != 4 {
		t.Fatalf("stat string form: got %+v", a.Stat)
	}
}

func TestParseFeedItemNumericPubTS(t *testing.T) {
	body := `{"code":0,"data":{"has_more":false,"items":[{"id_str":"2","type":"DYNAMIC_TYPE_AV","modules":{"module_author":{"name":"up","mid":1,"pub_ts":1667129854}}}]}}`
	var data feedData
	if err := json.Unmarshal(envelopeData(t, body), &data); err != nil {
		t.Fatalf("unmarshal feed data: %v", err)
	}
	if uint64(data.Items[0].Modules.ModuleAuthor.PubTime) != 1667129854 {
		t.Fatalf("pub_ts numeric form: got %d", data.Items[0].Modules.ModuleAuthor.PubTime)
	}
}

func TestFlexInt64Forms(t *testing.T) {
	var f flexInt64
	for _, tt := range []struct {
		in  string
		val flexInt64
	}{
		{`"1789454019"`, 1789454019},
		{`1789454019`, 1789454019},
		{`null`, 0},
		{`""`, 0},
	} {
		f = 999
		if err := json.Unmarshal([]byte(tt.in), &f); err != nil {
			t.Fatalf("unmarshal %s: %v", tt.in, err)
		}
		if f != tt.val {
			t.Fatalf("unmarshal %s: got %d want %d", tt.in, f, tt.val)
		}
	}
}

func TestFlexInt64MarshalsAsNumber(t *testing.T) {
	b, err := json.Marshal(flexInt64(1789454019))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != "1789454019" {
		t.Fatalf("marshal: got %s want 1789454019", b)
	}
}

func TestFlexInt64BadInput(t *testing.T) {
	var f flexInt64
	if err := json.Unmarshal([]byte(`"abc"`), &f); err == nil {
		t.Fatalf("expected error for non-numeric string")
	}
}
