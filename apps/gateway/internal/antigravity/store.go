package antigravity

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	DefaultCacheTTL    = 5 * time.Minute
	AgyQuotaBinary     = "agy-quota"
	DefaultCooldownSec = 300
	MaxOAuthAccounts   = 3
)

func AccountSocketPath(directory, accountID string) string {
	return filepath.Join(directory, accountID, accountID+".sock")
}

var ErrAccountLimit = errors.New("tenant already has 3 OAuth account slots")

type Account struct {
	ID            string     `json:"account_id"`
	TenantID      string     `json:"tenant_id"`
	Label         string     `json:"label"`
	Enabled       bool       `json:"enabled"`
	Tier          string     `json:"tier"`
	LastUsed      time.Time  `json:"last_used"`
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
}

type Config struct {
	DefaultModel  string `json:"default_model"`
	DefaultEffort string `json:"default_effort"`
	MaxConcurrent int    `json:"max_concurrent"`
	AccountCount  int    `json:"account_count"`
}

type QuotaWindow struct {
	RemainingPercent float64    `json:"remaining_percent"`
	ResetTime        *time.Time `json:"reset_time,omitempty"`
	IsExhausted      bool       `json:"is_exhausted"`
}

type QuotaGroup struct {
	Name     string      `json:"name"`
	Models   []string    `json:"models"`
	Weekly   QuotaWindow `json:"weekly"`
	FiveHour QuotaWindow `json:"five_hour"`
}

type QuotaAccount struct {
	AccountID   string       `json:"account_id"`
	Email       string       `json:"email"`
	Tier        string       `json:"tier"`
	Status      string       `json:"status"`
	Groups      []QuotaGroup `json:"groups"`
	LastRefresh time.Time    `json:"last_refresh"`
}

type QuotaSummary struct {
	Accounts  []QuotaAccount `json:"accounts"`
	FetchedAt time.Time      `json:"fetched_at"`
}

type Store struct {
	mu                sync.RWMutex
	accounts          map[string]*Account
	nextAccountNumber int
	config            Config
	quotaCache        *QuotaSummary
	quotaCacheAt      time.Time
	quotaCacheTTL     time.Duration
	dataDir           string
}

var (
	globalStore     *Store
	globalStoreOnce sync.Once
)

func GetStore() *Store {
	globalStoreOnce.Do(func() {
		globalStore = NewStore("")
	})
	return globalStore
}

func NewStore(dataDir string) *Store {
	if dataDir == "" {
		dataDir = strings.TrimSpace(os.Getenv("UBAG_ANTIGRAVITY_DATA_DIR"))
	}
	if dataDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		dataDir = filepath.Join(home, ".ubag", "antigravity")
	}
	_ = os.MkdirAll(dataDir, 0o755)

	s := &Store{
		accounts:          make(map[string]*Account),
		nextAccountNumber: 1,
		config:            Config{DefaultModel: "gemini-3.8-flash", DefaultEffort: "high", MaxConcurrent: 3, AccountCount: 0},
		quotaCacheTTL:     DefaultCacheTTL,
		dataDir:           dataDir,
	}
	s.loadFromDisk()
	return s
}

func (s *Store) loadFromDisk() {
	data, err := os.ReadFile(filepath.Join(s.dataDir, "accounts.json"))
	if err != nil {
		return
	}
	var stored struct {
		Accounts          []Account `json:"accounts"`
		NextAccountNumber int       `json:"next_account_number"`
	}
	if err := json.Unmarshal(data, &stored); err != nil {
		if err := json.Unmarshal(data, &stored.Accounts); err != nil {
			return
		}
	}
	for i := range stored.Accounts {
		account := &stored.Accounts[i]
		s.accounts[account.ID] = account
		if strings.HasPrefix(account.ID, "acct_") {
			if number, err := strconv.Atoi(strings.TrimPrefix(account.ID, "acct_")); err == nil && number >= s.nextAccountNumber {
				s.nextAccountNumber = number + 1
			}
		}
	}
	if stored.NextAccountNumber > s.nextAccountNumber {
		s.nextAccountNumber = stored.NextAccountNumber
	}
	s.config.AccountCount = len(s.accounts)
}

func (s *Store) saveToDisk() error {
	accounts := make([]Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		accounts = append(accounts, *a)
	}
	data, err := json.MarshalIndent(struct {
		Accounts          []Account `json:"accounts"`
		NextAccountNumber int       `json:"next_account_number"`
	}{accounts, s.nextAccountNumber}, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(s.dataDir, ".accounts-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), filepath.Join(s.dataDir, "accounts.json"))
}

func (s *Store) ListAccounts(tenantID string) []Account {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]Account, 0, len(s.accounts))
	for _, a := range s.accounts {
		if tenantID != "" && a.TenantID == tenantID {
			result = append(result, *a)
		}
	}
	return result
}

func (s *Store) EligibleAccounts(tenantID string, now time.Time) []Account {
	accounts := s.ListAccounts(tenantID)
	eligible := make([]Account, 0, len(accounts))
	for _, account := range accounts {
		if account.Enabled && (account.CooldownUntil == nil || !now.Before(*account.CooldownUntil)) {
			eligible = append(eligible, account)
		}
	}
	sort.Slice(eligible, func(left, right int) bool {
		if eligible[left].LastUsed.Equal(eligible[right].LastUsed) {
			return eligible[left].ID < eligible[right].ID
		}
		return eligible[left].LastUsed.Before(eligible[right].LastUsed)
	})
	return eligible
}

