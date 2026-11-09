package wasm

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fitsync "github.com/lesismal/fit/sync"
)

const opDrop = 0x1a

// echoCodec is a codec plugin whose decode hands the bytes it is given
// back to records_emit, as records in the binary form, and whose
// fit_sync_form has form as its body, taking the form offered, its param,
// unless form says otherwise.
func echoCodec(t *testing.T, form []byte) string {
	t.Helper()
	m := wasmModule{
		imports: []wasmImport{
			{module: syncModule, name: "input", params: 2, results: 1},
			{module: syncModule, name: "records_emit", params: 2},
		},
		funcs: []wasmFunc{
			{export: exportDecode, params: 1, results: 1, body: cat(
				i32(0), []byte{opLocalGet, 0, opCall, 0, opDrop},
				i32(0), []byte{opLocalGet, 0, opCall, 1},
				i32(syncOK))},
		},
		pages: 1,
	}
	if form != nil {
		m.funcs = append(m.funcs, wasmFunc{export: exportForm, params: 1, results: 1, body: form})
	}
	path := filepath.Join(t.TempDir(), "echo.wasm")
	if err := os.WriteFile(path, m.encode(), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

var takeOffered = []byte{opLocalGet, 0}

// TestSyncBinaryEmit checks records_emit reads the records of the binary
// form a plugin hands back, each after its index, and refuses what is not.
func TestSyncBinaryEmit(t *testing.T) {
	m, err := loadSync(t, fitsync.Codecs, map[string]any{"path": echoCodec(t, takeOffered)})
	if err != nil {
		t.Fatal(err)
	}
	c := m.(*SyncCodec)
	if c.decode.form != formBinary {
		t.Fatalf("form %d", c.decode.form)
	}
	rec := records(3, 4)[0]
	emitted := func(index uint64, r *fitsync.Record) []byte {
		return r.AppendBinary(binary.AppendUvarint(nil, index))
	}
	var got []*fitsync.Record
	if err := c.Decode(emitted(0, rec), func(r *fitsync.Record) { got = append(got, r) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || js(t, got[0]) != js(t, rec) {
		t.Fatalf("%d records: %v", len(got), got)
	}
	if err := c.Decode(nil, func(*fitsync.Record) { t.Error("a record of nothing") }); err != nil {
		t.Error(err)
	}
	for _, bad := range []struct {
		data []byte
		want string
	}{
		{emitted(1, rec), "records_emit names record 1 of 1"},
		{emitted(0, rec)[:20], "records_emit: record 0: binary records cut short"},
		{[]byte{0x80}, "records_emit: binary records cut short"},
		{append(emitted(0, rec), emitted(0, rec)[:3]...), "cut short"},
	} {
		if err := c.Decode(bad.data, func(*fitsync.Record) {}); err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%x: %v, want %q", bad.data, err, bad.want)
		}
	}

	// Offered nothing, the plugin's records are JSON.
	withForm(formJSON, func() {
		m, err := loadSync(t, fitsync.Codecs, map[string]any{"path": echoCodec(t, takeOffered)})
		if err != nil {
			t.Fatal(err)
		}
		if f := m.(*SyncCodec).decode.form; f != formJSON {
			t.Errorf("form %d offered none", f)
		}
	})
	// Neither does a plugin that exports no fit_sync_form.
	m, err = loadSync(t, fitsync.Codecs, map[string]any{"path": echoCodec(t, nil)})
	if err != nil {
		t.Fatal(err)
	}
	if f := m.(*SyncCodec).decode.form; f != formJSON {
		t.Errorf("form %d without fit_sync_form", f)
	}
}

// TestSyncFormNegotiation checks a plugin's fit_sync_form must take a
// form offered, alike in every instance.
func TestSyncFormNegotiation(t *testing.T) {
	for _, bad := range []struct {
		form []byte
		want string
	}{
		{i32(7), "fit_sync_form took a form fit did not offer"},
		{[]byte{opUnreachable}, "fit_sync_form: wasm error: unreachable"},
	} {
		_, err := loadSync(t, fitsync.Codecs, map[string]any{"path": echoCodec(t, bad.form)})
		if err == nil || !strings.Contains(err.Error(), bad.want) {
			t.Errorf("%v, want %q", err, bad.want)
		}
	}

	m, err := loadSync(t, fitsync.Codecs, map[string]any{"path": echoCodec(t, takeOffered)})
	if err != nil {
		t.Fatal(err)
	}
	p := m.(*SyncCodec).decode
	// An instance made again after a failed call takes JSON, offered it,
	// where the first took the binary form.
	in := <-p.idle
	_ = in.mod.Close(context.Background())
	in.mod = nil
	p.idle <- in
	p.offer = formJSON
	if _, err := p.call(context.Background(), &syncCall{n: 1}); err == nil || !strings.Contains(err.Error(), "another record form") {
		t.Errorf("%v", err)
	}
	// Offered the binary form again, it is made as the first was.
	p.offer = formBinary
	if status, err := p.call(context.Background(), &syncCall{n: 1}); err != nil || status != syncOK {
		t.Errorf("%d %v", status, err)
	}
}
