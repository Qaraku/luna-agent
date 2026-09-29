package httpapi

import "testing"

func TestListenOccupiedAddressDoesNotChooseAnotherPort(t *testing.T) {
	first, err := Listen("127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Listen(first.Addr().String())
	if second != nil {
		second.Close()
		t.Fatal("occupied address silently produced another listener")
	}
	if err == nil {
		t.Fatal("occupied address must return an error")
	}
}
