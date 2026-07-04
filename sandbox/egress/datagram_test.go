package egress

import (
	"bytes"
	"net"
	"testing"
)

func TestDatagramRoundTrip(t *testing.T) {
	dst := &net.UDPAddr{IP: net.IPv4(8, 8, 8, 8), Port: 53}
	payload := []byte("a dns query")

	frame, err := EncodeDatagram(dst, payload)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	gotDst, gotPay, n, ok := DecodeDatagram(frame)
	if !ok || n != len(frame) {
		t.Fatalf("decode ok=%v n=%d want len=%d", ok, n, len(frame))
	}
	if gotDst != dst.String() || !bytes.Equal(gotPay, payload) {
		t.Fatalf("round-trip mismatch: dst=%q payload=%q", gotDst, gotPay)
	}

	// A partial frame yields ok=false (wait for more), never a panic.
	if _, _, _, ok := DecodeDatagram(frame[:len(frame)-1]); ok {
		t.Fatal("partial frame must be ok=false")
	}
	if _, _, _, ok := DecodeDatagram(nil); ok {
		t.Fatal("empty must be ok=false")
	}

	// Back-to-back frames are consumed one at a time by the reported length.
	two := append(append([]byte{}, frame...), frame...)
	_, _, n1, ok1 := DecodeDatagram(two)
	if !ok1 || n1 != len(frame) {
		t.Fatalf("first frame n=%d ok=%v", n1, ok1)
	}
	_, _, n2, ok2 := DecodeDatagram(two[n1:])
	if !ok2 || n2 != len(frame) {
		t.Fatalf("second frame n=%d ok=%v", n2, ok2)
	}
}
