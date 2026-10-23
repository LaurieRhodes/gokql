package engine

import "testing"

// The emulator's answer on the same input.
func TestDiffPatternsText(t *testing.T) {
	tbl := queryResult(t, `range i from 0 to 199 step 1 | extend s = iff(i % 2 == 0, strcat("disk full on host ", tostring(i % 7), " at boot"), strcat("login failed for user ", tostring(i % 5))), c = i % 2 == 0 or i % 9 == 0 | evaluate diffpatterns_text(s, c)`)
	expectColNames(t, tbl, "Count_of_True", "Count_of_False", "Percent_of_True", "Percent_of_False", "Pattern")
	expectRows(t, tbl, 2)
	for i, w := range [][]string{{"100", "0", "90.09", "0", "disk full on host * at boot *"}, {"11", "89", "9.91", "100", "login failed for user *"}} {
		for j, v := range w {
			expectCell(t, tbl, i, j, v)
		}
	}
}
