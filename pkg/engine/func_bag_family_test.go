package engine

import "testing"

// func_bag_family_test.go — bag_zip, bag_set_key, bag_pack_columns,
// dynamic_to_json, set_has_element, added 2026-08-18. Every case
// below is taken directly from real ADX's own docs' worked examples.

// --- bag_zip ---

func TestBagZipEqualLength(t *testing.T) {
	result := queryResult(t, `let Data = datatable(KeysArray: dynamic, ValuesArray: dynamic)
		[dynamic(["a","b","c"]), dynamic([1,"2",3.4])];
		Data | extend NewBag = bag_zip(KeysArray, ValuesArray)`)
	want := `{"a":1,"b":"2","c":3.4}`
	if got := result.Rows[0][2]; got != want {
		t.Errorf("NewBag = %v, want %v", got, want)
	}
}

func TestBagZipMoreKeysThanValues(t *testing.T) {
	result := queryResult(t, `let Data = datatable(KeysArray: dynamic, ValuesArray: dynamic)
		[dynamic(["a","b","c"]), dynamic([1,"2"])];
		Data | extend NewBag = bag_zip(KeysArray, ValuesArray)`)
	want := `{"a":1,"b":"2","c":null}`
	if got := result.Rows[0][2]; got != want {
		t.Errorf("NewBag = %v, want %v", got, want)
	}
}

func TestBagZipMoreValuesThanKeys(t *testing.T) {
	result := queryResult(t, `let Data = datatable(KeysArray: dynamic, ValuesArray: dynamic)
		[dynamic(["a","b"]), dynamic([1,"2",2.5])];
		Data | extend NewBag = bag_zip(KeysArray, ValuesArray)`)
	want := `{"a":1,"b":"2"}`
	if got := result.Rows[0][2]; got != want {
		t.Errorf("NewBag = %v, want %v", got, want)
	}
}

func TestBagZipNonStringKeyExcluded(t *testing.T) {
	result := queryResult(t, `let Data = datatable(KeysArray: dynamic, ValuesArray: dynamic)
		[dynamic(["a",8,"b"]), dynamic([1,"2",2.5])];
		Data | extend NewBag = bag_zip(KeysArray, ValuesArray)`)
	want := `{"a":1,"b":2.5}`
	if got := result.Rows[0][2]; got != want {
		t.Errorf("NewBag = %v, want %v", got, want)
	}
}

func TestBagZipValuesArrayNotAnArray(t *testing.T) {
	result := queryResult(t, `let Data = datatable(KeysArray: dynamic, ValuesArray: dynamic)
		[dynamic(["a",8,"b"]), dynamic(1)];
		Data | extend NewBag = bag_zip(KeysArray, ValuesArray)`)
	want := `{"a":null,"b":null}`
	if got := result.Rows[0][2]; got != want {
		t.Errorf("NewBag = %v, want %v", got, want)
	}
}

func TestBagZipKeysArrayNotAnArrayReturnsNull(t *testing.T) {
	result := queryResult(t, `let Data = datatable(KeysArray: dynamic, ValuesArray: dynamic)
		[dynamic("a"), dynamic([1,"2",2.5])];
		Data | extend NewBag = bag_zip(KeysArray, ValuesArray) | extend IsNewBagEmpty=isnull(NewBag)`)
	if result.Rows[0][3] != true {
		t.Errorf("IsNewBagEmpty = %v, want true", result.Rows[0][3])
	}
}

// --- bag_set_key ---

func TestBagSetKeyRootLevel(t *testing.T) {
	result := queryResult(t, `datatable(input: dynamic) [
		dynamic({'key1': 1, 'key2': 2}),
		dynamic({'key1': 1, 'key3': 'abc'})]
		| extend result = bag_set_key(input, 'key3', 3)`)
	if len(result.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(result.Rows))
	}
	want0 := `{"key1":1,"key2":2,"key3":3}`
	want1 := `{"key1":1,"key3":3}`
	if got := result.Rows[0][1]; got != want0 {
		t.Errorf("row 0 result = %v, want %v", got, want0)
	}
	if got := result.Rows[1][1]; got != want1 {
		t.Errorf("row 1 result = %v, want %v", got, want1)
	}
}

