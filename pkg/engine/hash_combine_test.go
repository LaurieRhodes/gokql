package engine

import "testing"

// Expected values from the Kusto emulator (engine 2025.07.06) and the
// hash_combine / hash_many reference examples.
func TestHashCombineAndMany(t *testing.T) {
	cases := []struct{ q, want string }{
		{`print hash_combine(hash("Hello"), hash("World"))`, "-1440138333540407281"},
		{`print hash_many("Hello", "World")`, "-1440138333540407281"},
		{`print hash_combine(0, 0)`, "0"},
		{`print hash_combine(0, 1)`, "9149743492299042081"},
		{`print hash_combine(1, 0)`, "3835341199769752896"},
		{`print hash_combine(1, 0, 0)`, "-2395191394221995529"},
		{`print hash_combine(0, 0, 0, 1)`, "9149743492299042081"},
		{`print hash_combine(-1, 0)`, "-6875344816673646571"},
		{`print hash_many("a", "b", "c")`, "-5134047267881961796"},
		{`print hash_many(1, 2)`, "-1694463872847316647"},
		{`print hash_many(1.5, datetime(2020-01-01), true)`, "-2622814692848377411"},
		{`print hash_many("x", int(null))`, "348436909733910324"},
		{`print hash_many("a")`, "-3292477735350538661"},
		{`print hash_many("", "")`, "1212311696082004790"},
	}
	for _, c := range cases {
		tbl := queryResult(t, c.q)
		expectCell(t, tbl, 0, 0, c.want)
	}
}

// hash() encodings by type, from the Kusto emulator.
func TestHashTypeEncodings(t *testing.T) {
	cases := []struct{ q, want string }{
		{`print hash(int(1))`, "-851299076295404719"},
		{`print hash(int(0))`, "4246796580750024372"},
		{`print hash(int(-1))`, "9185342943168159635"},
		{`print hash(true)`, "-8484456787529832656"},
		{`print hash(false)`, "-1642502924627794072"},
		{`print hash("")`, "8393271554155345163"},
		{`print hash(long(null))`, "8393271554155345163"},
		{`print hash(datetime(null))`, "8393271554155345163"},
		{`print hash(0)`, "3803688792395291579"},
		{`print hash(real(0))`, "3803688792395291579"},
		{`print hash(1.5)`, "5329932555030153977"},
		{`print hash(1s)`, "-8965002541506690436"},
		{`print hash(1s, 100)`, "80"},
		{`print hash(int(1), 100)`, "97"},
		{`print hash("", 100)`, "63"},
		{`print hash(guid(01020304-0506-0708-090a-0b0c0d0e0f10))`, "-8156725653505464669"},
		{`print hash(guid(00000000-0000-0000-0000-000000000001))`, "-1018491013694090041"},
		{`print hash(datetime(2020-01-01))`, "2979178823058244978"},
	}
	for _, c := range cases {
		tbl := queryResult(t, c.q)
		expectCell(t, tbl, 0, 0, c.want)
	}
}
