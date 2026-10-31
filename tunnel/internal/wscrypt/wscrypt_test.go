package wscrypt

import "testing"

func TestSession(t *testing.T) {
	secret, err := NewSecret("pw")
	if err != nil {
		t.Fatal(err)
	}
	sh, ch := Hello(), Hello()
	server, err := NewSession(secret, sh, ch, true)
	if err != nil {
		t.Fatal(err)
	}
	client, _ := NewSession(secret, sh, ch, false)
	for _, msg := range []string{"one", "two"} {
		got, err := server.Open(client.Seal([]byte(msg)))
		if err != nil || string(got) != msg {
			t.Fatalf("%q, %v", got, err)
		}
	}
	sealed := server.Seal([]byte("x"))
	if _, err := client.Open(sealed); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Open(sealed); err == nil {
		t.Fatal("a replayed message opened")
	}
	other, _ := NewSecret("other")
	wrong, _ := NewSession(other, sh, ch, false)
	if _, err := server.Open(wrong.Seal([]byte("x"))); err == nil {
		t.Fatal("a message of another password opened")
	}
}
