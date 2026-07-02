package service

import (
	"testing"

	"go.klarlabs.de/warden/internal/domain"
)

func TestService_SigningKey(t *testing.T) {
	t.Setenv("WARDEN_CONFIG_DIR", t.TempDir())
	dir := initRepo(t)
	svc, err := New(dir, "test", autoApprover{})
	if err != nil {
		t.Fatal(err)
	}

	pub, fp := svc.SigningKey()
	if pub == "" || fp == "" {
		t.Fatal("expected a signing key and fingerprint after New with a writable config dir")
	}
	if fp != domain.KeyFingerprint(pub) {
		t.Errorf("fingerprint %q does not match KeyFingerprint(pub)", fp)
	}
}
