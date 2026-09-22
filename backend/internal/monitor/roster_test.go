package monitor

import "testing"

func TestRosterFlagsAreRaisedOnlyPastTheirThresholds(t *testing.T) {
	cases := []struct {
		name string
		row  RosterRow
		want Flags
	}{
		{"nothing", RosterRow{Addresses: 1, PageLeft: LongAbsenceCount, AwayMs: LongAbsenceTotal.Milliseconds(),
			LargestPasteChars: LargePasteChars}, Flags{}},
		{"two addresses", RosterRow{Addresses: 2}, Flags{MultipleIPs: true}},
		{"an address change", RosterRow{Addresses: 1, IPChanges: 1}, Flags{MultipleIPs: true}},
		{"a parallel session", RosterRow{ParallelSessions: 1}, Flags{ParallelSessions: true}},
		{"too long away", RosterRow{AwayMs: LongAbsenceTotal.Milliseconds() + 1}, Flags{LongAbsence: true}},
		{"away too often", RosterRow{PageLeft: LongAbsenceCount + 1}, Flags{LongAbsence: true}},
		{"a blind answer", RosterRow{BlindCorrect: 1}, Flags{AnswerWithoutQueries: true}},
		{"a large paste", RosterRow{LargestPasteChars: LargePasteChars + 1}, Flags{LargePaste: true}},
		{"a shared query", RosterRow{IdenticalQueries: 1}, Flags{IdenticalQueries: true}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.row.Flags()
			if got != c.want {
				t.Errorf("flags = %+v, want %+v", got, c.want)
			}
			if got.Any() != (c.want != Flags{}) {
				t.Errorf("Any = %v", got.Any())
			}
		})
	}
}
