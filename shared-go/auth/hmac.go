// Package auth provides HMAC-SHA256 signed URL utilities for the file-push protocol.
//
// SignURL generates a signed URL with an expiry timestamp and HMAC-SHA256 signature.
// VerifyURL validates the signature and checks expiry, returning the original URL
// with sig and expires params stripped (prevents forwarding signed URLs).
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// SignURL appends `expires` (Unix timestamp) and `sig` (hex-encoded HMAC-SHA256)
// query parameters to the given URL. The HMAC is computed over the full URL
// (path + query, including the expires param) before the sig is appended.
func SignURL(rawURL string, key []byte, expiry time.Duration) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("parsing URL: %w", err)
	}

	// Add expires param
	expiresAt := time.Now().Add(expiry).Unix()
	q := u.Query()
	q.Set("expires", strconv.FormatInt(expiresAt, 10))
	u.RawQuery = q.Encode()

	// Compute HMAC-SHA256 over path?query (without sig)
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(u.Path + "?" + u.RawQuery))
	sig := hex.EncodeToString(mac.Sum(nil))

	// Append sig
	q.Set("sig", sig)
	u.RawQuery = q.Encode()

	return u.String(), nil
}

// VerifyURL validates the HMAC-SHA256 signature and checks the expiry timestamp.
// On success, returns the original URL with sig and expires params removed.
// Uses hmac.Equal for constant-time comparison (prevents timing attacks).
func VerifyURL(signedURL string, key []byte) (string, error) {
	u, err := url.Parse(signedURL)
	if err != nil {
		return "", fmt.Errorf("parsing signed URL: %w", err)
	}

	q := u.Query()

	// Extract and validate sig
	sigHex := q.Get("sig")
	if sigHex == "" {
		return "", fmt.Errorf("missing sig parameter")
	}

	// Extract and validate expires
	expiresStr := q.Get("expires")
	if expiresStr == "" {
		return "", fmt.Errorf("missing expires parameter")
	}

	expires, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil {
		return "", fmt.Errorf("parsing expiry: %w", err)
	}

	if time.Now().Unix() > expires {
		return "", fmt.Errorf("URL expired")
	}

	// Recompute HMAC over path?query WITHOUT sig param
	q.Del("sig")
	u.RawQuery = q.Encode()

	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(u.Path + "?" + u.RawQuery))
	expected := mac.Sum(nil)

	actual, err := hex.DecodeString(sigHex)
	if err != nil {
		return "", fmt.Errorf("decoding signature: %w", err)
	}

	if !hmac.Equal(expected, actual) {
		return "", fmt.Errorf("invalid signature")
	}

	// Strip expires and sig, return original URL
	q.Del("expires")
	u.RawQuery = q.Encode()

	// If no remaining query params, clear RawQuery to avoid trailing "?"
	if u.RawQuery == "" {
		u.ForceQuery = false
	}

	return u.String(), nil
}
