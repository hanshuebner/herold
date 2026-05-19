package tls

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"log/slog"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// writeCert generates a fresh self-signed ECDSA certificate with dnsNames and
// writes cert.pem / key.pem to dir, optionally using a provided tmp prefix for
// atomic renames (suffix ".tmp"). Returns the cert and key file paths.
func writeCert(t *testing.T, certPath, keyPath string, dnsNames []string) {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("writeCert keygen: %v", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		t.Fatalf("writeCert serial: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: dnsNames[0]},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     dnsNames,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("writeCert CreateCertificate: %v", err)
	}

	cf, err := os.Create(certPath)
	if err != nil {
		t.Fatalf("writeCert create cert: %v", err)
	}
	if err := pem.Encode(cf, &pem.Block{Type: "CERTIFICATE", Bytes: der}); err != nil {
		t.Fatalf("writeCert pem encode cert: %v", err)
	}
	cf.Close()

	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("writeCert marshal key: %v", err)
	}
	kf, err := os.Create(keyPath)
	if err != nil {
		t.Fatalf("writeCert create key: %v", err)
	}
	if err := pem.Encode(kf, &pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}); err != nil {
		t.Fatalf("writeCert pem encode key: %v", err)
	}
	kf.Close()
}

// pollStore waits until the store's default certificate's serial number
// differs from excludeSerial, up to deadline. Returns the new cert or nil on
// timeout.
func pollStoreDefault(t *testing.T, s *Store, excludeSerial *big.Int, deadline time.Time) *tls.Certificate {
	t.Helper()
	for time.Now().Before(deadline) {
		c, err := s.Get(nil)
		if err == nil && c != nil && c.Leaf != nil {
			if excludeSerial == nil || c.Leaf.SerialNumber.Cmp(excludeSerial) != 0 {
				return c
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

// pollStoreHost waits until the store's named host certificate's serial
// differs from excludeSerial, up to deadline.
func pollStoreHost(t *testing.T, s *Store, host string, excludeSerial *big.Int, deadline time.Time) *tls.Certificate {
	t.Helper()
	hello := &tls.ClientHelloInfo{ServerName: host}
	for time.Now().Before(deadline) {
		c, err := s.Get(hello)
		if err == nil && c != nil && c.Leaf != nil {
			if excludeSerial == nil || c.Leaf.SerialNumber.Cmp(excludeSerial) != 0 {
				return c
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return nil
}

// TestFileWatcher_DefaultCertReload tests that overwriting the cert+key files
// causes the store's fallback certificate to be updated.
func TestFileWatcher_DefaultCertReload(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	// Write initial cert.
	writeCert(t, certPath, keyPath, []string{"mail.test"})

	store := NewStore()
	cert1, err := LoadFromFile(certPath, keyPath)
	if err != nil {
		t.Fatalf("initial load: %v", err)
	}
	store.SetDefault(cert1)
	serial1 := cert1.Leaf.SerialNumber

	logger := slog.Default()
	entries := []WatchEntry{{CertFile: certPath, KeyFile: keyPath}}
	fw, err := StartFileWatcher(store, entries, logger)
	if err != nil {
		t.Fatalf("StartFileWatcher: %v", err)
	}
	defer fw.Stop()

	// Overwrite the files with a new cert.
	writeCert(t, certPath, keyPath, []string{"mail.test"})

	got := pollStoreDefault(t, store, serial1, time.Now().Add(3*time.Second))
	if got == nil {
		t.Fatal("timed out waiting for store to reflect new certificate")
	}
	if got.Leaf.SerialNumber.Cmp(serial1) == 0 {
		t.Fatal("store still holds original certificate after file update")
	}
}

// TestFileWatcher_HostCertReload tests that a per-host cert entry is updated
// on file change.
func TestFileWatcher_HostCertReload(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	writeCert(t, certPath, keyPath, []string{"smtp.test"})

	store := NewStore()
	cert1, err := LoadFromFile(certPath, keyPath)
	if err != nil {
		t.Fatalf("initial load: %v", err)
	}
	store.Add("smtp.test", cert1)
	serial1 := cert1.Leaf.SerialNumber

	entries := []WatchEntry{{CertFile: certPath, KeyFile: keyPath, Hostname: "smtp.test"}}
	fw, err := StartFileWatcher(store, entries, slog.Default())
	if err != nil {
		t.Fatalf("StartFileWatcher: %v", err)
	}
	defer fw.Stop()

	writeCert(t, certPath, keyPath, []string{"smtp.test"})

	got := pollStoreHost(t, store, "smtp.test", serial1, time.Now().Add(3*time.Second))
	if got == nil {
		t.Fatal("timed out waiting for host cert reload")
	}
	if got.Leaf.SerialNumber.Cmp(serial1) == 0 {
		t.Fatal("host cert not updated after file change")
	}
}

// TestFileWatcher_BadReloadKeepsPrevious tests that writing garbage to the
// cert file causes the reload to fail gracefully, leaving the old cert in the
// store.
func TestFileWatcher_BadReloadKeepsPrevious(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	writeCert(t, certPath, keyPath, []string{"mail.test"})

	store := NewStore()
	cert1, err := LoadFromFile(certPath, keyPath)
	if err != nil {
		t.Fatalf("initial load: %v", err)
	}
	store.SetDefault(cert1)
	serial1 := cert1.Leaf.SerialNumber

	entries := []WatchEntry{{CertFile: certPath, KeyFile: keyPath}}
	fw, err := StartFileWatcher(store, entries, slog.Default())
	if err != nil {
		t.Fatalf("StartFileWatcher: %v", err)
	}
	defer fw.Stop()

	// Write garbage to the cert file.
	if err := os.WriteFile(certPath, []byte("not a valid PEM certificate"), 0o600); err != nil {
		t.Fatalf("write garbage: %v", err)
	}

	// Wait long enough for the watcher to attempt a reload.
	time.Sleep(500 * time.Millisecond)

	// The store should still hold the original certificate.
	c, err := store.Get(nil)
	if err != nil {
		t.Fatalf("Get after bad reload: %v", err)
	}
	if c.Leaf == nil || c.Leaf.SerialNumber.Cmp(serial1) != 0 {
		t.Fatal("bad reload replaced the working certificate")
	}
}

// TestFileWatcher_RenameSwap tests the atomic-rename pattern used by certbot
// and cert-manager: write to a .tmp file in the same directory, then rename
// over the target. The watcher must catch this via the directory watch.
func TestFileWatcher_RenameSwap(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	writeCert(t, certPath, keyPath, []string{"mail.test"})

	store := NewStore()
	cert1, err := LoadFromFile(certPath, keyPath)
	if err != nil {
		t.Fatalf("initial load: %v", err)
	}
	store.SetDefault(cert1)
	serial1 := cert1.Leaf.SerialNumber

	entries := []WatchEntry{{CertFile: certPath, KeyFile: keyPath}}
	fw, err := StartFileWatcher(store, entries, slog.Default())
	if err != nil {
		t.Fatalf("StartFileWatcher: %v", err)
	}
	defer fw.Stop()

	// Write new cert+key to tmp files, then rename atomically.
	certTmp := certPath + ".tmp"
	keyTmp := keyPath + ".tmp"
	writeCert(t, certTmp, keyTmp, []string{"mail.test"})
	if err := os.Rename(certTmp, certPath); err != nil {
		t.Fatalf("rename cert: %v", err)
	}
	if err := os.Rename(keyTmp, keyPath); err != nil {
		t.Fatalf("rename key: %v", err)
	}

	got := pollStoreDefault(t, store, serial1, time.Now().Add(3*time.Second))
	if got == nil {
		t.Fatal("timed out waiting for rename-swap reload")
	}
	if got.Leaf.SerialNumber.Cmp(serial1) == 0 {
		t.Fatal("rename-swap did not update the store")
	}
}

// TestFileWatcher_Stop tests that Stop() is safe to call multiple times and
// that the watcher exits cleanly.
func TestFileWatcher_Stop(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")
	writeCert(t, certPath, keyPath, []string{"mail.test"})

	store := NewStore()
	cert1, _ := LoadFromFile(certPath, keyPath)
	store.SetDefault(cert1)

	entries := []WatchEntry{{CertFile: certPath, KeyFile: keyPath}}
	fw, err := StartFileWatcher(store, entries, slog.Default())
	if err != nil {
		t.Fatalf("StartFileWatcher: %v", err)
	}

	fw.Stop()
	fw.Stop() // second call must not block or panic
}

// TestFileWatcher_NilStop verifies that Stop on a nil FileWatcher is safe.
func TestFileWatcher_NilStop(t *testing.T) {
	var fw *FileWatcher
	fw.Stop() // must not panic
}

// TestStartFileWatcher_NoEntries verifies that passing an empty entries slice
// returns nil without error (no watcher started).
func TestStartFileWatcher_NoEntries(t *testing.T) {
	store := NewStore()
	fw, err := StartFileWatcher(store, nil, slog.Default())
	if err != nil {
		t.Fatalf("StartFileWatcher(nil entries): %v", err)
	}
	if fw != nil {
		t.Fatal("expected nil watcher for empty entries")
	}
}
