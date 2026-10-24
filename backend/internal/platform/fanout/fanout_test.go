package fanout_test

import (
	"sync"
	"testing"
	"time"

	"github.com/devrdn/db-contest/backend/internal/platform/fanout"
	"github.com/google/uuid"
)

func received(c <-chan struct{}) bool {
	select {
	case <-c:
		return true
	case <-time.After(50 * time.Millisecond):
		return false
	}
}

func TestAPublishReachesEverySubscriptionOnItsTopicAndNoOther(t *testing.T) {
	hub := fanout.NewHub()
	contest, ana, ben := uuid.New(), uuid.New(), uuid.New()
	anaHints, closeAna := hub.Subscribe(contest, ana)
	defer closeAna()
	benHints, closeBen := hub.Subscribe(contest, ben)
	defer closeBen()

	hub.Publish(ana)
	if !received(anaHints) {
		t.Fatal("ana's subscription missed a publish to her own topic")
	}
	if received(benHints) {
		t.Fatal("ben's subscription heard a publish to ana's topic")
	}

	hub.Publish(contest)
	if !received(anaHints) || !received(benHints) {
		t.Fatal("a publish to the contest missed one of its subscriptions")
	}
}

// Hints coalesce: a subscriber that has not read yet holds one, however many
// were published, so a slow reader never blocks a publisher.
func TestHintsCoalesceAndAPublishNeverBlocks(t *testing.T) {
	hub := fanout.NewHub()
	topic := uuid.New()
	hints, closeFn := hub.Subscribe(topic)
	defer closeFn()

	done := make(chan struct{})
	go func() {
		for range 100 {
			hub.Publish(topic)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("publishing to a subscriber that does not read blocked")
	}
	if !received(hints) {
		t.Fatal("no hint after a hundred publishes")
	}
	if received(hints) {
		t.Fatal("more than one hint was held for an unread subscriber")
	}
}

func TestAClosedSubscriptionHearsNothingAndLeavesNothingBehind(t *testing.T) {
	hub := fanout.NewHub()
	topic := uuid.New()
	hints, closeFn := hub.Subscribe(topic)
	closeFn()
	closeFn()
	hub.Publish(topic)
	if received(hints) {
		t.Fatal("a closed subscription heard a publish")
	}
	if n := hub.Subscribers(topic); n != 0 {
		t.Fatalf("the hub still holds %d subscriptions on the topic", n)
	}
}

func TestSubscribingAndPublishingConcurrentlyIsSafe(t *testing.T) {
	hub := fanout.NewHub()
	topic := uuid.New()
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, closeFn := hub.Subscribe(topic, uuid.New())
			closeFn()
		}()
		go func() {
			defer wg.Done()
			hub.Publish(topic)
		}()
	}
	wg.Wait()
	if n := hub.Subscribers(topic); n != 0 {
		t.Fatalf("%d subscriptions left after every one closed", n)
	}
}
