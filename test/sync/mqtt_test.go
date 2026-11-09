package synctest

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lesismal/fit/config"
	"github.com/lesismal/fit/module"
	fitsync "github.com/lesismal/fit/sync"
	_ "github.com/lesismal/fit/sync/mqtt"
	mqttwire "github.com/lesismal/fit/sync/mqtt/wire"
)

// mqttBroker is a broker of the interop tests, at an address
// test/sync/mqtt.sh prints.
type mqttBroker struct {
	name, addr string
}

func mqttBrokers(t *testing.T) []mqttBroker {
	t.Helper()
	var bs []mqttBroker
	for _, b := range []struct{ name, env string }{{"mosquitto", "FIT_SYNC_MQTT_MOSQUITTO"}, {"emqx", "FIT_SYNC_MQTT_EMQX"}} {
		if a := os.Getenv(b.env); a != "" {
			bs = append(bs, mqttBroker{b.name, a})
		}
	}
	if len(bs) == 0 {
		mqttEnv(t, "FIT_SYNC_MQTT_MOSQUITTO")
	}
	return bs
}

// mqttEnv returns the variable, skipping the test when it is unset
// (test/sync/mqtt.sh prints them).
func mqttEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		if os.Getenv("FIT_SYNC_INTEROP") == "required" {
			t.Fatalf("%s is not set", name)
		}
		t.Skipf("no MQTT broker: %s is not set (test/sync/mqtt.sh prints it)", name)
	}
	return v
}

// eachMQTT runs f on every broker in both versions.
func eachMQTT(t *testing.T, f func(t *testing.T, b mqttBroker, version string)) {
	for _, b := range mqttBrokers(t) {
		for _, v := range []string{"3.1.1", "5"} {
			t.Run(b.name+"-"+v, func(t *testing.T) { f(t, b, v) })
		}
	}
}

func mqttVersion(v string) byte {
	if v == "3.1.1" {
		return mqttwire.Version311
	}
	return mqttwire.Version5
}

// mqttName is a name of the test's own, for topics and client IDs.
func mqttName(t *testing.T) string {
	return fmt.Sprintf("fit-%s-%d", strings.NewReplacer("/", "-", "_", "-", ".", "-").Replace(t.Name()), time.Now().UnixNano()%1e9)
}

