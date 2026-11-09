# fit-guest (Rust)

The Rust side of fit's WASM plugin ABI for sync: the host functions of the modules `fit` and
`fit_sync` (see `plugin/wasm/sync.go` and [decision 036](../../../../docs/decisions/036-sync.md)),
wrapped, and the records' canonical JSON read and written by hand, as the Go guest does:
`parse_batch` reads a batch into `Record`s, `Row` and the value helpers (`int`, `string`,
`as_int`, `as_str`, ...) read and make a row's typed values, `emit_record` hands a record on, and
`request` makes an HTTP request of its parts. It has no dependencies; `records` splits a batch
into its records' JSON for a plugin that parses them with a JSON library of its own.

A plugin is a WASI reactor built as a `cdylib`:

```toml
[lib]
crate-type = ["cdylib"]

[dependencies]
fit-guest = { path = "path/to/fit/plugin/wasm/guest/rust" }
```

```rust
use fit_guest::{self as fit, Row};

#[no_mangle]
pub extern "C" fn fit_sync_transform(n: u32) -> i32 {
    let batch = match fit::parse_batch(&fit::input(n)) {
        Ok(b) => b,
        Err(e) => {
            fit::fail(None, &e.to_string());
            return fit::FAILED;
        }
    };
    for (i, mut rec) in batch.into_iter().enumerate() {
        if let Ok(Some(mut row)) = Row::parse(&rec.after) {
            row.set("seen", fit::boolean(true));
            rec.after = row.value();
        }
        fit::emit_record(i, &rec);
    }
    fit::OK
}
```

```sh
rustup target add wasm32-wasip1
cargo build --release --target wasm32-wasip1
```

Build with `panic = "abort"` in the release profile: a panic is then a trap, which fails the call.

The crate's unit tests run on the host (`cargo test`); fit's tests build
`plugin/wasm/testdata/rust`, a transform, a codec, a source and a sink on this crate, and run it
through the same tests as the Go plugin (CI's *WASM guests* job; elsewhere they skip without
`cargo` and the `wasm32-wasip1` target).
