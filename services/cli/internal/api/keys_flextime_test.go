package api

import "testing"

func TestFlexTime_DecodesStringObjectEpochNull(t *testing.T) {
	const epochStr = "2026-07-02T13:46:40Z" // time.Unix(1783000000,0).UTC()
	cases := map[string]string{
		`"2026-07-02T12:00:00Z"`:           "2026-07-02T12:00:00Z",
		`{"seconds":1783000000,"nanos":0}`: epochStr,
		`{"Seconds":1783000000,"Nanos":0}`: epochStr,
		`1783000000`:                       epochStr,
		`null`:                             "",
		`""`:                               "",
		`{"seconds":0,"nanos":0}`:          "",
	}
	for in, want := range cases {
		var f flexTime
		if err := f.UnmarshalJSON([]byte(in)); err != nil {
			t.Errorf("UnmarshalJSON(%s) error: %v", in, err)
			continue
		}
		if f.String() != want {
			t.Errorf("UnmarshalJSON(%s) = %q, want %q", in, f.String(), want)
		}
	}
}
