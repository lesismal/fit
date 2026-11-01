package yaml

import "testing"

// TestEmptyPlacesNothing checks an empty config places no path.
func TestEmptyPlacesNothing(t *testing.T) {
	r, err := Adapt([]byte("# nothing\n"))
	if err != nil {
		t.Fatal(err)
	}
	if line, col, ok := r.Position("listeners"); ok || line != 0 || col != 0 {
		t.Errorf("Position = %d %d %v", line, col, ok)
	}
}
