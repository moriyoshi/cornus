package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	authKeyStoreEnv       = "CORNUS_AUTH_KEYSTORE"
	authorizedKeysEnv     = "CORNUS_AUTHORIZED_KEYS"
	authDirName           = "auth"
	authorizedKeysName    = "authorized_keys"
	enrollmentSecretName  = "enrollment.secret"
	enrollmentSecretBytes = 32
)

var errSSHKeyStoreReadOnly = errors.New("SSH key enrollment is disabled by CORNUS_AUTH_KEYSTORE=none; provision CORNUS_AUTHORIZED_KEYS")

type sshAuthorizedKey struct {
	PublicKey ssh.PublicKey
	Name      string
	Enrolled  time.Time
}

func (k sshAuthorizedKey) subject() string {
	if k.Name != "" {
		return k.Name
	}
	return ssh.FingerprintSHA256(k.PublicKey)
}

type sshKeyStore struct {
	mu               sync.Mutex
	mode             string
	path             string
	enrollmentPath   string
	configured       []sshAuthorizedKey
	enrollmentSecret string
}

// newSSHKeyStore returns nil for the untouched default posture. Key auth is
// activated by an explicit keystore mode, configured authorized keys, or a
// runtime store left by an earlier enrollment.
func newSSHKeyStore(dataDir string) (*sshKeyStore, error) {
	modeValue := os.Getenv(authKeyStoreEnv)
	mode := strings.TrimSpace(modeValue)
	modeExplicit := mode != ""
	if mode == "" {
		mode = "file"
	}
	if mode != "file" && mode != "none" {
		return nil, fmt.Errorf("%s must be file or none (got %q)", authKeyStoreEnv, modeValue)
	}

	envKeys := strings.TrimSpace(os.Getenv(authorizedKeysEnv))
	var authDir, path string
	if dataDir != "" {
		authDir = filepath.Join(dataDir, authDirName)
		path = filepath.Join(authDir, authorizedKeysName)
	}
	_, statErr := os.Stat(path)
	storeExists := statErr == nil
	if statErr != nil && !os.IsNotExist(statErr) && path != "" {
		return nil, fmt.Errorf("stat SSH authorized keys %s: %w", path, statErr)
	}
	if !modeExplicit && envKeys == "" && !storeExists {
		return nil, nil
	}
	if mode == "file" && dataDir == "" {
		return nil, errors.New("CORNUS_AUTH_KEYSTORE=file requires a non-empty DataDir")
	}

	configured, err := parseSSHAuthorizedKeys([]byte(envKeys))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", authorizedKeysEnv, err)
	}
	store := &sshKeyStore{
		mode:           mode,
		path:           path,
		enrollmentPath: filepath.Join(authDir, enrollmentSecretName),
		configured:     configured,
	}
	if mode == "file" {
		if err := os.MkdirAll(authDir, 0o700); err != nil {
			return nil, fmt.Errorf("create SSH auth directory: %w", err)
		}
		if storeExists {
			if _, err := readSecureRegularFile(path); err != nil {
				return nil, err
			}
		}
		secret, err := loadOrCreateEnrollmentSecret(store.enrollmentPath)
		if err != nil {
			return nil, err
		}
		store.enrollmentSecret = secret
	}
	if _, err := store.keysLocked(); err != nil {
		return nil, err
	}
	return store, nil
}

func (s *sshKeyStore) writable() bool { return s != nil && s.mode == "file" }

func (s *sshKeyStore) keys() ([]sshAuthorizedKey, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.keysLocked()
}

func (s *sshKeyStore) keysLocked() ([]sshAuthorizedKey, error) {
	keys := append([]sshAuthorizedKey(nil), s.configured...)
	if s.mode != "file" {
		return deduplicateSSHKeys(keys), nil
	}
	raw, err := readSecureRegularFile(s.path)
	if os.IsNotExist(err) {
		return deduplicateSSHKeys(keys), nil
	}
	if err != nil {
		return nil, err
	}
	stored, err := parseSSHAuthorizedKeys(raw)
	if err != nil {
		return nil, fmt.Errorf("parse SSH authorized keys %s: %w", s.path, err)
	}
	return deduplicateSSHKeys(append(keys, stored...)), nil
}

func (s *sshKeyStore) find(fingerprint string) (sshAuthorizedKey, bool, error) {
	keys, err := s.keys()
	if err != nil {
		return sshAuthorizedKey{}, false, err
	}
	for _, key := range keys {
		if ssh.FingerprintSHA256(key.PublicKey) == fingerprint {
			return key, true, nil
		}
	}
	return sshAuthorizedKey{}, false, nil
}

