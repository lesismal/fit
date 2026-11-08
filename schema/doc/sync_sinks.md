A pipeline's sink, and its `dead_letter`, writes records: one of these, named by `module` in its
`sink`, with its `params`. A sink acknowledges a batch once it is written, and writes idempotently,
since what came after the checkpoint is written again after a crash.

| Module | What it does |
| --- | --- |
| [`memory`](#sync-sinks-memory) | Keeps the records in memory, bounded, for tests and trials |
| [`stdout`](#sync-sinks-stdout) | Writes each record to standard output as a line of its canonical JSON |
