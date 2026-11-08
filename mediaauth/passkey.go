package mediaauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
)

// A BitTorrent passkey is what a private tracker (the role
// bittorrent.tracker) knows a client by, carried in the announce URL as
// /<passkey>/announce or ?passkey=. A policy's passkeys are its tokens,
// static or signed, used as they are, and one passkey for each user,
// derived from the policy's signing key and the user's name, since a
// password cannot ride in a URL a .torrent file keeps for ever.
//
// A user's passkey is the first 32 hex digits of HMAC-SHA256(key, "fit
// bittorrent passkey\x00" + name), key being the policy's signing key, or
// each key of its keyring: Passkey hands out the default key's, and every
// key's is taken, so that rotating a key that is not the default one does
// not change the passkeys handed out. Rotating the secret, or the default
// key, changes every user's passkey. Without a secret, a random key of the
// process's is the signing key, and passkeys change when fit restarts.

// PasskeyChars is how many hex digits a user's passkey has.
const PasskeyChars = 32

// passkeyOf returns the passkey of user name under key.
func passkeyOf(key []byte, name string) string {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("fit bittorrent passkey\x00" + name))
	var sum [sha256.Size]byte
	return hex.EncodeToString(m.Sum(sum[:0]))[:PasskeyChars]
}

// userPasskeys returns the passkeys of st's users, of every key of st, by
// their SHA-256.
func userPasskeys(st *state) map[[32]byte]*user {
	if len(st.users) == 0 {
		return nil
	}
	keys := [][]byte{st.signKey}
	if len(st.keys) > 0 {
		keys = keys[:0]
		for _, k := range st.keys {
			keys = append(keys, k.signKey)
		}
	}
	out := make(map[[32]byte]*user, len(st.users)*len(keys))
	for _, u := range st.users {
		for _, k := range keys {
			out[sha256.Sum256([]byte(passkeyOf(k, u.name)))] = u
		}
	}
	return out
}

// Passkey returns the BitTorrent passkey of the policy's user name, signed
// with the default key.
func (p *Policy) Passkey(name string) (string, error) {
	st := p.state()
	if st == nil {
		return "", ErrUnavailable
	}
	if st.users[name] == nil {
		return "", fmt.Errorf("mediaauth: the policy %s has no user %q", p.name, name)
	}
	key := st.signKey
	if len(st.keys) > 0 {
		key = st.keys[0].signKey
	}
	return passkeyOf(key, name), nil
}

// AllowPasskey checks whether passkey may play stream, as a private
// tracker asks it of every announce and scrape, without waiting: a user's
// passkey with that user's grant, or else a token, as Allow checks it. It
// returns the subject, or an error Status maps to a status: ErrNoCredentials
// for no passkey, ErrBadCredentials for an unknown one, ErrForbidden for a
// grant that does not play stream. A policy that makes play public lets
// every request through, as Allow does; a nil policy too.
func (p *Policy) AllowPasskey(stream, passkey string) (subject string, err error) {
	if p == nil {
		return "", nil
	}
	st := p.state()
	if st == nil {
		return "", ErrUnavailable
	}
	if passkey != "" && len(passkey) <= MaxTokenBytes {
		// Looked up by its SHA-256, as a static token is.
		if u := st.passkeys[sha256.Sum256([]byte(passkey))]; u != nil && !st.public[Play] {
			if !u.grant.Allows(Play, stream) {
				return u.name, ErrForbidden
			}
			return u.name, nil
		}
	}
	return p.Allow(Play, stream, Credentials{Token: passkey})
}
