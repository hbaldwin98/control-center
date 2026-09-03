package tid

import (
	"testing"
)

func TestParseCSV(t *testing.T) {
	in := "Date,Usage (kWh),Cost\n2026-08-01,12.5,$3.21\n08/02/2026,10,2.50\n"
	got := Parse([]byte(in), "text/csv")
	if len(got) != 2 {
		t.Fatalf("len %d", len(got))
	}
	if got[0].Day != "2026-08-01" || got[0].KWh != 12.5 || got[0].CostCents == nil || *got[0].CostCents != 321 {
		t.Fatalf("row0 %+v", got[0])
	}
	if got[1].Day != "2026-08-02" || got[1].KWh != 10 {
		t.Fatalf("row1 %+v", got[1])
	}
}

func TestParseJSON(t *testing.T) {
	in := `{"readings":[{"date":"2026-08-01","kwh":12.5,"cost":3.21},{"day":"2026-08-01","kWh":2.5,"cost_cents":40}]}`
	got := Parse([]byte(in), "application/json")
	if len(got) != 1 {
		t.Fatalf("merged len %d %#v", len(got), got)
	}
	if got[0].KWh != 15 {
		t.Fatalf("sum kwh %v", got[0].KWh)
	}
	if got[0].CostCents == nil || *got[0].CostCents != 361 {
		t.Fatalf("sum cents %v", got[0].CostCents)
	}
}

func TestParseHTMLTable(t *testing.T) {
	in := `<table class="usage"><tr><th>Date</th><th>kWh</th></tr>
<tr><td>2026-08-01</td><td>12.5</td></tr>
<tr><td>2026-08-02</td><td>8</td></tr></table>`
	got := Parse([]byte(in), "text/html")
	if len(got) != 2 || got[0].KWh != 12.5 || got[1].Day != "2026-08-02" {
		t.Fatalf("%+v", got)
	}
}

func TestParseOriginCXUsageList(t *testing.T) {
	in := `{"status":"OK","data":{"usageList":[
		{"periodStartDate":"2026-08-01T00:00:00","usage":12.5,"dailyCost":3.21},
		{"costDate":"2026-08-02","usage":"8","amount":2.1}
	]}}`
	got := Parse([]byte(in), "application/json")
	if len(got) != 2 {
		t.Fatalf("len %d %#v", len(got), got)
	}
	if got[0].Day != "2026-08-01" || got[0].KWh != 12.5 || got[0].CostCents == nil || *got[0].CostCents != 321 {
		t.Fatalf("row0 %+v", got[0])
	}
	if got[1].Day != "2026-08-02" || got[1].KWh != 8 {
		t.Fatalf("row1 %+v", got[1])
	}
}

func TestParseEmpty(t *testing.T) {
	if got := Parse(nil, "text/csv"); got != nil {
		t.Fatalf("%v", got)
	}
}

func TestParseJSONReadsTemperature(t *testing.T) {
	in := `{"data":{"usageList":[
		{"costDate":"2026-08-10","usage":"44.4","dailyCost":4.91,"highTemperature":101,"lowTemperature":"68"},
		{"costDate":"2026-08-11","usage":"30.0","temperature":72},
		{"costDate":"2026-08-12","usage":"12.0"}
	]}}`
	got := Parse([]byte(in), "application/json")
	if len(got) != 3 {
		t.Fatalf("want 3 readings, got %d", len(got))
	}
	if got[0].HighTempF == nil || *got[0].HighTempF != 101 {
		t.Fatalf("high temp: %v", got[0].HighTempF)
	}
	if got[0].LowTempF == nil || *got[0].LowTempF != 68 {
		t.Fatalf("low temp: %v", got[0].LowTempF)
	}
	if got[0].AvgTempF == nil || *got[0].AvgTempF != 84.5 {
		t.Fatalf("avg temp should fall back to the midpoint: %v", got[0].AvgTempF)
	}
	if got[1].AvgTempF == nil || *got[1].AvgTempF != 72 {
		t.Fatalf("bare temperature key: %v", got[1].AvgTempF)
	}
	if got[2].HighTempF != nil || got[2].AvgTempF != nil {
		t.Fatalf("day without weather should carry no temperature")
	}
}
