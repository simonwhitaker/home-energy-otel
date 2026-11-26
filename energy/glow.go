package energy

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// GlowMeter identifies which kind of message a Glow display published.
type GlowMeter string

const (
	GlowElectricity GlowMeter = "electricitymeter"
	GlowGas         GlowMeter = "gasmeter"
	GlowState       GlowMeter = "STATE"
)

// GlowMessage is one MQTT message from a Glow display, decoded into readings.
type GlowMessage struct {
	Device    string
	Meter     GlowMeter
	Timestamp time.Time
	Readings  []Reading
}

// The Glow display publishes to:
//
//	<prefix>/<device>/STATE
//	<prefix>/<device>/SENSOR/electricitymeter
//	<prefix>/<device>/SENSOR/gasmeter
//
// The prefix is configurable on the display, so topics are read from the end.
func parseGlowTopic(topic string) (device string, meter GlowMeter, err error) {
	parts := strings.Split(topic, "/")
	n := len(parts)

	switch {
	case n >= 3 && parts[n-1] == string(GlowState):
		return parts[n-2], GlowState, nil
	case n >= 4 && parts[n-2] == "SENSOR":
		switch m := GlowMeter(parts[n-1]); m {
		case GlowElectricity, GlowGas:
			return parts[n-3], m, nil
		}
	}
	return "", "", fmt.Errorf("unrecognised Glow topic %q", topic)
}

type glowMeterPayload struct {
	Timestamp string `json:"timestamp"`
	Energy    struct {
		Import struct {
			Cumulative *float64 `json:"cumulative"`
			Price      struct {
				UnitRate       *float64 `json:"unitrate"`
				StandingCharge *float64 `json:"standingcharge"`
			} `json:"price"`
		} `json:"import"`
	} `json:"energy"`
	Power struct {
		Value *float64 `json:"value"`
	} `json:"power"`
}

type glowStatePayload struct {
	Timestamp string `json:"timestamp"`
	HAN       struct {
		RSSI *float64 `json:"rssi"`
		LQI  *float64 `json:"lqi"`
	} `json:"han"`
}

// ParseGlowMessage decodes a message from a Glow display's local MQTT feed.
// Fields the display publishes as null (prices, before a supplier has sent
// them) are left out rather than reported as zero.
func ParseGlowMessage(topic string, payload []byte) (GlowMessage, error) {
	device, meter, err := parseGlowTopic(topic)
	if err != nil {
		return GlowMessage{}, err
	}
	msg := GlowMessage{Device: device, Meter: meter}

	var rawTimestamp string
	switch meter {
	case GlowState:
		var p glowStatePayload
		if err := json.Unmarshal(payload, &p); err != nil {
			return GlowMessage{}, fmt.Errorf("decoding %s: %w", topic, err)
		}
		rawTimestamp = p.Timestamp
		msg.Readings = appendIfSet(msg.Readings, SignalRSSI, p.HAN.RSSI, 1)
		msg.Readings = appendIfSet(msg.Readings, SignalLQI, p.HAN.LQI, 1)

	case GlowElectricity, GlowGas:
		var wrapper map[string]*glowMeterPayload
		if err := json.Unmarshal(payload, &wrapper); err != nil {
			return GlowMessage{}, fmt.Errorf("decoding %s: %w", topic, err)
		}
		p := wrapper[string(meter)]
		if p == nil {
			return GlowMessage{}, fmt.Errorf("decoding %s: no %q object in payload", topic, meter)
		}
		rawTimestamp = p.Timestamp

		imp := p.Energy.Import
		if meter == GlowElectricity {
			msg.Readings = appendIfSet(msg.Readings, LivePowerElectricity, p.Power.Value, 1000) // kW -> W
			msg.Readings = appendIfSet(msg.Readings, MeterElectricity, imp.Cumulative, 1)
			msg.Readings = appendIfSet(msg.Readings, UnitRateElectricity, imp.Price.UnitRate, 1)
			msg.Readings = appendIfSet(msg.Readings, StandingChargeElectricity, imp.Price.StandingCharge, 1)
		} else {
			// Gas power is always zero from the display, so it isn't reported.
			msg.Readings = appendIfSet(msg.Readings, MeterGas, imp.Cumulative, 1)
			msg.Readings = appendIfSet(msg.Readings, UnitRateGas, imp.Price.UnitRate, 1)
			msg.Readings = appendIfSet(msg.Readings, StandingChargeGas, imp.Price.StandingCharge, 1)
		}
	}

	if rawTimestamp == "" {
		return GlowMessage{}, errors.New("decoding " + topic + ": no timestamp in payload")
	}
	ts, err := time.Parse(time.RFC3339, rawTimestamp)
	if err != nil {
		return GlowMessage{}, fmt.Errorf("decoding %s: bad timestamp: %w", topic, err)
	}
	msg.Timestamp = ts

	return msg, nil
}

func appendIfSet(rs []Reading, m Metric, v *float64, scale float64) []Reading {
	if v == nil {
		return rs
	}
	return append(rs, Reading{Metric: m, Value: *v * scale})
}
