package energy

import "fmt"

// Metric describes one exported time series.
type Metric struct {
	Name        string
	Unit        string // UCUM, as OpenTelemetry expects
	Description string
}

var (
	LivePowerElectricity = Metric{"energy.live.electricity", "W", "Instantaneous electricity import power"}
	MeterElectricity     = Metric{"energy.meter.electricity", "kW.h", "Cumulative electricity import meter reading"}
	MeterGas             = Metric{"energy.meter.gas", "kW.h", "Cumulative gas import meter reading"}

	UnitRateElectricity       = Metric{"energy.unit_rate.electricity", "{GBP}/kW.h", "Electricity unit rate"}
	UnitRateGas               = Metric{"energy.unit_rate.gas", "{GBP}/kW.h", "Gas unit rate"}
	StandingChargeElectricity = Metric{"energy.standing_charge.electricity", "{GBP}/d", "Electricity daily standing charge"}
	StandingChargeGas         = Metric{"energy.standing_charge.gas", "{GBP}/d", "Gas daily standing charge"}

	SignalRSSI = Metric{"glow.signal.rssi", "dBm", "Signal strength between the Glow display and the smart meter HAN"}
	SignalLQI  = Metric{"glow.signal.lqi", "1", "Link quality between the Glow display and the smart meter HAN"}
)

// AllMetrics lists every metric a Reading may carry.
var AllMetrics = []Metric{
	LivePowerElectricity,
	MeterElectricity,
	MeterGas,
	UnitRateElectricity,
	UnitRateGas,
	StandingChargeElectricity,
	StandingChargeGas,
	SignalRSSI,
	SignalLQI,
}

type Reading struct {
	Metric Metric
	Value  float64
}

func (r Reading) String() string {
	return fmt.Sprintf("%s: %g %s", r.Metric.Name, r.Value, r.Metric.Unit)
}
