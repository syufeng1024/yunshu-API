package gw

import (
	"context"
	"errors"
	"testing"

	"yunshu.dev/core/internal/apierr"
)

type fakeBacklog bool

func (f fakeBacklog) Exceeded(context.Context) bool { return bool(f) }

// The GR-03 gate rejects a NEW request with 503 backpressure_rejected before
// an in-flight slot is taken. An already running request never reaches P08.5
// again, which is what keeps in-flight traffic untouched.
func TestP085SettleBacklogRejectsNewRequest(t *testing.T) {
	d := &Deps{
		Backpressure:  NewBackpressure(nil),
		SettleBacklog: fakeBacklog(true),
	}
	err := NewBackpressureStage(d).Run(context.Background(), &Ctx{RequestID: "t"})
	if err == nil {
		t.Fatal("stage admitted a request above the settlement backlog line")
	}
	var se *StageError
	if !errors.As(err, &se) {
		t.Fatalf("err = %T %v, want *StageError", err, err)
	}
	if se.Stage != StageP085 {
		t.Fatalf("stage = %s, want %s", se.Stage, StageP085)
	}
	if se.API == nil || se.API.Code != apierr.CodeBackpressureRejected {
		t.Fatalf("api = %+v, want %s", se.API, apierr.CodeBackpressureRejected)
	}
	if se.API.HTTPStatus != 503 {
		t.Fatalf("status = %d, want 503", se.API.HTTPStatus)
	}
	if n := d.Backpressure.Inflight(); n != 0 {
		t.Fatalf("inflight = %d, want 0 (gate checked before Acquire)", n)
	}
}

func TestP085SettleBacklogBelowThresholdAdmits(t *testing.T) {
	d := &Deps{
		Backpressure:  NewBackpressure(nil),
		SettleBacklog: fakeBacklog(false),
	}
	c := &Ctx{RequestID: "t"}
	if err := NewBackpressureStage(d).Run(context.Background(), c); err != nil {
		t.Fatalf("stage rejected below threshold: %v", err)
	}
	if c.Slot == nil {
		t.Fatal("admitted request has no in-flight slot")
	}
	c.Slot.Release()
	if n := d.Backpressure.Inflight(); n != 0 {
		t.Fatalf("inflight after release = %d", n)
	}
}

// A nil reader preserves T08 behaviour: the gate is simply absent.
func TestP085NoBacklogReaderStillAdmits(t *testing.T) {
	d := &Deps{Backpressure: NewBackpressure(nil)}
	c := &Ctx{RequestID: "t"}
	if err := NewBackpressureStage(d).Run(context.Background(), c); err != nil {
		t.Fatalf("stage rejected without a backlog reader: %v", err)
	}
	c.Slot.Release()
}
