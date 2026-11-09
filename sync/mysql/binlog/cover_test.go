package binlog

import "testing"

// TestJSONDiffRemoveKey: removing a member of an object drops its key with
// its value, the others kept in order.
func TestJSONDiffRemoveKey(t *testing.T) {
	doc, err := parseJSON(jdoc(jobj{{"a", int16(1)}, {"b", "x"}, {"c", jarr{true}}}))
	if err != nil {
		t.Fatal(err)
	}
	doc, err = applyDiff(doc, jsonDiffRemove, "$.b", nil)
	if err != nil || doc.String() != `{"a": 1, "c": [true]}` {
		t.Fatal(doc, err)
	}
	doc, err = applyDiff(doc, jsonDiffRemove, "$.a", nil)
	if err != nil || doc.String() != `{"c": [true]}` {
		t.Fatal(doc, err)
	}
}