func (s *Store) AddAccount(tenantID, label, tier string) (*Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if tenantID == "" || label == "" {
		return nil, fmt.Errorf("tenant and label are required")
	}
	count := 0
	for _, account := range s.accounts {
		if account.TenantID == tenantID {
			count++
		}
	}
	if count >= MaxOAuthAccounts {
		return nil, ErrAccountLimit
	}
	previousNumber := s.nextAccountNumber
	for {
		if _, exists := s.accounts[fmt.Sprintf("acct_%d", s.nextAccountNumber)]; !exists {
			break
		}
		s.nextAccountNumber++
	}
	id := fmt.Sprintf("acct_%d", s.nextAccountNumber)
	s.nextAccountNumber++

	now := time.Now().UTC()
	account := &Account{
		ID:        id,
		TenantID:  tenantID,
		Label:     label,
		Enabled:   true,
		Tier:      tier,
		CreatedAt: now,
	}
	s.accounts[id] = account
	s.config.AccountCount = len(s.accounts)

	if err := s.saveToDisk(); err != nil {
		delete(s.accounts, id)
		s.config.AccountCount = len(s.accounts)
		s.nextAccountNumber = previousNumber
		return nil, err
	}
	return account, nil
}

func (s *Store) RemoveAccount(tenantID, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	account, ok := s.accounts[id]
	if !ok || tenantID == "" || account.TenantID != tenantID {
		return fmt.Errorf("account %s not found", id)
	}
	delete(s.accounts, id)
	s.config.AccountCount = len(s.accounts)
	if err := s.saveToDisk(); err != nil {
		s.accounts[id] = account
		s.config.AccountCount = len(s.accounts)
		return err
	}
	return nil
}

func (s *Store) UpdateAccount(tenantID, id string, enabled bool) (*Account, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	account, ok := s.accounts[id]
	if !ok || tenantID == "" || account.TenantID != tenantID {
		return nil, fmt.Errorf("account %s not found", id)
	}
	previousEnabled := account.Enabled
	account.Enabled = enabled
	if err := s.saveToDisk(); err != nil {
		account.Enabled = previousEnabled
		return nil, err
	}
	return account, nil
}

func (s *Store) GetConfig() Config {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config
}

func (s *Store) UpdateConfig(cfg Config) Config {
	s.mu.Lock()
	defer s.mu.Unlock()
	if cfg.DefaultModel != "" {
		s.config.DefaultModel = cfg.DefaultModel
	}
	if cfg.DefaultEffort != "" {
		s.config.DefaultEffort = cfg.DefaultEffort
	}
	if cfg.MaxConcurrent > 0 {
		s.config.MaxConcurrent = cfg.MaxConcurrent
	}
	return s.config
}

func (s *Store) GetQuotaSummary() *QuotaSummary {
	s.mu.RLock()
	if s.quotaCache != nil && time.Since(s.quotaCacheAt) < s.quotaCacheTTL {
		defer s.mu.RUnlock()
		return s.quotaCache
	}
	s.mu.RUnlock()

	summary := s.refreshQuota()

	s.mu.Lock()
	s.quotaCache = summary
	s.quotaCacheAt = time.Now()
	s.mu.Unlock()

	return summary
}

func (s *Store) RefreshQuota() *QuotaSummary {
	summary := s.refreshQuota()

	s.mu.Lock()
	s.quotaCache = summary
	s.quotaCacheAt = time.Now()
	s.mu.Unlock()

	return summary
}

func (s *Store) refreshQuota() *QuotaSummary {
	summary := &QuotaSummary{
		Accounts:  []QuotaAccount{},
		FetchedAt: time.Now().UTC(),
	}

	s.mu.RLock()
	accounts := make([]Account, 0, len(s.accounts))
	for _, account := range s.accounts {
		accounts = append(accounts, *account)
	}
	s.mu.RUnlock()
	for _, account := range accounts {
		qa := QuotaAccount{
			AccountID:   account.ID,
			Email:       account.Label,
			Tier:        account.Tier,
			Status:      "ok",
			Groups:      []QuotaGroup{},
			LastRefresh: time.Now().UTC(),
		}

		accountQuota := s.fetchQuotaForAccount(account.ID)
		if accountQuota != nil {
			qa.Groups = accountQuota.Groups
			if len(accountQuota.Groups) == 0 {
				qa.Status = "no_quota"
			}
		} else {
			qa.Status = "error"
		}

		summary.Accounts = append(summary.Accounts, qa)
	}

	return summary
}

func (s *Store) fetchQuotaForAccount(accountID string) *QuotaAccount {
	cmd := exec.Command(AgyQuotaBinary, "-c", "-d", accountID)
	output, err := cmd.Output()
	if err != nil {
		return nil
	}

	var result QuotaAccount
	if err := json.Unmarshal(output, &result); err != nil {
		return nil
	}
	return &result
}

func (s *Store) MarkAccountUsed(tenantID, accountID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.accounts[accountID]
	if !ok || tenantID == "" || account.TenantID != tenantID {
		return fmt.Errorf("account %s not found", accountID)
	}
	account.LastUsed = time.Now().UTC()
	return s.saveToDisk()
}

func (s *Store) MarkAccountExhausted(tenantID, accountID string, cooldownSec int) error {
	if cooldownSec <= 0 {
		cooldownSec = DefaultCooldownSec
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	account, ok := s.accounts[accountID]
	if !ok || tenantID == "" || account.TenantID != tenantID {
		return fmt.Errorf("account %s not found", accountID)
	}
	now := time.Now().UTC()
	account.LastUsed = now
	until := now.Add(time.Duration(cooldownSec) * time.Second)
	account.CooldownUntil = &until
	return s.saveToDisk()
}