func (s *sshKeyStore) enroll(key sshAuthorizedKey, code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.writable() {
		return errSSHKeyStoreReadOnly
	}
	if subtle.ConstantTimeCompare([]byte(code), []byte(s.enrollmentSecret)) != 1 {
		return errors.New("invalid enrollment code")
	}
	all, err := s.keysLocked()
	if err != nil {
		return err
	}
	fingerprint := ssh.FingerprintSHA256(key.PublicKey)
	for _, existing := range all {
		if ssh.FingerprintSHA256(existing.PublicKey) == fingerprint {
			return fmt.Errorf("SSH key %s is already authorized", fingerprint)
		}
	}
	// Consume the code before publishing the key. If the second write fails, the
	// operator retrieves the new code and retries; a successful key enrollment
	// can never leave the old one-time code reusable.
	if err := s.rotateEnrollmentSecretLocked(); err != nil {
		return err
	}
	runtimeKeys, err := s.runtimeKeysLocked()
	if err != nil {
		return err
	}
	runtimeKeys = append(runtimeKeys, key)
	return writeSecureFile(s.path, marshalSSHAuthorizedKeys(runtimeKeys))
}

func (s *sshKeyStore) delete(fingerprint string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.writable() {
		return errSSHKeyStoreReadOnly
	}
	for _, key := range s.configured {
		if ssh.FingerprintSHA256(key.PublicKey) == fingerprint {
			return fmt.Errorf("SSH key %s comes from %s and cannot be deleted through the API", fingerprint, authorizedKeysEnv)
		}
	}
	keys, err := s.runtimeKeysLocked()
	if err != nil {
		return err
	}
	out := keys[:0]
	found := false
	for _, key := range keys {
		if ssh.FingerprintSHA256(key.PublicKey) == fingerprint {
			found = true
			continue
		}
		out = append(out, key)
	}
	if !found {
		return os.ErrNotExist
	}
	return writeSecureFile(s.path, marshalSSHAuthorizedKeys(out))
}

func (s *sshKeyStore) runtimeKeysLocked() ([]sshAuthorizedKey, error) {
	raw, err := readSecureRegularFile(s.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return parseSSHAuthorizedKeys(raw)
}

func (s *sshKeyStore) rotateEnrollmentSecretLocked() error {
	secret, err := newEnrollmentSecret()
	if err != nil {
		return err
	}
	if err := writeSecureFile(s.enrollmentPath, []byte(secret+"\n")); err != nil {
		return err
	}
	s.enrollmentSecret = secret
	return nil
}

func deduplicateSSHKeys(keys []sshAuthorizedKey) []sshAuthorizedKey {
	seen := map[string]bool{}
	out := make([]sshAuthorizedKey, 0, len(keys))
	for _, key := range keys {
		fingerprint := ssh.FingerprintSHA256(key.PublicKey)
		if seen[fingerprint] {
			continue
		}
		seen[fingerprint] = true
		out = append(out, key)
	}
	return out
}

func parseSSHAuthorizedKeys(raw []byte) ([]sshAuthorizedKey, error) {
	var keys []sshAuthorizedKey
	for len(strings.TrimSpace(string(raw))) > 0 {
		publicKey, _, options, rest, err := ssh.ParseAuthorizedKey(raw)
		if err != nil {
			return nil, err
		}
		entry := sshAuthorizedKey{PublicKey: publicKey}
		for _, option := range options {
			key, value, ok := strings.Cut(option, "=")
			if !ok {
				continue
			}
			if unquoted, err := strconv.Unquote(value); err == nil {
				value = unquoted
			}
			switch key {
			case "cornus-name":
				entry.Name = value
			case "cornus-enrolled":
				entry.Enrolled, err = time.Parse(time.RFC3339, value)
				if err != nil {
					return nil, fmt.Errorf("invalid cornus-enrolled value %q: %w", value, err)
				}
			}
		}
		keys = append(keys, entry)
		raw = rest
	}
	return keys, nil
}

func marshalSSHAuthorizedKeys(keys []sshAuthorizedKey) []byte {
	var out strings.Builder
	for _, key := range keys {
		var options []string
		if key.Name != "" {
			options = append(options, "cornus-name="+strconv.Quote(key.Name))
		}
		if !key.Enrolled.IsZero() {
			options = append(options, "cornus-enrolled="+strconv.Quote(key.Enrolled.UTC().Format(time.RFC3339)))
		}
		if len(options) > 0 {
			out.WriteString(strings.Join(options, ","))
			out.WriteByte(' ')
		}
		out.Write(ssh.MarshalAuthorizedKey(key.PublicKey))
	}
	return []byte(out.String())
}

func loadOrCreateEnrollmentSecret(path string) (string, error) {
	raw, err := readSecureRegularFile(path)
	if err == nil {
		secret := strings.TrimSpace(string(raw))
		if secret == "" {
			return "", fmt.Errorf("enrollment secret %s is empty", path)
		}
		return secret, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	secret, err := newEnrollmentSecret()
	if err != nil {
		return "", err
	}
	if err := writeSecureFile(path, []byte(secret+"\n")); err != nil {
		return "", err
	}
	return secret, nil
}

func newEnrollmentSecret() (string, error) {
	random := make([]byte, enrollmentSecretBytes)
	if _, err := rand.Read(random); err != nil {
		return "", fmt.Errorf("generate enrollment secret: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(random), nil
}

func readSecureRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("SSH auth file %s is not a regular file", path)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("SSH auth file %s has insecure permissions %04o; want 0600 or stricter", path, info.Mode().Perm())
	}
	return os.ReadFile(path)
}

func writeSecureFile(path string, raw []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
