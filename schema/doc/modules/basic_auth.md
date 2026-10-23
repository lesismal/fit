Asks for a user name and password. Hashes are bcrypt, as `fit hash-password` or `htpasswd -B`
makes them.

```caddyfile
example.com {
	basic_auth /admin/* {
		alice $2a$14$Zkx19XLiW6VYouLHR5NmfOFU0z2GTNmpkT/5qqR7hx4IjWJPDhjvG
	}
	respond "hello"
}
```

```json route
{
  "match": {"host": ["example.com"], "path": ["/admin/*"]},
  "middleware": [{"module": "basic_auth", "params": {
    "realm": "admin",
    "users": [{"username": "alice", "password_hash": "$2a$14$Zkx19XLiW6VYouLHR5NmfOFU0z2GTNmpkT/5qqR7hx4IjWJPDhjvG"}],
    "user_header": "X-User"
  }}],
  "handler": {"module": "respond", "params": {"body": "hello"}}
}
```
