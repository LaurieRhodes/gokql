package engine

import (
	"strings"
	"testing"
)

// plugin_ip_lookup_test.go — ipv4_lookup / ipv6_lookup against the
// reference examples (external_data replaced by an equivalent
// datatable), plus `as hint.materialized=true`.

func ipLookupRows(t *testing.T, q string) []string {
	t.Helper()
	r := queryResult(t, q)
	var out []string
	cols := make([]string, len(r.Schema.Columns))
	for i, c := range r.Schema.Columns {
		cols[i] = c.Name
	}
	out = append(out, strings.Join(cols, ","))
	for i := range r.Rows {
		cells := make([]string, len(r.Schema.Columns))
		for j := range cells {
			cells[j] = formatCell(r, i, j)
		}
		out = append(out, strings.Join(cells, ","))
	}
	return out
}

func assertCSVRows(t *testing.T, got, want []string) {
	t.Helper()
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

const ipv4LookupData = `let IP_Data = datatable(network:string,geoname_id:long,continent_code:string,continent_name:string,country_iso_code:string,country_name:string,is_anonymous_proxy:bool,is_satellite_provider:bool)[
	'2.20.183.0/24',2635167,'EU','Europe','GB','United Kingdom',false,false,
	'5.8.0.0/19',2017370,'EU','Europe','RU','Russia',false,false,
	'192.165.8.0/21',2661886,'EU','Europe','SE','Sweden',false,false];
`

func TestIPv4LookupReference(t *testing.T) {
	q := ipv4LookupData + `let IPs = datatable(ip:string)['2.20.183.12','5.8.1.2','192.165.12.17'];
IPs | evaluate ipv4_lookup(IP_Data, ip, network) | project ip, network, continent_code, continent_name, country_iso_code, country_name`
	assertCSVRows(t, ipLookupRows(t, q), []string{
		"ip,network,continent_code,continent_name,country_iso_code,country_name",
		"2.20.183.12,2.20.183.0/24,EU,Europe,GB,United Kingdom",
		"5.8.1.2,5.8.0.0/19,EU,Europe,RU,Russia",
		"192.165.12.17,192.165.8.0/21,EU,Europe,SE,Sweden",
	})
}

func TestIPv4LookupReturnUnmatched(t *testing.T) {
	q := ipv4LookupData + `let IPs = datatable(ip:string)['2.20.183.12','5.8.1.2','10.0.0.1'];
IPs | evaluate ipv4_lookup(IP_Data, ip, network, return_unmatched = true) | project ip, network, country_name`
	assertCSVRows(t, ipLookupRows(t, q), []string{
		"ip,network,country_name",
		"2.20.183.12,2.20.183.0/24,United Kingdom",
		"5.8.1.2,5.8.0.0/19,Russia",
		"10.0.0.1,,",
	})
	// positional bool form, and default drops unmatched
	q2 := ipv4LookupData + `datatable(ip:string)['10.0.0.1'] | evaluate ipv4_lookup(IP_Data, ip, network, true) | count`
	if got := formatCell(queryResult(t, q2), 0, 0); got != "1" {
		t.Errorf("positional return_unmatched count = %s", got)
	}
	q3 := ipv4LookupData + `datatable(ip:string)['10.0.0.1'] | evaluate ipv4_lookup(IP_Data, ip, network) | count`
	if got := formatCell(queryResult(t, q3), 0, 0); got != "0" {
		t.Errorf("default unmatched count = %s", got)
	}
}

func TestIPv4LookupExtraKeys(t *testing.T) {
	q := ipv4LookupData + `let IPs = datatable(ip:string, continent_name:string, country_iso_code:string)[
	'2.20.183.12','Europe','GB', '5.8.1.2','Europe','RU', '192.165.12.17','Europe',''];
IPs | evaluate ipv4_lookup(IP_Data, ip, network, continent_name, country_iso_code)`
	assertCSVRows(t, ipLookupRows(t, q), []string{
		"ip,continent_name,country_iso_code,network,geoname_id,continent_code,country_name,is_anonymous_proxy,is_satellite_provider",
		"2.20.183.12,Europe,GB,2.20.183.0/24,2635167,EU,United Kingdom,false,false",
		"5.8.1.2,Europe,RU,5.8.0.0/19,2017370,EU,Russia,false,false",
	})
}

func TestIPv6LookupAllMatches(t *testing.T) {
	q := `let L = datatable(net:string, tag:string)['fe80::/10','linklocal', '2001:db8::/32','doc', '2001:db8:1::/48','doc-sub'];
datatable(ip:string)['fe80::1','2001:db8:1::5','::1'] | evaluate ipv6_lookup(L, ip, net, true)`
	assertCSVRows(t, ipLookupRows(t, q), []string{
		"ip,net,tag",
		"fe80::1,fe80::/10,linklocal",
		"2001:db8:1::5,2001:db8::/32,doc",
		"2001:db8:1::5,2001:db8:1::/48,doc-sub",
		"::1,,",
	})
}

func TestAsHintMaterialized(t *testing.T) {
	r := queryResult(t, `range x from 1 to 3 step 1 | as hint.materialized=true T | summarize s = sum(x)`)
	if got := formatCell(r, 0, 0); got != "6" {
		t.Errorf("sum = %s", got)
	}
}
