package product

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sepayvn/sepay-cli/internal/config"

	"github.com/zalando/go-keyring"
	"gopkg.in/yaml.v3"
)

// knownProducts drives eviction across the product × env matrix: the keychain cannot be listed by prefix.
var knownProducts = []string{PG, EInvoice, BankHub}

const keyringService = "sepay"

// ProductsFile is the product-credential cache file inside the config directory.
const ProductsFile = "products.yml"

// ErrNotCached means no credential is stored for the (profile, product, env).
var ErrNotCached = errors.New("no cached product credential")

// ErrKeychainUnavailable means the OS keychain refused a write and file storage was not chosen.
// Credentials never fall back to the plaintext file on their own.
var ErrKeychainUnavailable = errors.New("could not store the product credential in the OS keychain")

// keychain lets tests fail Get and Delete independently, which go-keyring's mock cannot.
type keychain interface {
	Get(service, key string) (string, error)
	Set(service, key, value string) error
	Delete(service, key string) error
}

type osKeychain struct{}

func (osKeychain) Get(service, key string) (string, error) { return keyring.Get(service, key) }
func (osKeychain) Set(service, key, value string) error    { return keyring.Set(service, key, value) }
func (osKeychain) Delete(service, key string) error        { return keyring.Delete(service, key) }

// Store persists product credentials in the OS keychain, or a 0600 products.yml when file storage
// was chosen (--insecure-storage, or a profile signed in that way).
type Store struct {
	dir string
	kr  keychain
}

// NewStore returns a Store persisting under the given config directory.
func NewStore(dir string) *Store { return &Store{dir: dir, kr: osKeychain{}} }

func cacheKey(profile, prod, env string) string {
	return fmt.Sprintf("product:%s:%s:%s", profile, prod, env)
}

// Get returns the cached credential or ErrNotCached.
func (s *Store) Get(profile, prod, env string) (*Credential, error) {
	key := cacheKey(profile, prod, env)

	// A broken keychain falls through to the file.
	if data, err := s.kr.Get(keyringService, key); err == nil {
		var c Credential
		if uerr := json.Unmarshal([]byte(data), &c); uerr == nil {
			return &c, nil
		}
	}

	doc, err := s.readFile()
	if err != nil {
		return nil, err
	}
	if c := doc.Credentials[key]; c != nil {
		return c, nil
	}
	return nil, ErrNotCached
}

// Set stores c in the keychain, or in the 0600 file when insecure; usedFile reports the file.
// A keychain failure without insecure returns ErrKeychainUnavailable and writes nothing.
func (s *Store) Set(profile, prod, env string, c *Credential, insecure bool) (usedFile bool, err error) {
	key := cacheKey(profile, prod, env)

	if insecure {
		// Get reads the keychain first, so a leftover entry would shadow the file write.
		if derr := s.kr.Delete(keyringService, key); derr != nil && !errors.Is(derr, keyring.ErrNotFound) {
			if _, gerr := s.kr.Get(keyringService, key); gerr == nil {
				return true, fmt.Errorf("a keychain copy of the %s %s credential could not be removed and would shadow the new value: %w", prod, env, derr)
			}
		}
		return true, s.writeFile(key, c)
	}

	data, err := json.Marshal(c)
	if err != nil {
		return false, err
	}
	if err := s.kr.Set(keyringService, key, string(data)); err != nil {
		return false, fmt.Errorf("%w (%v); to keep it in a 0600 file instead, run: sepay login --insecure-storage", ErrKeychainUnavailable, err)
	}
	_ = s.clearFile(key) // drop any stale plaintext copy
	return false, nil
}

// EvictProfile removes every cached product credential for profile from both stores, so a reused
// label can't resurface a prior tenant's. A non-nil error means one may still be resolvable.
func (s *Store) EvictProfile(profile string) error {
	var keyErr error
	for _, prod := range knownProducts {
		for _, env := range []string{config.EnvSandbox, config.EnvProduction} {
			err := s.kr.Delete(keyringService, cacheKey(profile, prod, env))
			if err != nil && !errors.Is(err, keyring.ErrNotFound) && keyErr == nil {
				keyErr = fmt.Errorf("keychain: %w", err)
			}
		}
	}
	prefix := fmt.Sprintf("product:%s:", profile)
	fileErr := s.underLock(func() error {
		doc, err := s.readFile()
		if err != nil {
			return err
		}
		changed := false
		for k := range doc.Credentials {
			if strings.HasPrefix(k, prefix) {
				delete(doc.Credentials, k)
				changed = true
			}
		}
		if !changed {
			return nil
		}
		return s.saveFile(doc)
	})

	return errors.Join(keyErr, fileErr)
}

type productsDoc struct {
	Credentials map[string]*Credential `yaml:"credentials"`
}

func (s *Store) path() string { return filepath.Join(s.dir, ProductsFile) }

func (s *Store) readFile() (*productsDoc, error) {
	doc := &productsDoc{Credentials: map[string]*Credential{}}
	data, err := os.ReadFile(s.path())
	if errors.Is(err, os.ErrNotExist) {
		return doc, nil
	}
	if err != nil {
		return nil, err
	}
	if err := yaml.Unmarshal(data, doc); err != nil {
		return nil, fmt.Errorf("could not read %s: %w", ProductsFile, err)
	}
	if doc.Credentials == nil {
		doc.Credentials = map[string]*Credential{}
	}
	return doc, nil
}

func (s *Store) writeFile(key string, c *Credential) error {
	return s.underLock(func() error {
		doc, err := s.readFile()
		if err != nil {
			return err
		}
		doc.Credentials[key] = c
		return s.saveFile(doc)
	})
}

func (s *Store) clearFile(key string) error {
	return s.underLock(func() error {
		doc, err := s.readFile()
		if err != nil {
			return err
		}
		if _, ok := doc.Credentials[key]; !ok {
			return nil
		}
		delete(doc.Credentials, key)
		return s.saveFile(doc)
	})
}

// underLock serializes products.yml writes across sepay processes.
func (s *Store) underLock(fn func() error) error {
	release, err := config.LockDir(s.dir)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

func (s *Store) saveFile(doc *productsDoc) error {
	data, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(s.dir, ProductsFile, data)
}
