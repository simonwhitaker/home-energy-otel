package energy

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

// exportTimeout bounds each export, so a stalled collector can't hold up the
// MQTT subscription that calls WriteReadings.
const exportTimeout = 5 * time.Second

// OTelWriter exports readings over OTLP as they arrive. It uses a manual
// reader, not a periodic one: every WriteReadings call collects and exports
// straight away, so each upstream message becomes one export and nothing is
// sent on a timer.
type OTelWriter struct {
	logger        *log.Logger
	meterProvider *sdkmetric.MeterProvider
	reader        *sdkmetric.ManualReader
	exporter      sdkmetric.Exporter
	gauges        map[string]metric.Float64Gauge

	mu sync.Mutex // serialises record+collect+export
}

func NewOTelWriter(ctx context.Context, hostname string, logger *log.Logger) (*OTelWriter, error) {
	exporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithTemporalitySelector(gaugeTemporality))
	if err != nil {
		return nil, fmt.Errorf("creating OTLP exporter: %w", err)
	}
	return newOTelWriter(ctx, exporter, hostname, logger)
}

// gaugeTemporality reports gauges with delta temporality. Under the SDK's
// default cumulative temporality a gauge re-exports its last recorded value on
// every collection, forever, so each export would repeat values from earlier
// messages instead of carrying only the new ones.
func gaugeTemporality(kind sdkmetric.InstrumentKind) metricdata.Temporality {
	if kind == sdkmetric.InstrumentKindGauge {
		return metricdata.DeltaTemporality
	}
	return sdkmetric.DefaultTemporalitySelector(kind)
}

func newOTelWriter(ctx context.Context, exporter sdkmetric.Exporter, hostname string, logger *log.Logger) (*OTelWriter, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.HostName(hostname)),
	)
	if err != nil {
		return nil, fmt.Errorf("creating resource: %w", err)
	}

	// Take temporality and aggregation from the exporter, as a periodic
	// reader would, so they're configured in one place.
	reader := sdkmetric.NewManualReader(
		sdkmetric.WithTemporalitySelector(exporter.Temporality),
		sdkmetric.WithAggregationSelector(exporter.Aggregation),
	)

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(res),
	)

	meter := provider.Meter("home-energy-otel")

	gauges := make(map[string]metric.Float64Gauge, len(AllMetrics))
	for _, m := range AllMetrics {
		g, err := meter.Float64Gauge(m.Name, metric.WithUnit(m.Unit), metric.WithDescription(m.Description))
		if err != nil {
			return nil, fmt.Errorf("creating gauge %s: %w", m.Name, err)
		}
		gauges[m.Name] = g
	}

	return &OTelWriter{
		logger:        logger,
		meterProvider: provider,
		reader:        reader,
		exporter:      exporter,
		gauges:        gauges,
	}, nil
}

func (w *OTelWriter) WriteReadings(readings []Reading) error {
	if len(readings) == 0 {
		return nil
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), exportTimeout)
	defer cancel()

	for _, r := range readings {
		gauge, ok := w.gauges[r.Metric.Name]
		if !ok {
			if w.logger != nil {
				w.logger.Printf("Unknown metric: %s", r.Metric.Name)
			}
			continue
		}
		gauge.Record(ctx, r.Value)
	}

	var rm metricdata.ResourceMetrics
	if err := w.reader.Collect(ctx, &rm); err != nil {
		return fmt.Errorf("collecting metrics: %w", err)
	}
	if err := w.exporter.Export(ctx, &rm); err != nil {
		return fmt.Errorf("exporting metrics: %w", err)
	}
	return nil
}

func (w *OTelWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// The provider shuts down the reader; the reader doesn't own the
	// exporter, so shut that down too.
	return errors.Join(w.meterProvider.Shutdown(ctx), w.exporter.Shutdown(ctx))
}
