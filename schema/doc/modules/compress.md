Compresses responses as they are written, with the encoding the client prefers.

```json route
{
  "middleware": [{"module": "compress", "params": {"encodings": ["zstd", "gzip"], "level": "default", "min_bytes": 512}}],
  "handler": {"module": "respond", "params": {"body": "hello"}}
}
```
