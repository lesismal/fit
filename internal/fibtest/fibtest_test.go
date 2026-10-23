package fibtest

import (
	"testing"
	"time"

	fibhttp "github.com/lesismal/fib/http"
)

func TestRecorder(t *testing.T) {
	rec := Serve(fibhttp.HandlerFunc(func(c *fibhttp.Context) {
		_ = c.Respond(201, "text/plain", []byte("made"))
	}), "GET", "http://example.com/")
	if !rec.Written || rec.Response.StatusCode != 201 || string(rec.Response.Body) != "made" {
		t.Fatalf("%v %d %q", rec.Written, rec.Response.StatusCode, rec.Response.Body)
	}
	if err := rec.WriteResponse(nil, fibhttp.Response{StatusCode: 200}); err == nil {
		t.Error("a second response was taken")
	}
	if err := rec.WriteInterim(103, nil); err == nil {
		t.Error("an interim response was taken")
	}
	if err := rec.Push(nil, "/x", nil); err == nil {
		t.Error("a push was taken")
	}
}

func TestWait(t *testing.T) {
	rec := &Recorder{}
	if rec.Wait(time.Millisecond) {
		t.Fatal("waited for nothing")
	}
	done := make(chan bool)
	go func() { done <- rec.Wait(10 * time.Second) }()
	if err := rec.WriteResponse(nil, fibhttp.Response{StatusCode: 204}); err != nil {
		t.Fatal(err)
	}
	if !<-done {
		t.Error("the response was not seen")
	}
}
