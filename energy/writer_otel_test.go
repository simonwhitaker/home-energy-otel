package energy

import (
	"context"
	"errors"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

type exportedPoint struct {
	Unit  string
	Value float64
}

// fakeExporter records what each Export call carried.
type fakeExporter struct {
	exports []map[string]exportedPoint
	err     error
}

func (e *fakeExporter) Temporality(k sdkmetric.InstrumentKind) metricdata.Temporality {
	return gaugeTemporality(k)
}

func (e *fakeExporter) Aggregation(k sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(k)
}

func (e *fakeExporter) Export(_ context.Context, rm *metricdata.ResourceMetrics) error {
	points := map[string]exportedPoint{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			gauge, ok := m.Data.(metricdata.Gauge[float64])
			if !ok {
				continue
			}
			for _, dp := range gauge.DataPoints {
				points[m.Name] = exportedPoint{Unit: m.Unit, Value: dp.Value}
			}
		}
	}
	e.exports = append(e.exports, points)
	return e.err
}

func (e *fakeExporter) ForceFlush(context.Context) error { return nil }
func (e *fakeExporter) Shutdown(context.Context) error   { return nil }

func newTestOTelWriter(t *testing.T, exp *fakeExporter) *OTelWriter {
	t.Helper()
	w, err := newOTelWriter(context.Background(), exp, "test-host", nil)
	if err != nil {
		t.Fatalf("creating OTelWriter: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func TestWriteReadingsExportsImmediatelyWithUnits(t *testing.T) {
	exp := &fakeExporter{}
	w := newTestOTelWriter(t, exp)

	err := w.WriteReadings([]Reading{
		{Metric: LivePowerElectricity, Value: 951},
		{Metric: MeterElectricity, Value: 6613.405},
	})
	if err != nil {
		t.Fatalf("WriteReadings failed: %v", err)
	}

	if len(exp.exports) != 1 {
		t.Fatalf("expected 1 export, got %d", len(exp.exports))
	}
	want := map[string]exportedPoint{
		"energy.live.electricity":  {Unit: "W", Value: 951},
		"energy.meter.electricity": {Unit: "kW.h", Value: 6613.405},
	}
	got := exp.exports[0]
	if len(got) != len(want) {
		t.Fatalf("expected %d points, got %v", len(want), got)
	}
	for name, w := range want {
		if got[name] != w {
			t.Errorf("%s: expected %+v, got %+v", name, w, got[name])
		}
	}
}

func TestEveryWriteIsExportedSoNoSampleIsLost(t *testing.T) {
	exp := &fakeExporter{}
	w := newTestOTelWriter(t, exp)

	for _, v := range []float64{100, 200} {
		if err := w.WriteReadings([]Reading{{Metric: LivePowerElectricity, Value: v}}); err != nil {
			t.Fatalf("WriteReadings failed: %v", err)
		}
	}

	if len(exp.exports) != 2 {
		t.Fatalf("expected 2 exports, got %d", len(exp.exports))
	}
	if v := exp.exports[0]["energy.live.electricity"].Value; v != 100 {
		t.Errorf("first export: expected 100, got %v", v)
	}
	if v := exp.exports[1]["energy.live.electricity"].Value; v != 200 {
		t.Errorf("second export: expected 200, got %v", v)
	}
}

func TestWritesDoNotRepeatEarlierValues(t *testing.T) {
	exp := &fakeExporter{}
	w := newTestOTelWriter(t, exp)

	if err := w.WriteReadings([]Reading{{Metric: MeterGas, Value: 17940.852}}); err != nil {
		t.Fatalf("WriteReadings failed: %v", err)
	}
	if err := w.WriteReadings([]Reading{{Metric: LivePowerElectricity, Value: 951}}); err != nil {
		t.Fatalf("WriteReadings failed: %v", err)
	}

	second := exp.exports[1]
	if _, ok := second["energy.meter.gas"]; ok {
		t.Fatal("gas reading from the first write was exported again")
	}
	if len(second) != 1 {
		t.Fatalf("expected only the new reading, got %v", second)
	}
}

func TestWriteReadingsReturnsExportErrors(t *testing.T) {
	exp := &fakeExporter{err: errors.New("collector down")}
	w := newTestOTelWriter(t, exp)

	if err := w.WriteReadings([]Reading{{Metric: LivePowerElectricity, Value: 951}}); err == nil {
		t.Fatal("expected the export error to be returned")
	}
}

func TestWriteReadingsWithNothingToWriteDoesNotExport(t *testing.T) {
	exp := &fakeExporter{}
	w := newTestOTelWriter(t, exp)

	if err := w.WriteReadings(nil); err != nil {
		t.Fatalf("WriteReadings failed: %v", err)
	}
	if len(exp.exports) != 0 {
		t.Fatalf("expected no export, got %d", len(exp.exports))
	}
}
