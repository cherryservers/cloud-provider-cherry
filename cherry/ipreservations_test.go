package cherry

import (
	"testing"

	cherrygo "github.com/cherryservers/cherrygo/v3"
)

func TestIPReservationByAllTags(t *testing.T) {
	ips := []cherrygo.IPAddress{
		{Tags: &map[string]string{"a": "1", "b": "2"}},
		{Tags: &map[string]string{"c": "3", "d": "4"}},
		{Tags: &map[string]string{"a": "1", "d": "4"}},
		{Tags: &map[string]string{"b": "2", "c": "3"}},
		{Tags: &map[string]string{"b": "2", "q": "10"}},
	}
	tests := []struct {
		tags  map[string]string
		match int
	}{
		{map[string]string{"a": "1"}, 0},
		{map[string]string{"a": "1", "b": "2"}, 0},
		{map[string]string{"b": "2"}, 0},
		{map[string]string{"c": "3"}, 1},
		{map[string]string{"d": "4"}, 1},
		{map[string]string{"q": "10"}, 4},
		{map[string]string{"q": "10", "n": "8"}, -1},
	}

	for i, tt := range tests {
		matched := ipReservationByAllTags(tt.tags, ips)
		switch {
		case matched == nil && tt.match >= 0:
			t.Errorf("%d: found no match but expected index %d", i, tt.match)
		case matched != nil && tt.match < 0:
			t.Errorf("%d: found a match but expected none", i)
		case matched == nil && tt.match < 0:
			// this is good
		case matched != &ips[tt.match]:
			t.Errorf("%d: match did not find index %d", i, tt.match)
		}
	}
}
