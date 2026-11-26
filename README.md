# home-energy-otel

A Go application that subscribes to the local MQTT feed of a [Hildebrand Glow](https://glowforhomes.com/) display (Display and CAD) and exports smart meter readings via OpenTelemetry (OTLP gRPC) to any compatible backend.

Each MQTT message the display publishes is exported as it arrives: there's no polling and no batching.

## Metrics

| Metric | Unit | Source |
|---|---|---|
| `energy.live.electricity` | W | Instantaneous electricity import power |
| `energy.meter.electricity` | kWh | Cumulative electricity import |
| `energy.meter.gas` | kWh | Cumulative gas import |
| `energy.unit_rate.electricity` / `.gas` | GBP/kWh | Unit rate, when the supplier has sent one |
| `energy.standing_charge.electricity` / `.gas` | GBP/day | Standing charge, when the supplier has sent one |
| `glow.signal.rssi` | dBm | Signal strength between the display and the meters |
| `glow.signal.lqi` | – | Link quality between the display and the meters |

All metrics are gauges, exported once per message and never repeated. The display publishes electricity roughly every 10 seconds, but gas, which comes from a battery-powered meter, only about every 30 minutes. In Datadog, join sparse points with `.fill(linear)`, or e.g. `.fill(last, 3600)` for gas.

There's no live gas metric: the display always reports gas power as zero. To approximate gas power, take the rate of `energy.meter.gas`, e.g. in Datadog `per_second(energy.meter.gas) * 3600000` for watts, averaged over each meter update.

Messages whose timestamp isn't newer than the last one seen for the same meter (e.g. a retained message replayed on reconnect) are dropped.

## Getting started

### 1. Run a broker

`docker-compose.yml` includes a [Mosquitto](https://mosquitto.org/) broker, published on port 1883 so the display can reach it over your network. It refuses anonymous clients, so create a password file first, with one user for the display and one for this app:

```sh
touch mosquitto/passwd && chmod 0700 mosquitto/passwd
docker run --rm -it -v "$PWD/mosquitto:/work" eclipse-mosquitto:2 mosquitto_passwd /work/passwd glow
docker run --rm -it -v "$PWD/mosquitto:/work" eclipse-mosquitto:2 mosquitto_passwd /work/passwd home-energy-otel
```

`mosquitto/passwd` is gitignored.

### 2. Point the display at the broker

In the Glow display's local MQTT settings, set the broker host to the IP address of the machine running Docker, port 1883, and the `glow` user's credentials. Leave the topic prefix as `glow` (or set `MQTT_TOPIC_PREFIX` to match). See Glow's [MQTT article](https://glowforhomes.com/home/articles/mqtt) and [this guide](https://medium.com/@joshua.cooper/glow-local-mqtt-f69b776b7af4) for where to find the settings.

The display publishes to `glow/<device id>/STATE`, `glow/<device id>/SENSOR/electricitymeter` and `glow/<device id>/SENSOR/gasmeter`. The app subscribes to every device under the prefix, so there's no device ID to configure.

### 3. Configure and run the app

| Variable | Default | |
|---|---|---|
| `MQTT_BROKER_URL` | `tcp://mosquitto:1883` | |
| `MQTT_USERNAME` | | |
| `MQTT_PASSWORD` | | |
| `MQTT_TOPIC_PREFIX` | `glow` | |
| `OTEL_EXPORTER_OTLP_ENDPOINT` | | OTel export is off unless set |
| `OTEL_HOSTNAME` | `localhost` | Sent as the `host.name` resource attribute |

Put them in a `.env` file, then:

```sh
docker compose up
```

To run outside Docker, point `MQTT_BROKER_URL` at the broker, e.g. `tcp://localhost:1883`, and `go run .`.

A `docker-compose-hyperdx.yml` file is included for running [HyperDX](https://www.hyperdx.io/) locally as an OTLP backend for testing:

```sh
docker compose -f docker-compose-hyperdx.yml up
```

## Health checks

The app serves on port 8080:

- `/healthz` always returns 200.
- `/readyz` returns 200 while connected to the MQTT broker, 503 otherwise.

A display that stops publishing doesn't make the app unready. Instead the app logs a warning after 5 minutes without an electricity message, and logs again when messages resume.
