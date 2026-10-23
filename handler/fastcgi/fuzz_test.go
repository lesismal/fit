package fastcgi

import "testing"

// FuzzReader checks that the record reader, fed anything in any pieces,
// neither panics nor keeps more than a record's worth.
func FuzzReader(f *testing.F) {
	f.Add(appendRecord(appendRecord(nil, typeStdout, []byte("Status: 200\r\n\r\nhi")), typeEndRequest, make([]byte, 8)), uint8(3))
	f.Add([]byte{1, 6, 0, 1, 0xff, 0xff, 0xff, 0}, uint8(1))
	f.Fuzz(func(t *testing.T, data []byte, step uint8) {
		var r reader
		n := max(int(step), 1)
		for i := 0; i < len(data); i += n {
			if err := r.feed(data[i:min(i+n, len(data))], func(rec record) error {
				if len(rec.content) > maxContentLen {
					t.Fatalf("a record of %d bytes", len(rec.content))
				}
				return nil
			}); err != nil {
				return
			}
			if len(r.buf) > headerLen+maxContentLen+255 {
				t.Fatalf("%d bytes kept", len(r.buf))
			}
		}
	})
}
