package alerts

import (
	"testing"

	db "github.com/exploded/monitor/db/sqlc"
)

func TestFormat5xxBreakdown(t *testing.T) {
	rows := []db.Top5xxBetweenRow{
		{Host: "iondrift.mchugh.au", Status: 502, Cnt: 5, Paths: 1, SampleUri: "/"},
		{Host: "spending.mchugh.au", Status: 504, Cnt: 2, Paths: 2, SampleUri: "/supplier/9098"},
	}

	got := format5xxBreakdown(rows, 9)
	want := "\n• iondrift.mchugh.au: 5 × 502 on /" +
		"\n• spending.mchugh.au: 2 × 504 on 2 paths, e.g. /supplier/9098" +
		"\n• and 2 more"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestFormat5xxBreakdownNoRemainderWhenRowsOutrunCount(t *testing.T) {
	// A 5xx logged between the count and the breakdown query makes the rows sum
	// past the total. That must not produce "and -1 more".
	rows := []db.Top5xxBetweenRow{{Host: "a", Status: 500, Cnt: 6, Paths: 1, SampleUri: "/"}}
	if got, want := format5xxBreakdown(rows, 5), "\n• a: 6 × 500 on /"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestFormat5xxBreakdownEmpty(t *testing.T) {
	if got := format5xxBreakdown(nil, 5); got != "" {
		t.Errorf("no rows should add nothing, got %q", got)
	}
}
