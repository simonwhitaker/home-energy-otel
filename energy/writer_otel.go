package energy

import (
	"context"
	"fmt"
	"log"
	"time"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
)

type OTelWriter struct {
	logger        *log.Logger
	meterProvider *sdkmetric.MeterProvider
	gauges        map[string]metric.Float64Gauge
}

func NewOTelWriter(ctx context.Context, hostname string, logger *log.Logger) (*OTelWriter, error) {
	exporter, err := otlpmetricgrpc.New(ctx, otlpmetricgrpc.WithTemporalitySelector(gaugeTemporality))
	if err != nil {
		return nil, fmt.Errorf("creating OTLP exporter: %w", err)
	}

	reader := sdkmetric.NewPeriodicReader(exporter, sdkmetric.WithInterval(10*time.Second))
	return newOTelWriter(ctx, reader, hostname, logger)
}

// gaugeTemporality reports gauges with delta temporality. Under the SDK's
// default cumulative temporality a gauge re-exports its last recorded value on
// every collection, forever, so an upstream outage shows up downstream as a
// flat line of stale readings instead of a gap.
func gaugeTemporality(kind sdkmetric.InstrumentKind) metricdata.Temporality {
	if kind == sdkmetric.InstrumentKindGauge {
		return metricdata.DeltaTemporality
	}
	return sdkmetric.DefaultTemporalitySelector(kind)
}

func newOTelWriter(ctx context.Context, reader sdkmetric.Reader, hostname string, logger *log.Logger) (*OTelWriter, error) {
	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.HostName(hostname)),
	)
	if err != nil {
		return nil, fmt.Errorf("creating resource: %w", err)
	}

	provider := sdkmetric.NewMeterProvider(
		sdkmetric.WithReader(reader),
		sdkmetric.WithResource(res),
	)

	meter := provider.Meter("geo-energy")

	metricNames := []string{
		"energy.live.electricity",
		"energy.live.gas",
		"energy.meter.electricity",
		"energy.meter.gas",
	}

	gauges := make(map[string]metric.Float64Gauge, len(metricNames))
	for _, name := range metricNames {
		g, err := meter.Float64Gauge(name)
		if err != nil {
			return nil, fmt.Errorf("creating gauge %s: %w", name, err)
		}
		gauges[name] = g
	}

	return &OTelWriter{
		logger:        logger,
		meterProvider: provider,
		gauges:        gauges,
	}, nil
}

func (w *OTelWriter) WriteReadings(readings []Reading) error {
	ctx := context.Background()
	for _, r := range readings {
		name := fmt.Sprintf("energy.%v.%v", r.ReadingType, r.Commodity)
		gauge, ok := w.gauges[name]
		if !ok {
			if w.logger != nil {
				w.logger.Printf("Unknown metric: %s", name)
			}
			continue
		}
		gauge.Record(ctx, r.Value)
	}
	return nil
}

func (w *OTelWriter) Close() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return w.meterProvider.Shutdown(ctx)
}
