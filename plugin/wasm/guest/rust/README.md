# fit-guest (Rust)

The Rust side of fit's WASM plugin ABI for sync: the host functions of the modules `fit` and
`fit_sync` (see `plugin/wasm/sync.go` and [decision 036](../../../../docs/decisions/036-sync.md)),
wrapped, and the records read and written by hand, as the Go guest does: `parse_batch` reads a
batch into `Record`s, `Row` and the value helpers (`int`, `string`, `decimal`, `as_int`,
`as_str`, `as_decimal`, ...) read and make a row's typed values, `emit_record` and `Emitter` hand
records on, and `request` makes an HTTP request of its parts. It has no dependencies.

Records cross in the binary form fit offers (lengths and tags rather than JSON; see `src/binary.rs`
and the I165b section of `docs/dev-plan.md`): the crate exports `fit_sync_form` with its default
feature `binary`, and its helpers read values of either form, told by their first byte, and make
them in the one taken; a value written as canonical JSON by hand still goes. Built with
`default-features = false`, records stay their canonical JSON, as for plugins built before: then
`records` splits a batch into its records' JSON for a plugin that parses them with a JSON library
of its own, and `emit` hands one on.

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
    // Hands its records on in one go as the export returns.
    let mut out = fit::Emitter::new();
    for (i, mut rec) in batch.into_iter().enumerate() {
        if let Ok(Some(mut row)) = Row::parse(&rec.after) {
            row.set("seen", fit::boolean(true));
            rec.after = row.value();
        }
        out.emit(i, &rec);
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
