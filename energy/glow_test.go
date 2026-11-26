package energy

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return b
}

func readingsByName(rs []Reading) map[string]float64 {
	m := make(map[string]float64, len(rs))
	for _, r := range rs {
		m[r.Metric.Name] = r.Value
	}
	return m
}

func assertReadings(t *testing.T, got []Reading, want map[string]float64) {
	t.Helper()
	gotByName := readingsByName(got)
	if len(gotByName) != len(want) {
		t.Errorf("expected %d readings, got %d: %v", len(want), len(gotByName), got)
	}
	for name, v := range want {
		actual, ok := gotByName[name]
		if !ok {
			t.Errorf("missing reading %s", name)
			continue
		}
		if diff := actual - v; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("%s: expected %v, got %v", name, v, actual)
		}
	}
}

func TestParseGlowElectricityMessage(t *testing.T) {
	msg, err := ParseGlowMessage("glow/ABCDEF012345/SENSOR/electricitymeter", fixture(t, "electricitymeter.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if want := time.Date(2022, 6, 11, 20, 38, 0, 0, time.UTC); !msg.Timestamp.Equal(want) {
		t.Errorf("expected timestamp %v, got %v", want, msg.Timestamp)
	}
	assertReadings(t, msg.Readings, map[string]float64{
		"energy.live.electricity":            951, // 0.951 kW
		"energy.meter.electricity":           6613.405,
		"energy.unit_rate.electricity":       0.04998,
		"energy.standing_charge.electricity": 0.24030,
	})
}

func TestParseGlowGasMessage(t *testing.T) {
	msg, err := ParseGlowMessage("glow/ABCDEF012345/SENSOR/gasmeter", fixture(t, "gasmeter.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if want := time.Date(2022, 6, 11, 20, 53, 52, 0, time.UTC); !msg.Timestamp.Equal(want) {
		t.Errorf("expected timestamp %v, got %v", want, msg.Timestamp)
	}
	// Gas power is always zero from the Glow display, so it isn't reported.
	assertReadings(t, msg.Readings, map[string]float64{
		"energy.meter.gas":           17940.852,
		"energy.unit_rate.gas":       0.07320,
		"energy.standing_charge.gas": 0.17850,
	})
}

func TestParseGlowStateMessage(t *testing.T) {
	msg, err := ParseGlowMessage("glow/ABCDEF012345/STATE", fixture(t, "state.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if want := time.Date(2022, 6, 11, 20, 54, 53, 0, time.UTC); !msg.Timestamp.Equal(want) {
		t.Errorf("expected timestamp %v, got %v", want, msg.Timestamp)
	}
	assertReadings(t, msg.Readings, map[string]float64{
		"glow.signal.rssi": -75,
		"glow.signal.lqi":  100,
	})
}

func TestParseGlowMessageSkipsNullPrices(t *testing.T) {
	msg, err := ParseGlowMessage("glow/ABCDEF012345/SENSOR/electricitymeter", fixture(t, "electricitymeter_null_prices.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertReadings(t, msg.Readings, map[string]float64{
		"energy.live.electricity":  1200,
		"energy.meter.electricity": 6613.410,
	})
}

func TestParseGlowMessageReportsTheMeter(t *testing.T) {
	cases := map[string]GlowMeter{
		"glow/ABCDEF012345/SENSOR/electricitymeter": GlowElectricity,
		"glow/ABCDEF012345/SENSOR/gasmeter":         GlowGas,
		"glow/ABCDEF012345/STATE":                   GlowState,
	}
	payloads := map[GlowMeter]string{
		GlowElectricity: "electricitymeter.json",
		GlowGas:         "gasmeter.json",
		GlowState:       "state.json",
	}
	for topic, want := range cases {
		msg, err := ParseGlowMessage(topic, fixture(t, payloads[want]))
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", topic, err)
		}
		if msg.Meter != want {
			t.Errorf("%s: expected meter %q, got %q", topic, want, msg.Meter)
		}
		if msg.Device != "ABCDEF012345" {
			t.Errorf("%s: expected device ABCDEF012345, got %q", topic, msg.Device)
		}
	}
}

func TestParseGlowMessageRejectsBadInput(t *testing.T) {
	cases := []struct {
		name, topic, payload string
	}{
		{"unknown topic", "glow/ABCDEF012345/SENSOR/watermeter", `{}`},
		{"short topic", "glow/STATE", `{}`},
		{"invalid json", "glow/ABCDEF012345/STATE", `{not json`},
		{"missing meter object", "glow/ABCDEF012345/SENSOR/electricitymeter", `{"gasmeter":{}}`},
		{"missing timestamp", "glow/ABCDEF012345/STATE", `{"han":{"rssi":-75,"lqi":100}}`},
		{"bad timestamp", "glow/ABCDEF012345/STATE", `{"timestamp":"yesterday","han":{"rssi":-75,"lqi":100}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ParseGlowMessage(tc.topic, []byte(tc.payload)); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestParseGlowMessageHonoursCustomTopicPrefix(t *testing.T) {
	msg, err := ParseGlowMessage("home/glow/ABCDEF012345/STATE", fixture(t, "state.json"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if msg.Meter != GlowState || msg.Device != "ABCDEF012345" {
		t.Fatalf("unexpected meter/device: %q/%q", msg.Meter, msg.Device)
	}
}
