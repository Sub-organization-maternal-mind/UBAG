package ubag

import "testing"

func TestBuildTraceparent(t *testing.T) {
	tp := BuildTraceparent("0af7651916cd43dd8448eb211c80319c", "b7ad6b7169203331")
	want := "00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01"
	if tp != want {
		t.Fatalf("got %q want %q", tp, want)
	}
}

func TestParseTraceparent(t *testing.T) {
	tid, sid, ok := ParseTraceparent("00-0af7651916cd43dd8448eb211c80319c-b7ad6b7169203331-01")
	if !ok || tid != "0af7651916cd43dd8448eb211c80319c" || sid != "b7ad6b7169203331" {
		t.Fatalf("parse failed: %s %s %v", tid, sid, ok)
	}
	if _, _, ok := ParseTraceparent("garbage"); ok {
		t.Fatal("expected garbage to fail")
	}
}
