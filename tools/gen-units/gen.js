// Generates func_convert_units.go from unitsnet-js: for each ADX unit,
// toBase(x) = a*x + b, sampled at 0, 1 and 2 (linearity checked).
const u = require('unitsnet-js');
const lists = {
  angle: 'Arcminute Arcsecond Centiradian Deciradian Degree Gradian Microdegree Microradian Millidegree Milliradian Nanodegree Nanoradian NatoMil Radian Revolution',
  energy: 'BritishThermalUnit Calorie DecathermEc DecathermImperial DecathermUs ElectronVolt Erg FootPound GigabritishThermalUnit GigaelectronVolt Gigajoule GigawattDay GigawattHour HorsepowerHour Joule KilobritishThermalUnit Kilocalorie KiloelectronVolt Kilojoule KilowattDay KilowattHour MegabritishThermalUnit Megacalorie MegaelectronVolt Megajoule MegawattDay MegawattHour Millijoule TeraelectronVolt TerawattDay TerawattHour ThermEc ThermImperial ThermUs WattDay WattHour',
  force: 'Decanewton Dyn KilogramForce Kilonewton KiloPond KilopoundForce Meganewton Micronewton Millinewton Newton OunceForce Poundal PoundForce ShortTonForce TonneForce',
  length: 'Angstrom AstronomicalUnit Centimeter Chain DataMile Decameter Decimeter DtpPica DtpPoint Fathom Foot Hand Hectometer Inch KilolightYear Kilometer Kiloparsec LightYear MegalightYear Megaparsec Meter Microinch Micrometer Mil Mile Millimeter Nanometer NauticalMile Parsec PrinterPica PrinterPoint Shackle SolarRadius Twip UsSurveyFoot Yard',
  mass: 'Centigram Decagram Decigram EarthMass Grain Gram Hectogram Kilogram Kilopound Kilotonne LongHundredweight LongTon Megapound Megatonne Microgram Milligram Nanogram Ounce Pound ShortHundredweight ShortTon Slug SolarMass Stone Tonne',
  speed: 'CentimeterPerHour CentimeterPerMinute CentimeterPerSecond DecimeterPerMinute DecimeterPerSecond FootPerHour FootPerMinute FootPerSecond InchPerHour InchPerMinute InchPerSecond KilometerPerHour KilometerPerMinute KilometerPerSecond Knot MeterPerHour MeterPerMinute MeterPerSecond MicrometerPerMinute MicrometerPerSecond MilePerHour MillimeterPerHour MillimeterPerMinute MillimeterPerSecond NanometerPerMinute NanometerPerSecond UsSurveyFootPerHour UsSurveyFootPerMinute UsSurveyFootPerSecond YardPerHour YardPerMinute YardPerSecond',
  temperature: 'DegreeCelsius DegreeDelisle DegreeFahrenheit DegreeNewton DegreeRankine DegreeReaumur DegreeRoemer Kelvin MillidegreeCelsius SolarTemperature',
  volume: 'AcreFoot AuTablespoon BoardFoot Centiliter CubicCentimeter CubicDecimeter CubicFoot CubicHectometer CubicInch CubicKilometer CubicMeter CubicMicrometer CubicMile CubicMillimeter CubicYard Decaliter DecausGallon Deciliter DeciusGallon HectocubicFoot HectocubicMeter Hectoliter HectousGallon ImperialBeerBarrel ImperialGallon ImperialOunce ImperialPint KilocubicFoot KilocubicMeter KiloimperialGallon Kiloliter KilousGallon Liter MegacubicFoot MegaimperialGallon Megaliter MegausGallon MetricCup MetricTeaspoon Microliter Milliliter OilBarrel UkTablespoon UsBeerBarrel UsCustomaryCup UsGallon UsLegalCup UsOunce UsPint UsQuart UsTablespoon UsTeaspoon',
};
const cls = {angle:'Angle',energy:'Energy',force:'Force',length:'Length',mass:'Mass',speed:'Speed',temperature:'Temperature',volume:'Volume'};
let out = [], missing = [];
for (const q of Object.keys(lists)) {
  const C = u[cls[q]], E = u[cls[q] + 'Units'];
  const bySingular = {};
  for (const k of Object.keys(E)) bySingular[E[k].toLowerCase()] = E[k];
  const rows = [];
  for (const name of lists[q].split(' ')) {
    if (!(name.toLowerCase() in bySingular)) { missing.push(q + ':' + name); continue; }
    const f = x => new C(x, bySingular[name.toLowerCase()]).value;
    const b = f(0), a = (f(1e9) - b) / 1e9, c = f(2);
    const lin = Math.abs(c - (2*a + b)) <= 1e-9 * Math.max(1, Math.abs(c));
    if (!lin) { missing.push(q + ':' + name + '(nonlinear)'); continue; }
    rows.push(`\t\t"${name.toLowerCase()}": {${a.toPrecision(17)}, ${b.toPrecision(17)}},`);
  }
  out.push(`\t"convert_${q}": {\n${rows.join('\n')}\n\t},`);
}
if (missing.length) { console.error('MISSING ' + missing.join(' ')); process.exit(1); }
console.log(`// Code generated from unitsnet-js 4.0.10 (the UnitsNet definitions
// ADX's convert_* functions use) by sampling each unit's conversion to
// its quantity's base unit: base = a*value + b. Unit lists are the
// ones on the ADX reference pages. DO NOT EDIT by hand.

package engine

// unitAffine converts a value in a unit to the base unit: a*v + b.
type unitAffine struct{ a, b float64 }

var convertUnits = map[string]map[string]unitAffine{
${out.join('\n')}
}`);
