package xhs

import "testing"

func TestGoldenVectors(t *testing.T) {
	// JS-style CRC32 (from xhshow tests/test_public_api.py::test_crc32_js_int_basic)
	got := crc32JSString("I38rHdgsjopgIvesdVwgIC+oIELmBZ5e3VwXLgFTIxS3bqwErFeexd0ekncAzMFYnqthIhJeSBMDKutRI3KsYorWHPtGrbV0P9W")
	if got != 679790455 {
		t.Errorf("crc32JS golden: got %d want 679790455", got)
	}

	// sharding key (test_sharding_key_deterministic_and_ranged)
	if got := shardingKey("5ff0e6c70000000001008f9a"); got != 36 {
		t.Errorf("shardingKey golden: got %d want 36", got)
	}

	// webId = md5(a1) (test_generate_web_id_known_pair)
	if got := webIDFromA1("19ab1e5ce48c3b3c5c50e2c040a801cfcca0a4ac50000571046"); got != "9a1f41a6292d4e1742bb95f8f9fd1ad3" {
		t.Errorf("webID golden: got %q", got)
	}

	// a1 length / charset invariants
	a1 := generateA1()
	if len(a1) != 52 {
		t.Errorf("a1 length: got %d want 52", len(a1))
	}
	for _, c := range a1 {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')) {
			t.Fatalf("a1 has invalid char %q", c)
		}
	}

	// percent-encode parity with urllib.parse.quote(safe=",")
	if got := percentEncodeValue("a=b/c d,e~f"); got != "a%3Db%2Fc%20d,e~f" {
		t.Errorf("percentEncodeValue: got %q", got)
	}
}
