-- Prints wrk's summary as one line of JSON after the human-readable one,
-- for fitbench to parse. Latencies are in microseconds.
done = function(summary, latency, requests)
  local e = summary.errors
  io.write(string.format(
    'FITBENCH {"requests":%d,"duration_us":%d,"bytes":%d,' ..
    '"errors":{"connect":%d,"read":%d,"write":%d,"status":%d,"timeout":%d},' ..
    '"latency_us":{"p50":%d,"p90":%d,"p99":%d,"p999":%d,"max":%d,"mean":%.2f,"stdev":%.2f}}\n',
    summary.requests, summary.duration, summary.bytes,
    e.connect, e.read, e.write, e.status, e.timeout,
    latency:percentile(50), latency:percentile(90), latency:percentile(99), latency:percentile(99.9),
    latency.max, latency.mean, latency.stdev))
end
