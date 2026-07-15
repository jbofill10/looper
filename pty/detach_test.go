package pty

import "testing"

func TestDetachScanner_PlainBytes(t *testing.T) {
	var d detachScanner
	out, detached := d.scan([]byte("hello"))
	if string(out) != "hello" {
		t.Errorf("passthrough = %q, want %q", out, "hello")
	}
	if detached {
		t.Errorf("detached = true, want false")
	}
}

func TestDetachScanner_CtrlBThenD_Detaches(t *testing.T) {
	var d detachScanner
	out, detached := d.scan([]byte{0x02, 'd'})
	if len(out) != 0 {
		t.Errorf("passthrough = %v, want empty", out)
	}
	if !detached {
		t.Errorf("detached = false, want true")
	}
}

func TestDetachScanner_CtrlBThenNonD_PassesThroughBoth(t *testing.T) {
	var d detachScanner
	out, detached := d.scan([]byte{0x02, 'x'})
	want := []byte{0x02, 'x'}
	if string(out) != string(want) {
		t.Errorf("passthrough = %v, want %v", out, want)
	}
	if detached {
		t.Errorf("detached = true, want false")
	}
}

func TestDetachScanner_SplitAcrossCalls_Detaches(t *testing.T) {
	var d detachScanner
	out1, detached1 := d.scan([]byte{0x02})
	if len(out1) != 0 {
		t.Errorf("call1 passthrough = %v, want empty", out1)
	}
	if detached1 {
		t.Errorf("call1 detached = true, want false")
	}
	out2, detached2 := d.scan([]byte{'d'})
	if len(out2) != 0 {
		t.Errorf("call2 passthrough = %v, want empty", out2)
	}
	if !detached2 {
		t.Errorf("call2 detached = false, want true")
	}
}

func TestDetachScanner_SplitAcrossCalls_NonD_PassesThroughBoth(t *testing.T) {
	var d detachScanner
	out1, detached1 := d.scan([]byte{0x02})
	if len(out1) != 0 || detached1 {
		t.Errorf("call1 = (%v, %v), want (empty, false)", out1, detached1)
	}
	out2, detached2 := d.scan([]byte{'x'})
	want := []byte{0x02, 'x'}
	if string(out2) != string(want) {
		t.Errorf("call2 passthrough = %v, want %v", out2, want)
	}
	if detached2 {
		t.Errorf("call2 detached = true, want false")
	}
}

func TestDetachScanner_MixedBytesAroundEscape(t *testing.T) {
	var d detachScanner
	out, detached := d.scan([]byte("ab"))
	if string(out) != "ab" || detached {
		t.Fatalf("prefix mismatch: %v %v", out, detached)
	}
	out, detached = d.scan([]byte{0x02})
	if len(out) != 0 || detached {
		t.Fatalf("arm mismatch: %v %v", out, detached)
	}
	out, detached = d.scan([]byte("cd"))
	// first byte 'c' after armed -> not 'd', emits Ctrl-b + 'c', disarms;
	// then plain 'd' passes through.
	want := []byte{0x02, 'c', 'd'}
	if string(out) != string(want) || detached {
		t.Fatalf("resolve mismatch: %v %v (want %v)", out, detached, want)
	}
}

func TestDetachScanner_DetachStopsConsumingRestOfInput(t *testing.T) {
	var d detachScanner
	out, detached := d.scan([]byte{0x02, 'd', 'x', 'y'})
	if !detached {
		t.Fatalf("detached = false, want true")
	}
	if len(out) != 0 {
		t.Errorf("passthrough = %v, want empty (rest of input after detach dropped)", out)
	}
}
