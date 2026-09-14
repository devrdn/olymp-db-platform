package auth

import (
	"fmt"
	"testing"
)

func TestTheWaitingQueueRefusesAnAddressPastItsShare(t *testing.T) {
	q := newWaitingQueue(2)
	leaveA, ok := q.join("a")
	if !ok {
		t.Fatal("the first waiting attempt was refused")
	}
	if _, ok := q.join("a"); !ok {
		t.Fatal("the second waiting attempt was refused")
	}
	if _, ok := q.join("a"); ok {
		t.Error("a third waiting attempt from one address was admitted past a limit of two")
	}
	if _, ok := q.join("b"); !ok {
		t.Error("another address was refused for the first one's share")
	}

	leaveA()
	leaveA() // a second call must not free a place it never took
	if _, ok := q.join("a"); !ok {
		t.Error("an address was still refused after one of its attempts stopped waiting")
	}
	if _, ok := q.join("a"); ok {
		t.Error("leaving twice freed two places")
	}
}

func TestTheWaitingQueueForgetsAnAddressWithNobodyWaiting(t *testing.T) {
	q := newWaitingQueue(2)
	leave1, _ := q.join("a")
	leave2, _ := q.join("a")
	leave1()
	leave2()
	if len(q.waiting) != 0 {
		t.Errorf("the table holds %d entries with nobody waiting, want 0", len(q.waiting))
	}
}

func TestTheWaitingQueueTableIsBounded(t *testing.T) {
	q := newWaitingQueue(1)
	for i := range maxWaitingAddresses + 10 {
		if _, ok := q.join(fmt.Sprintf("addr-%d", i)); !ok {
			t.Fatalf("address %d was refused although it had nobody waiting", i)
		}
	}
	if len(q.waiting) > maxWaitingAddresses {
		t.Errorf("the table holds %d addresses, want at most %d", len(q.waiting), maxWaitingAddresses)
	}
}
