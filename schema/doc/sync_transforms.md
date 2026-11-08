A pipeline's `transforms` change records on their way from the source to the sinks, in order: a
transform may change a record, drop it or split it into several. fit has no transforms yet; filters,
routes, fields, keys, casts, masks and envelopes come in I164, and WASM transforms in I165.
