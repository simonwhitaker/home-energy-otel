package main

import (
	"bytes"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/simonwhitaker/home-energy-otel/energy"
)

const (
	elecTopic = "glow/ABCDEF012345/SENSOR/electricitymeter"
	gasTopic  = "glow/ABCDEF012345/SENSOR/gasmeter"
)

func elecPayload(ts string, kw float64) []byte {
	return []byte(`{"electricitymeter":{"timestamp":"` + ts + `","energy":{"import":{"cumulative":6613.405,"price":{"unitrate":null,"standingcharge":null}}},"power":{"value":` +
		strconv.FormatFloat(kw, 'f', -1, 64) + `}}}`)
}

func gasPayload(ts string) []byte {
	return []byte(`{"gasmeter":{"timestamp":"` + ts + `","energy":{"import":{"cumulative":17940.852,"price":{"unitrate":null,"standingcharge":null}}},"power":{"value":0}}}`)
}

type fakeWriter struct {
	written [][]energy.Reading
	err     error
}

func (w *fakeWriter) WriteReadings(r []energy.Reading) error {
	w.written = append(w.written, r)
	return w.err
}

func (w *fakeWriter) Close() error { return nil }

func newTestIngester(writers ...energy.EnergyDataWriter) (*ingester, *bytes.Buffer, *time.Time) {
	var buf bytes.Buffer
	now := time.Date(2026, 11, 26, 12, 0, 0, 0, time.UTC)
	ing := newIngester(log.New(&buf, "", 0), writers, now)
	return ing, &buf, &now
}

func TestIngestWritesParsedReadingsToEveryWriter(t *testing.T) {
	failing := &fakeWriter{err: errors.New("exporter down")}
	working := &fakeWriter{}
	ing, logs, now := newTestIngester(failing, working)

	ing.handle(elecTopic, elecPayload("2026-11-26T12:00:00Z", 0.951), *now)

	if len(working.written) != 1 {
		t.Fatalf("expected the healthy writer to get 1 batch, got %d", len(working.written))
	}
	if len(failing.written) != 1 {
		t.Fatalf("expected the failing writer to still be called")
	}
	if !strings.Contains(logs.String(), "exporter down") {
		t.Fatalf("expected the writer error to be logged, got %q", logs.String())
	}
}

func TestIngestDropsMessagesThatAreNotNewer(t *testing.T) {
	w := &fakeWriter{}
	ing, _, now := newTestIngester(w)

	ing.handle(elecTopic, elecPayload("2026-11-26T12:00:10Z", 1), *now)
	ing.handle(elecTopic, elecPayload("2026-11-26T12:00:10Z", 1), *now) // replayed (e.g. retained)
	ing.handle(elecTopic, elecPayload("2026-11-26T12:00:00Z", 1), *now) // older
	ing.handle(elecTopic, elecPayload("2026-11-26T12:00:20Z", 1), *now) // newer

	if len(w.written) != 2 {
		t.Fatalf("expected 2 batches written, got %d", len(w.written))
	}
}

func TestIngestTracksTimestampsPerMeter(t *testing.T) {
	w := &fakeWriter{}
	ing, _, now := newTestIngester(w)

	ing.handle(elecTopic, elecPayload("2026-11-26T12:00:10Z", 1), *now)
	// Gas is older than the latest electricity message but is the first gas
	// message, so it must still go through.
	ing.handle(gasTopic, gasPayload("2026-11-26T11:30:00Z"), *now)

	if len(w.written) != 2 {
		t.Fatalf("expected 2 batches written, got %d", len(w.written))
	}
}

func TestIngestLogsAndSkipsUnparseableMessages(t *testing.T) {
	w := &fakeWriter{}
	ing, logs, now := newTestIngester(w)

	ing.handle(elecTopic, []byte("{not json"), *now)

	if len(w.written) != 0 {
		t.Fatal("expected nothing written")
	}
	if !strings.Contains(logs.String(), elecTopic) {
		t.Fatalf("expected the topic in the log, got %q", logs.String())
	}
}

func TestSilenceIsLoggedOnceAndResumptionIsLogged(t *testing.T) {
	ing, logs, start := newTestIngester(&fakeWriter{})

	ing.checkSilence(start.Add(silenceThreshold - time.Second))
	if logs.Len() != 0 {
		t.Fatalf("expected no log before the threshold, got %q", logs.String())
	}

	ing.checkSilence(start.Add(silenceThreshold + time.Second))
	ing.checkSilence(start.Add(silenceThreshold + time.Minute))
	if n := strings.Count(logs.String(), "No electricity data"); n != 1 {
		t.Fatalf("expected exactly one silence warning, got %d in %q", n, logs.String())
	}

	ing.handle(elecTopic, elecPayload("2026-11-26T12:10:00Z", 1), start.Add(10*time.Minute))
	if !strings.Contains(logs.String(), "Electricity data resumed after 10m0s") {
		t.Fatalf("expected a resumption log, got %q", logs.String())
	}
}

func TestGasMessagesDoNotCountAsElectricityData(t *testing.T) {
	ing, logs, start := newTestIngester(&fakeWriter{})

	ing.handle(gasTopic, gasPayload("2026-11-26T12:04:00Z"), start.Add(4*time.Minute))
	ing.checkSilence(start.Add(silenceThreshold + time.Second))

	if !strings.Contains(logs.String(), "No electricity data") {
		t.Fatalf("expected a silence warning, got %q", logs.String())
	}
}

func TestReadinessFollowsBrokerConnection(t *testing.T) {
	for _, tc := range []struct {
		connected bool
		want      int
	}{
		{true, http.StatusOK},
		{false, http.StatusServiceUnavailable},
	} {
		h := readinessHandler(func() bool { return tc.connected })
		rec := httptest.NewRecorder()
		h(rec, httptest.NewRequest(http.MethodGet, "/readyz", nil))
		if rec.Code != tc.want {
			t.Errorf("connected=%v: expected %d, got %d", tc.connected, tc.want, rec.Code)
		}
	}
}
