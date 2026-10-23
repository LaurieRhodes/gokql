package engine

import (
	"sort"
	"testing"
	"time"
)

func TestDatetimeListTimezones(t *testing.T) {
	tbl := queryResult(t, `print n = array_length(datetime_list_timezones()), a = datetime_list_timezones()[0], z = datetime_list_timezones()[596]`)
	expectCell(t, tbl, 0, 0, "597")
	expectCell(t, tbl, 0, 1, "Africa/Abidjan")
	expectCell(t, tbl, 0, 2, "Zulu")
	if !sort.StringsAreSorted(kqlTimezones) {
		t.Error("zone list is not in ordinal order")
	}
	for _, z := range kqlTimezones {
		if _, err := time.LoadLocation(z); err != nil {
			t.Errorf("%s does not load: %v", z, err)
		}
	}
}
