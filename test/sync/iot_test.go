package synctest

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"testing"
	"time"

	mqttwire "github.com/lesismal/fit/sync/mqtt/wire"
)

// TestInfluxDBFromMQTT is I220's IoT pipeline end to end: devices publish
// JSON readings to MQTT at QoS 1, kept by the source's session while fit
// is away; the mqtt source decodes them and the influxdb sink writes a
// point per device and second, the pipeline killed at random points and
// started again: InfluxDB ends with every reading once, replays written
// over themselves. It runs in the obs family, which starts Mosquitto
// beside InfluxDB.
func TestInfluxDBFromMQTT(t *testing.T) {
	u2 := obsEnv(t, "FIT_SYNC_INFLUX2_URL")
	t.Setenv("FIT_TEST_INFLUX_TOKEN", obsEnv(t, "FIT_SYNC_INFLUX2_TOKEN"))
	broker := mqttBrokers(t)[0]
	seed := uint64(time.Now().UnixNano())
	t.Logf("seed %d", seed)
	rnd := rand.New(rand.NewPCG(seed, 220))
	name := mqttName(t)
	ctx := hangGuard(t)

	// The source's session, subscribed before the devices publish.
	sess, err := mqttwire.Dial(ctx, mqttwire.Config{Address: broker.addr, ClientID: name, SessionExpiry: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sess.Subscribe(ctx, mqttwire.Subscription{Filter: name + "/#", QoS: 1}); err != nil {
		t.Fatal(err)
	}
	sess.Close()

	const devices, seconds = 6, 150
	pub := mqttDial(t, mqttwire.Config{Address: broker.addr, ClientID: name + "-pub", CleanStart: true})
	var ds []*mqttwire.Delivery
	wantSum := 0
	for s := range seconds {
		for d := range devices {
			at := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC).Add(time.Duration(s) * time.Second).Format(time.RFC3339)
			seq := s*devices + d
			wantSum += seq
			body := fmt.Sprintf(`{"device": "d%d", "at": %q, "temp": %d.5, "seq": %d}`, d, at, 20+d, seq)
			dl, err := pub.Send(ctx, &mqttwire.Message{Topic: fmt.Sprintf("%s/d%d", name, d), QoS: 1, Payload: []byte(body)})
			if err != nil {
				t.Fatal(err)
			}
			ds = append(ds, dl)
		}
	}
	for _, d := range ds {
		if err := d.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}

	m := "fit_iot_" + fmt.Sprint(time.Now().UnixNano())
	cfg := loadSync(t, t.TempDir(), map[string]any{"name": "p", "mode": "once", "on_error": "stop", "checkpoint_interval": "1ms",
		"queue": map[string]any{"max_records": 20 + rnd.IntN(200)},
		"source": map[string]any{"module": "mqtt", "params": map[string]any{"servers": []string{broker.addr}, "client_id": name,
			"topics": []string{name + "/#"}, "codec": map[string]any{"module": "json"}, "idle": "2s"}},
		"sinks": []any{map[string]any{"name": "influx", "batch": map[string]any{"max_records": 1 + rnd.IntN(100), "max_wait": "1ms"},
			"sink": map[string]any{"module": "influxdb", "params": map[string]any{"url": u2, "org": "fit", "bucket": "fit",
				"token_env": "FIT_TEST_INFLUX_TOKEN", "measurement": m, "tags": []string{"device"}, "fields": []string{"temp", "seq"},
				"time_field": "at", "precision": "s"}}}}})
	kills := runKilled(t, cfg, rnd, 6)
	h := http.Header{"Authorization": {"Token " + os.Getenv("FIT_TEST_INFLUX_TOKEN")}}
	if n := influxQL(t, u2, h, "fit", "SELECT count(seq) FROM "+m); n != devices*seconds {
		t.Fatalf("%v points, want %d", n, devices*seconds)
	}
	if s := influxQL(t, u2, h, "fit", "SELECT sum(seq) FROM "+m); s != float64(wantSum) {
		t.Fatalf("the sum %v, want %d", s, wantSum)
	}
	if n := influxQL(t, u2, h, "fit", "SELECT count(temp) FROM "+m+" WHERE device = 'd3'"); n != seconds {
		t.Fatalf("d3: %v points, want %d", n, seconds)
	}
	t.Logf("%d readings, each one point, across %d kills", devices*seconds, kills)
}