func mqttDial(t *testing.T, cfg mqttwire.Config) *mqttwire.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := mqttwire.Dial(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// mqttPublish publishes n messages to topic, numbered from first.
func mqttPublish(t *testing.T, c *mqttwire.Client, topic string, qos byte, first, n int) {
	t.Helper()
	ctx := hangGuard(t)
	var ds []*mqttwire.Delivery
	for i := first; i < first+n; i++ {
		d, err := c.Send(ctx, &mqttwire.Message{Topic: fmt.Sprintf("%s/%d", topic, i%7), QoS: qos, Payload: []byte(fmt.Sprint(i))})
		if err != nil {
			t.Fatal(err)
		}
		ds = append(ds, d)
	}
	for _, d := range ds {
		if err := d.Wait(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

// mqttSourceConfig is a pipeline from the source mqtt to the memory store
// named store.
func mqttSourceConfig(t *testing.T, stateDir, store string, params map[string]any, extra map[string]any) *config.Config {
	p := map[string]any{"name": "p", "checkpoint_interval": "10ms",
		"source": map[string]any{"module": "mqtt", "params": params},
		"sinks": []any{map[string]any{"name": "s", "batch": map[string]any{"max_records": 50, "max_wait": "1ms"},
			"sink": map[string]any{"module": "memory", "params": map[string]any{"store": store, "max_records": 1000000}}}}}
	for k, v := range extra {
		p[k] = v
	}
	return loadSync(t, stateDir, p)
}

// payloads counts the records of the store by their payloads.
func payloads(store string) map[string]int {
	got := map[string]int{}
	for _, r := range fitsync.MemoryStore(store).Records() {
		p, _ := r.After.Get("payload")
		got[p.Text()]++
	}
	return got
}

// waitPayloads waits until the store holds every payload from 0 to n.
func waitPayloads(t *testing.T, store string, n int) map[string]int {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for {
		got := payloads(store)
		missing := 0
		for i := range n {
			if got[fmt.Sprint(i)] == 0 {
				missing++
			}
		}
		if missing == 0 {
			return got
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d of %d messages missing", missing, n)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// startSync applies cfg on a runtime of its own, which kill, when not nil,
// may kill, and waits for the pipeline to run.
func startSync(t *testing.T, cfg *config.Config, kill func(fitsync.Point) bool) *fitsync.Runtime {
	t.Helper()
	opts := fitsync.Options{}
	if kill != nil {
		opts.Crash = func(_ string, p fitsync.Point) bool { return kill(p) }
	}
	rt := fitsync.NewRuntime(opts)
	if err := rt.Apply(cfg.Sync); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Minute)
	for st := redisState(rt); st != "running"; st = redisState(rt) {
		if time.Now().After(deadline) {
			t.Fatalf("the pipeline is %s: %+v", st, rt.Status())
		}
		time.Sleep(time.Millisecond)
	}
	return rt
}

// mqttWarm publishes to topic until the pipeline writes to store what it
// sent: the source has subscribed. Its records are not the test's.
func mqttWarm(t *testing.T, pub *mqttwire.Client, topic, store string) {
	t.Helper()
	deadline := time.Now().Add(time.Minute)
	for payloads(store)["warm"] == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the source never subscribed")
		}
		if err := pub.Publish(hangGuard(t), &mqttwire.Message{Topic: topic + "/warm", QoS: 1, Payload: []byte("warm")}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestMQTTAcrossKills: QoS 1 messages published while the pipeline runs,
// killed part way, and while it is away, all reach the sink once it runs
// again; its session kept them.
func TestMQTTAcrossKills(t *testing.T) {
	eachMQTT(t, func(t *testing.T, b mqttBroker, version string) {
		seed := uint64(time.Now().UnixNano())
		t.Logf("seed %d", seed)
		rnd := rand.New(rand.NewPCG(seed, 11))
		name := mqttName(t)
		store := name
		fitsync.MemoryStore(store).Reset()
		params := map[string]any{"servers": []string{b.addr}, "client_id": name, "version": version, "topics": []string{name + "/#"}}
		cfg := mqttSourceConfig(t, t.TempDir(), store, params, nil)
		pub := mqttDial(t, mqttwire.Config{Address: b.addr, Version: mqttVersion(version), ClientID: name + "-pub", CleanStart: true})

		// The first run makes the session, and is killed while it reads.
		points := []fitsync.Point{fitsync.CrashEmitted, fitsync.CrashDispatched, fitsync.CrashWritten, fitsync.CrashAcknowledged}
		at, nth := points[rnd.IntN(len(points))], int32(1+rnd.IntN(5))
		if at == fitsync.CrashEmitted {
			nth = int32(20 + rnd.IntN(100)) // a record's; the others a batch's
		}
		var seen atomic.Int32
		var armed atomic.Bool // once warm: the kill falls among the test's messages
		rt := startSync(t, cfg, func(p fitsync.Point) bool { return armed.Load() && p == at && seen.Add(1) == nth })
		mqttWarm(t, pub, name, store)
		armed.Store(true)
		const n = 400
		mqttPublish(t, pub, name, 1, 0, n/2)
		deadline := time.Now().Add(time.Minute)
		for redisState(rt) != "killed" {
			if time.Now().After(deadline) {
				t.Fatalf("not killed at %v %d: %+v", at, nth, rt.Status())
			}
			time.Sleep(time.Millisecond)
		}
		rt.Close(context.Background())
		before := len(fitsync.MemoryStore(store).Records())
		// Published while fit is away.
		mqttPublish(t, pub, name, 1, n/2, n/2)
		rt = startSync(t, cfg, nil)
		defer rt.Close(context.Background())
		got := waitPayloads(t, store, n)
		t.Logf("killed at %v after %d records: %d messages, %d records", at, before, n, len(fitsync.MemoryStore(store).Records()))
		if delete(got, "warm"); len(got) != n {
			t.Fatalf("%d payloads", len(got))
		}
	})
}

// TestMQTTQoS2Once: QoS 2 messages published while the pipeline runs and
// while it is stopped reach the sink once each.
func TestMQTTQoS2Once(t *testing.T) {
	eachMQTT(t, func(t *testing.T, b mqttBroker, version string) {
		name := mqttName(t)
		store := name
		fitsync.MemoryStore(store).Reset()
		params := map[string]any{"servers": []string{b.addr}, "client_id": name, "version": version, "topics": []string{name + "/#"}, "qos": 2}
		cfg := mqttSourceConfig(t, t.TempDir(), store, params, nil)
		pub := mqttDial(t, mqttwire.Config{Address: b.addr, Version: mqttVersion(version), ClientID: name + "-pub", CleanStart: true})
		const n = 300
		rt := startSync(t, cfg, nil)
		mqttWarm(t, pub, name, store)
		mqttPublish(t, pub, name, 2, 0, n/3)
		waitPayloads(t, store, n/3)
		mqttPublish(t, pub, name, 2, n/3, n/3)
		rt.Close(context.Background())
		mqttPublish(t, pub, name, 2, 2*n/3, n-2*n/3)
		rt = startSync(t, cfg, nil)
		defer rt.Close(context.Background())
		waitPayloads(t, store, n)
		time.Sleep(500 * time.Millisecond) // for copies that would follow
		got := payloads(store)
		delete(got, "warm")
		for p, k := range got {
			if k != 1 {
				t.Fatalf("payload %s %d times", p, k)
			}
		}
		if len(got) != n {
			t.Fatalf("%d payloads", len(got))
		}
	})
}

// runJob runs the pipeline p of s to its end, within a minute.
func runJob(t *testing.T, s *config.Sync) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if st, err := fitsync.RunPipeline(ctx, s, "p", fitsync.Options{}); err != nil || st.State != "finished" {
		t.Fatalf("%v: %+v", err, st)
	}
}

// TestMQTTSinkAndJob publishes records through the sink mqtt, a subscriber
// reading them back, and reads them again with the source in a job, which
// ends once idle.
func TestMQTTSinkAndJob(t *testing.T) {
	eachMQTT(t, func(t *testing.T, b mqttBroker, version string) {
		name := mqttName(t)
		store := name
		fitsync.MemoryStore(store).Reset()
		ctx := hangGuard(t)
		sub := mqttDial(t, mqttwire.Config{Address: b.addr, Version: mqttVersion(version), ClientID: name + "-sub", CleanStart: true})
		if _, err := sub.Subscribe(ctx, mqttwire.Subscription{Filter: name + "/#", QoS: 2}); err != nil {
			t.Fatal(err)
		}
		// The job's session, made by a first job that reads nothing.
		// Each job of a state directory of its own: a job finished is not
		// run again.
		job := func() *config.Sync {
			return mqttSourceConfig(t, t.TempDir(), store, map[string]any{"servers": []string{b.addr}, "client_id": name + "-job",
				"version": version, "topics": []string{name + "/#"}, "idle": "1s"}, map[string]any{"mode": "once"}).Sync
		}
		runJob(t, job())
		const n = 200
		sinkParams := map[string]any{"servers": []string{b.addr}, "version": version, "topic": name + "/{namespace}", "qos": 2}
		if version == "5" {
			sinkParams["user_properties"] = map[string]string{"from": "fit"}
			sinkParams["message_expiry"] = "1h"
		}
		cfg := loadSync(t, t.TempDir(), map[string]any{"name": "p", "mode": "once",
			"source": map[string]any{"module": "generate", "params": map[string]any{"records": n, "namespace": "gen"}},
			"sinks": []any{map[string]any{"name": "m", "batch": map[string]any{"max_records": 30, "max_wait": "1ms"},
				"sink": map[string]any{"module": "mqtt", "params": sinkParams}}}})
		runJob(t, cfg.Sync)
		for i := range n {
			select {
			case m := <-sub.Messages():
				sub.Ack(m)
				if m.Topic != name+"/gen" || len(m.Payload) == 0 || version == "5" && (len(m.UserProperties) != 1 || m.UserProperties[0].Value != "fit") {
					t.Fatalf("%d: %+v", i, m)
				}
			case <-time.After(30 * time.Second):
				t.Fatalf("%d of %d messages", i, n)
			}
		}
		runJob(t, job())
		if recs := fitsync.MemoryStore(store).Records(); len(recs) != n || recs[0].Namespace != name+"/gen" {
			t.Fatalf("the job read %d records", len(recs))
		}
	})
}

// TestMQTTAuthTLS: the source's Test over Mosquitto's password and TLS
// listeners, and a password refused.
func TestMQTTAuthTLS(t *testing.T) {
	auth := mqttEnv(t, "FIT_SYNC_MQTT_MOSQUITTO_AUTH")
	tlsAddr := mqttEnv(t, "FIT_SYNC_MQTT_MOSQUITTO_TLS")
	t.Setenv("FIT_TEST_SYNC_MQTT_PASSWORD", mqttEnv(t, "FIT_SYNC_MQTT_PASSWORD"))
	t.Setenv("FIT_TEST_SYNC_MQTT_WRONG", "wrong")
	user, ca := mqttEnv(t, "FIT_SYNC_MQTT_USER"), mqttEnv(t, "FIT_SYNC_MQTT_CA")
	for _, v := range []string{"3.1.1", "5"} {
		for _, tc := range []struct {
			addr string
			tls  bool
			env  string
			ok   bool
		}{{auth, false, "FIT_TEST_SYNC_MQTT_PASSWORD", true}, {tlsAddr, true, "FIT_TEST_SYNC_MQTT_PASSWORD", true},
			{auth, false, "FIT_TEST_SYNC_MQTT_WRONG", false}} {
			p := map[string]any{"servers": []string{tc.addr}, "client_id": mqttName(t), "version": v, "topics": []string{"fit/#"},
				"username": user, "password_env": tc.env}
			if tc.tls {
				p["tls"], p["ca_file"] = true, ca
			}
			cfg := mqttSourceConfig(t, t.TempDir(), "unused", p, nil)
			m, err := module.Load(fitsync.Sources, cfg.Sync.Pipelines[0].Source, nil)
			if err != nil {
				t.Fatal(err)
			}
			err = m.(interface{ Test(context.Context) error }).Test(hangGuard(t))
			module.Cleanup(m)
			var ce *mqttwire.ConnectError
			if tc.ok && err != nil || !tc.ok && (!errors.As(err, &ce) || !ce.Auth()) {
				t.Fatalf("%s %s %v: %v", v, tc.addr, tc.ok, err)
			}
		}
	}
	// The wire client over TLS, the certificate checked.
	pem, _ := os.ReadFile(ca)
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM(pem)
	c := mqttDial(t, mqttwire.Config{Address: tlsAddr, ClientID: mqttName(t), CleanStart: true, Username: user,
		Password: os.Getenv("FIT_SYNC_MQTT_PASSWORD"), TLS: &tls.Config{RootCAs: pool}})
	if err := c.Publish(hangGuard(t), &mqttwire.Message{Topic: "fit/tls", QoS: 1}); err != nil {
		t.Fatal(err)
	}
}
