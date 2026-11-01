package main

import (
	"encoding/json"
	"errors"
	"github.com/emsnoel/tandem/internal/exitcode"
	"testing"
	"time"
)

func TestWaitForRunReturnsOnAListedState(t *testing.T) {
	states := []string{"running", "running", "paused"}
	i := 0
	read := func() (statusView, error) {
		s := statusView{State: states[i]}
		if i < len(states)-1 {
			i++
		}
		return s, nil
	}
	clock := time.Unix(0, 0)
	v, ok, err := waitForRun(read, nil, []string{"paused", "finished"}, "", 10*time.Second, func() time.Time { return clock }, func(d time.Duration) { clock = clock.Add(d) })
	if err != nil || !ok || v.State != "paused" {
		t.Fatalf("v=%+v ok=%v err=%v", v, ok, err)
	}
}

func TestWaitForRunMatchesSentOnTheCurrentAttempt(t *testing.T) {
	var v statusView
	if err := json.Unmarshal([]byte(`{"state":"running","tasks":[{"accepted":true,"attempts":[{"current":true,"dispatch":"sent"}]},{"accepted":false,"attempts":[{"current":false,"dispatch":"failed"},{"current":true,"dispatch":"sent"}]}]}`), &v); err != nil {
		t.Fatal(err)
	}
	clock := time.Unix(0, 0)
	got, ok, err := waitForRun(func() (statusView, error) { return v, nil }, nil, []string{"sent"}, "", time.Second, func() time.Time { return clock }, func(d time.Duration) { clock = clock.Add(d) })
	if err != nil || !ok || got.State != "running" {
		t.Fatalf("ok=%v err=%v; the unaccepted task's current attempt was sent", ok, err)
	}
}

func TestWaitForRunTimesOutWithTheLastState(t *testing.T) {
	clock := time.Unix(0, 0)
	ticks := 0
	v, ok, err := waitForRun(func() (statusView, error) { return statusView{State: "running"}, nil }, func() error { ticks++; return nil },
		[]string{"paused"}, "", 2*time.Second, func() time.Time { return clock }, func(d time.Duration) { clock = clock.Add(d) })
	if err != nil || ok || v.State != "running" {
		t.Fatalf("v=%+v ok=%v err=%v, want a timeout that reports running", v, ok, err)
	}
	if ticks == 0 {
		t.Error("--tick ran no tick")
	}
}

func fakeClock() (func() time.Time, func(time.Duration)) {
	clock := time.Unix(0, 0)
	return func() time.Time { return clock }, func(d time.Duration) { clock = clock.Add(d) }
}

func TestWaitForRunUntilNotReturnsOnAnyOtherState(t *testing.T) {
	now, sleep := fakeClock()
	states := []string{"running", "stopping"}
	i := 0
	read := func() (statusView, error) { s := statusView{State: states[i]}; i = min(i+1, 1); return s, nil }
	v, ok, err := waitForRun(read, nil, []string{"sent"}, "running", time.Minute, now, sleep)
	if err != nil || !ok || v.State != "stopping" {
		t.Fatalf("v=%+v ok=%v err=%v", v, ok, err)
	}
	v, ok, _ = waitForRun(func() (statusView, error) { return statusView{State: "running"}, nil }, nil, []string{"sent"}, "running", time.Second, now, sleep)
	if ok {
		t.Fatalf("a running run matched --until-not running: %+v", v)
	}
}

func TestWaitForRunKeepsGoingAfterAFailedRead(t *testing.T) {
	now, sleep := fakeClock()
	reads := 0
	read := func() (statusView, error) {
		reads++
		if reads < 3 {
			return statusView{}, errors.New("busy")
		}
		return statusView{State: "paused"}, nil
	}
	v, ok, err := waitForRun(read, nil, []string{"paused"}, "", time.Minute, now, sleep)
	if err != nil || !ok || v.State != "paused" || reads != 3 {
		t.Fatalf("v=%+v ok=%v err=%v reads=%d", v, ok, err, reads)
	}
}

func TestWaitForRunReturnsTheReadErrorOnlyAtTheDeadline(t *testing.T) {
	now, sleep := fakeClock()
	start := now()
	reads := 0
	_, ok, err := waitForRun(func() (statusView, error) { reads++; return statusView{}, errors.New("no run") }, nil, []string{"paused"}, "", 2*time.Second, now, sleep)
	if ok || err == nil {
		t.Fatalf("ok=%v err=%v, want the read error", ok, err)
	}
	if reads < 2 || now().Sub(start) < 2*time.Second {
		t.Fatalf("gave up after %d reads at %v, before the deadline", reads, now().Sub(start))
	}
}

func TestBuildWaitFieldsSettled(t *testing.T) {
	d := durableRepo(t)
	out, rc := captureStdoutRC(t, func() int {
		return cmdBuild([]string{"wait", "--root", d.root, "--state-home", d.home, "--until", "running", "--timeout", "1000", "--fields"})
	})
	if rc != exitcode.OK || out != "running\x00" {
		t.Fatalf("rc %d, out %q", rc, out)
	}
}

// Review focus 3: a timeout still prints the last state, so unpair can say
// which state the run was stuck in.
func TestBuildWaitFieldsTimeout(t *testing.T) {
	d := durableRepo(t)
	out, rc := captureStdoutRC(t, func() int {
		return cmdBuild([]string{"wait", "--root", d.root, "--state-home", d.home, "--until", "finished", "--timeout", "0", "--fields"})
	})
	if rc != exitcode.ManualGate || out != "running\x00" {
		t.Fatalf("rc %d, out %q", rc, out)
	}
}