// TestBagSetKeyJSONPathAutoVivifies guards real ADX's own second
// worked example: setting a nested JSONPath key auto-creates any
// missing intermediate object along the way (row 2's input has no
// "key2" at all, yet the result creates {"key2": {"prop1": "abc"}}).
func TestBagSetKeyJSONPathAutoVivifies(t *testing.T) {
	result := queryResult(t, `datatable(input: dynamic)[
		dynamic({'key1': 123, 'key2': {'prop1': 123, 'prop2': 'xyz'}}),
		dynamic({'key1': 123})]
		| extend result = bag_set_key(input, '$.key2.prop1', 'abc')`)
	if len(result.Rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(result.Rows))
	}
	want0 := `{"key1":123,"key2":{"prop1":"abc","prop2":"xyz"}}`
	want1 := `{"key1":123,"key2":{"prop1":"abc"}}`
	if got := result.Rows[0][1]; got != want0 {
		t.Errorf("row 0 result = %v, want %v", got, want0)
	}
	if got := result.Rows[1][1]; got != want1 {
		t.Errorf("row 1 (auto-vivified key2) result = %v, want %v", got, want1)
	}
}

// TestBagSetKeyNonBagReturnsNull guards real ADX's own documented
// "if the input bag isn't a property-bag, a null value is returned."
func TestBagSetKeyNonBagReturnsNull(t *testing.T) {
	result := queryResult(t, `print r = bag_set_key(dynamic([1,2,3]), 'key', 'value')`)
	if result.Rows[0][0] != nil {
		t.Errorf("r = %v, want null", result.Rows[0][0])
	}
}

// --- bag_pack_columns ---

func TestBagPackColumnsWorkedExample(t *testing.T) {
	result := queryResult(t, `datatable(Id: string, Value: string, Other: long)
		["A", "val_a", 1, "B", "val_b", 2, "C", "val_c", 3]
		| extend Packed = bag_pack_columns(Id, Value)`)
	if len(result.Rows) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(result.Rows))
	}
	want := []string{
		`{"Id":"A","Value":"val_a"}`,
		`{"Id":"B","Value":"val_b"}`,
		`{"Id":"C","Value":"val_c"}`,
	}
	for i, w := range want {
		if got := result.Rows[i][3]; got != w {
			t.Errorf("row %d Packed = %v, want %v", i, got, w)
		}
	}
}

// TestBagPackColumnsRejectsNonColumnArgument guards that a bare
// expression (which has no column NAME to use as a property key)
// produces a clear error rather than silently guessing a key name.
func TestBagPackColumnsRejectsNonColumnArgument(t *testing.T) {
	queryError(t, `datatable(Id: string)["A"] | extend Packed = bag_pack_columns(strcat(Id, "!"))`)
}

// --- dynamic_to_json ---

// TestDynamicToJsonSortsKeysCanonically guards real ADX's own worked
// example: two bags built with different key insertion orders but
// the same content produce byte-for-byte-equal canonical strings,
// because dynamic_to_json sorts property-bag keys alphabetically.
func TestDynamicToJsonSortsKeysCanonically(t *testing.T) {
	result := queryResult(t, `let bag1 = dynamic_to_json(dynamic({'Y10':{},'X8':{'c3':1,'d8':5,'a4':6},'D1':114,'A1':12,'B1':2,'C1':3,'A14':[15,13,18]}));
		let bag2 = dynamic_to_json(dynamic({'X8':{'a4':6,'c3':1,'d8':5},'A14':[15,13,18],'C1':3,'B1':2,'Y10':{},'A1':12,'D1':114}));
		print AreEqual=bag1 == bag2, Result=bag1`)
	if result.Rows[0][0] != true {
		t.Errorf("AreEqual = %v, want true", result.Rows[0][0])
	}
	want := `{"A1":12,"A14":[15,13,18],"B1":2,"C1":3,"D1":114,"X8":{"a4":6,"c3":1,"d8":5},"Y10":{}}`
	if got := result.Rows[0][1]; got != want {
		t.Errorf("Result = %v, want %v", got, want)
	}
}

// TestDynamicToJsonNonDynamicScalar guards real ADX's own documented
// first rule: a non-dynamic scalar input goes through tostring().
func TestDynamicToJsonNonDynamicScalar(t *testing.T) {
	result := queryResult(t, `print r = dynamic_to_json(42)`)
	if got := result.Rows[0][0]; got != "42" {
		t.Errorf("r = %v, want \"42\"", got)
	}
}

// --- set_has_element ---

func TestSetHasElementFound(t *testing.T) {
	result := queryResult(t, `print r = set_has_element(dynamic([1,2,3]), 2)`)
	if result.Rows[0][0] != true {
		t.Errorf("r = %v, want true", result.Rows[0][0])
	}
}

func TestSetHasElementNotFound(t *testing.T) {
	result := queryResult(t, `print r = set_has_element(dynamic([1,2,3]), 5)`)
	if result.Rows[0][0] != false {
		t.Errorf("r = %v, want false", result.Rows[0][0])
	}
}

func TestSetHasElementStringMatch(t *testing.T) {
	result := queryResult(t, `print r = set_has_element(dynamic(["a","b"]), "b")`)
	if result.Rows[0][0] != true {
		t.Errorf("r = %v, want true", result.Rows[0][0])
	}
}
