package engine

import (
	"math"
	"testing"
)

// convert_units_test.go — the eight convert_* reference examples
// (to the 15 significant digits ADX displays), invalid units, Tilt.
func TestConvertUnitsReference(t *testing.T) {
	cases := []struct {
		expr string
		want float64
	}{
		{`convert_length(1.2, 'Meter', 'Foot')`, 3.93700787401575},
		{`convert_temperature(1.2, 'Kelvin', 'DegreeCelsius')`, -271.95},
		{`convert_angle(1.2, 'Degree', 'Arcminute')`, 72},
		{`convert_energy(1.2, 'Joule', 'BritishThermalUnit')`, 0.00113738054437598},
		{`convert_force(1.2, 'Newton', 'Decanewton')`, 0.12},
		{`convert_mass(1.2, 'Kilogram', 'Pound')`, 2.64554714621853},
		{`convert_speed(1.2, 'MeterPerSecond', 'CentimeterPerHour')`, 432000},
		{`convert_volume(1.2, 'CubicMeter', 'AcreFoot')`, 0.0009728568},
		{`convert_temperature(100, 'DegreeCelsius', 'DegreeFahrenheit')`, 212},
		{`convert_angle(0.5, 'Tilt', 'Degree')`, 30},
		{`convert_force(1, 'KiloPond', 'Newton')`, 9.80665},
	}
	for _, c := range cases {
		r := queryResult(t, `print x = `+c.expr)
		got, ok := r.Rows[0][0].(float64)
		if !ok || math.Abs(got-c.want) > 1e-14*math.Max(1, math.Abs(c.want)) && math.Abs(got-c.want)/math.Abs(c.want) > 1e-14 {
			t.Errorf("%s = %v, want %v", c.expr, r.Rows[0][0], c.want)
		}
	}
	r := queryResult(t, `print x = convert_length(1, 'Meter', 'Bogus')`)
	if r.Rows[0][0] != nil {
		t.Errorf("invalid unit = %v, want null", r.Rows[0][0])
	}
}
