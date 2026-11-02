package product

import "context"

// Manager returns a cached product credential for one profile/env, minting when missing or expired.
type Manager struct {
	broker  *Broker
	store   *Store
	profile string
	env     string

	insecure bool
	usedFile bool
}

// NewManager ties a Broker and Store to one profile/env; an empty profile caches as "default".
func NewManager(broker *Broker, store *Store, profile, env string) *Manager {
	if profile == "" {
		profile = "default"
	}
	return &Manager{broker: broker, store: store, profile: profile, env: env}
}

// SetInsecure chooses file storage for the next mint; without it a keychain failure is an error.
func (m *Manager) SetInsecure(v bool) { m.insecure = v }

// UsedFile reports whether the last mint was stored in the 0600 file.
func (m *Manager) UsedFile() bool { return m.usedFile }

// Env returns the environment this manager brokers for.
func (m *Manager) Env() string { return m.env }

// Get returns a usable credential for prod, minting one if needed.
func (m *Manager) Get(ctx context.Context, prod string) (*Credential, error) {
	if c, err := m.store.Get(m.profile, prod, m.env); err == nil && !c.Expired() {
		return c, nil
	}
	return m.mint(ctx, prod)
}

// Refresh always mints and re-caches a fresh credential.
func (m *Manager) Refresh(ctx context.Context, prod string) (*Credential, error) {
	return m.mint(ctx, prod)
}

func (m *Manager) mint(ctx context.Context, prod string) (*Credential, error) {
	c, err := m.broker.Mint(ctx, prod, m.env)
	if err != nil {
		return nil, err
	}
	usedFile, err := m.store.Set(m.profile, prod, m.env, c, m.insecure)
	if err != nil {
		return nil, err
	}
	m.usedFile = usedFile
	return c, nil
}
