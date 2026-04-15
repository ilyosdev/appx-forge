package auth

import (
	"strings"
	"testing"
	"time"
)

func TestSignAndVerify(t *testing.T) {
	key := []byte("test-secret-key-256bit-minimum!!")
	rawURL := "https://agent.internal:8080/sandboxes/abc123/files"

	signed, err := SignURL(rawURL, key, 5*time.Minute)
	if err != nil {
		t.Fatalf("SignURL failed: %v", err)
	}

	original, err := VerifyURL(signed, key)
	if err != nil {
		t.Fatalf("VerifyURL failed: %v", err)
	}

	if original != rawURL {
		t.Errorf("VerifyURL returned %q, want %q", original, rawURL)
	}
}

func TestVerifyRejectsTamperedURL(t *testing.T) {
	key := []byte("test-secret-key-256bit-minimum!!")
	rawURL := "https://agent.internal:8080/sandboxes/abc123/files"

	signed, err := SignURL(rawURL, key, 5*time.Minute)
	if err != nil {
		t.Fatalf("SignURL failed: %v", err)
	}

	// Tamper with the URL path
	tampered := strings.Replace(signed, "abc123", "evil999", 1)

	_, err = VerifyURL(tampered, key)
	if err == nil {
		t.Error("VerifyURL should reject tampered URL")
	}
}

func TestVerifyRejectsExpiredURL(t *testing.T) {
	key := []byte("test-secret-key-256bit-minimum!!")
	rawURL := "https://agent.internal:8080/sandboxes/abc123/files"

	// Sign with zero expiry (already expired)
	signed, err := SignURL(rawURL, key, 0)
	if err != nil {
		t.Fatalf("SignURL failed: %v", err)
	}

	// Small sleep to ensure we're past expiry
	time.Sleep(10 * time.Millisecond)

	_, err = VerifyURL(signed, key)
	if err == nil {
		t.Error("VerifyURL should reject expired URL")
	}
	if !strings.Contains(err.Error(), "expired") {
		t.Errorf("error should mention expiry, got: %v", err)
	}
}

func TestVerifyRejectsWrongKey(t *testing.T) {
	keyA := []byte("key-alpha-256bit-minimum!!!!!!!!")
	keyB := []byte("key-bravo-256bit-minimum!!!!!!!!")
	rawURL := "https://agent.internal:8080/sandboxes/abc123/files"

	signed, err := SignURL(rawURL, keyA, 5*time.Minute)
	if err != nil {
		t.Fatalf("SignURL failed: %v", err)
	}

	_, err = VerifyURL(signed, keyB)
	if err == nil {
		t.Error("VerifyURL should reject URL signed with different key")
	}
}

func TestSignedURLContainsExpiry(t *testing.T) {
	key := []byte("test-secret-key-256bit-minimum!!")
	rawURL := "https://agent.internal:8080/sandboxes/abc123/files"

	signed, err := SignURL(rawURL, key, 5*time.Minute)
	if err != nil {
		t.Fatalf("SignURL failed: %v", err)
	}

	if !strings.Contains(signed, "expires=") {
		t.Errorf("signed URL should contain 'expires=' param, got: %s", signed)
	}
}

func TestSignedURLContainsSignature(t *testing.T) {
	key := []byte("test-secret-key-256bit-minimum!!")
	rawURL := "https://agent.internal:8080/sandboxes/abc123/files"

	signed, err := SignURL(rawURL, key, 5*time.Minute)
	if err != nil {
		t.Fatalf("SignURL failed: %v", err)
	}

	if !strings.Contains(signed, "sig=") {
		t.Errorf("signed URL should contain 'sig=' param, got: %s", signed)
	}
}
