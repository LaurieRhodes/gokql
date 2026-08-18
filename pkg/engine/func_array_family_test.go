package engine

import "testing"

// func_array_family_test.go — array_rotate_left/right, array_split,
// array_sum, array_iff/array_iif, added 2026-08-18. Every case below
// is taken directly from real ADX's own docs' worked examples.

// --- array_rotate_left / array_rotate_right ---

func TestArrayRotateLeftPositive(t *testing.T) {
	result := queryResult(t, `print arr=dynamic([1,2,3,4,5]) | extend r=array_rotate_left(arr, 2)`)
	want := `[3,4,5,1,2]`
	if got := result.Rows[0][1]; got != want {
		t.Errorf("array_rotate_left([1,2,3,4,5], 2) = %v, want %v", got, want)
	}
}

func TestArrayRotateLeftNegative(t *testing.T) {
	result := queryResult(t, `print arr=dynamic([1,2,3,4,5]) | extend r=array_rotate_left(arr, -2)`)
	want := `[4,5,1,2,3]`
	if got := result.Rows[0][1]; got != want {
		t.Errorf("array_rotate_left([1,2,3,4,5], -2) = %v, want %v", got, want)
	}
}

func TestArrayRotateRightPositive(t *testing.T) {
	result := queryResult(t, `print arr=dynamic([1,2,3,4,5]) | extend r=array_rotate_right(arr, 2)`)
	want := `[4,5,1,2,3]`
	if got := result.Rows[0][1]; got != want {
		t.Errorf("array_rotate_right([1,2,3,4,5], 2) = %v, want %v", got, want)
	}
}

func TestArrayRotateRightNegative(t *testing.T) {
	result := queryResult(t, `print arr=dynamic([1,2,3,4,5]) | extend r=array_rotate_right(arr, -2)`)
	want := `[3,4,5,1,2]`
	if got := result.Rows[0][1]; got != want {
		t.Errorf("array_rotate_right([1,2,3,4,5], -2) = %v, want %v", got, want)
	}
}

// TestArrayRotateEmptyArray guards a zero-length array doesn't panic
// on the modular-arithmetic normalization (division/modulo by n=0).
func TestArrayRotateEmptyArray(t *testing.T) {
	result := queryResult(t, `print arr=dynamic([]) | extend r=array_rotate_left(arr, 3)`)
	want := `[]`
	if got := result.Rows[0][1]; got != want {
		t.Errorf("array_rotate_left([], 3) = %v, want %v", got, want)
	}
}

// --- array_split ---

func TestArraySplitScalarIndex(t *testing.T) {
	result := queryResult(t, `print arr=dynamic([1,2,3,4,5]) | extend r=array_split(arr, 2)`)
	want := `[[1,2],[3,4,5]]`
	if got := result.Rows[0][1]; got != want {
		t.Errorf("array_split([1,2,3,4,5], 2) = %v, want %v", got, want)
	}
}

func TestArraySplitDynamicArrayIndex(t *testing.T) {
	result := queryResult(t, `print arr=dynamic([1,2,3,4,5]) | extend r=array_split(arr, dynamic([1,3]))`)
	want := `[[1],[2,3],[4,5]]`
	if got := result.Rows[0][1]; got != want {
		t.Errorf("array_split([1,2,3,4,5], [1,3]) = %v, want %v", got, want)
	}
}

// TestArraySplitNegativeIndex guards real ADX's own documented (but
// not worked-example-exercised) rule: "negative values are converted
// to array_length + value" -- -1 on a 5-element array means index 4.
func TestArraySplitNegativeIndex(t *testing.T) {
	result := queryResult(t, `print arr=dynamic([1,2,3,4,5]) | extend r=array_split(arr, -1)`)
	want := `[[1,2,3,4],[5]]`
	if got := result.Rows[0][1]; got != want {
		t.Errorf("array_split([1,2,3,4,5], -1) = %v, want %v", got, want)
	}
}

// --- array_sum ---

func TestArraySumWorkedExample(t *testing.T) {
	result := queryResult(t, `print arr=dynamic([1,2,3,4]) | extend r=array_sum(arr)`)
	got, ok := result.Rows[0][1].(float64)
	if !ok || got != 10 {
		t.Errorf("array_sum([1,2,3,4]) = %v (%T), want float64(10)", result.Rows[0][1], result.Rows[0][1])
	}
}

// TestArraySumNonNumericReturnsNull guards real ADX's own documented
// "if the array contains elements of non-numeric types, the result
// is null" rule.
func TestArraySumNonNumericReturnsNull(t *testing.T) {
	result := queryResult(t, `print arr=dynamic([1,2,"three"]) | extend r=array_sum(arr)`)
	if result.Rows[0][1] != nil {
		t.Errorf("array_sum([1,2,\"three\"]) = %v, want null", result.Rows[0][1])
	}
}

// --- array_iff / array_iif ---

func TestArrayIffAllArrayArguments(t *testing.T) {
	result := queryResult(t, `print condition=dynamic([true,false,true]), if_true=dynamic([1,2,3]), if_false=dynamic([4,5,6])
		| extend res=array_iff(condition, if_true, if_false)`)
	want := `[1,5,3]`
	if got := result.Rows[0][3]; got != want {
		t.Errorf("res = %v, want %v", got, want)
	}
}

func TestArrayIffNumericConditionScalarBranches(t *testing.T) {
	result := queryResult(t, `print condition=dynamic([1,0,50]), if_true="yes", if_false="no"
		| extend res=array_iff(condition, if_true, if_false)`)
	want := `["yes","no","yes"]`
	if got := result.Rows[0][3]; got != want {
		t.Errorf("res = %v, want %v", got, want)
	}
}

func TestArrayIffNonNumericNonBooleanConditionIsNull(t *testing.T) {
	result := queryResult(t, `print condition=dynamic(["some string value", "2022-01-01T00:00:00Z", null]), if_true=1, if_false=0
		| extend res=array_iff(condition, if_true, if_false)`)
	want := `[null,null,null]`
	if got := result.Rows[0][3]; got != want {
		t.Errorf("res = %v, want %v", got, want)
	}
}

func TestArrayIffMismatchedArrayLengthsFillNull(t *testing.T) {
	result := queryResult(t, `print condition=dynamic([true,true,true]), if_true=dynamic([1,2]), if_false=dynamic([3,4])
		| extend res=array_iff(condition, if_true, if_false)`)
	want := `[1,2,null]`
	if got := result.Rows[0][3]; got != want {
		t.Errorf("res = %v, want %v", got, want)
	}
}

// TestArrayIifAliasEquivalence guards that array_iif() (the
// documented equivalent alias) produces the same result as
// array_iff().
func TestArrayIifAliasEquivalence(t *testing.T) {
	result := queryResult(t, `print a=array_iff(dynamic([true,false]), dynamic([1,2]), dynamic([3,4])),
		b=array_iif(dynamic([true,false]), dynamic([1,2]), dynamic([3,4]))`)
	if result.Rows[0][0] != result.Rows[0][1] {
		t.Errorf("array_iff = %v, array_iif = %v, want equal", result.Rows[0][0], result.Rows[0][1])
	}
}
