// Package testaccounts keeps each project's test logins (HELPERS_PLAN.md,
// H3): logins for the user's own app that work only in its preview, a test
// copy with test data. Commitarium makes the username and a strong password;
// the preview receives the list as a read-only file and the app seeds them.
//
// The list is a secret: it lives beside the project's other credentials,
// never in the repository, the chat, or a log. Listing leaves the passwords
// out; only Password (the Settings copy button) and Preview (the host
// starting a preview) answer them.
package testaccounts

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// Domain is every test login's email domain. The .test top-level domain is
// reserved and never resolves, so mail to these logins goes nowhere.
const Domain = "commitarium.test"

// PreviewPath is where the preview finds the list, named by the
// COMMITARIUM_TEST_ACCOUNTS variable in every preview container.
const PreviewPath = "/run/commitarium/test-accounts.json"

const (
	// MaxAccounts is how many test logins a project keeps.
	MaxAccounts = 50
	maxLabel    = 60
	maxRole     = 40
	maxNote     = 300
	maxSlug     = 40
	// passwordLength letters and digits give about 138 bits.
	passwordLength = 24
)

var (
	ErrInvalid     = errors.New("invalid test account")
	ErrNotFound    = errors.New("no such test account")
	ErrTooMany     = errors.New("too many test accounts")
	ErrUnavailable = errors.New("test accounts are unavailable")

	projectPattern = regexp.MustCompile(`^prj_[A-Za-z0-9]{1,64}$`)
	notSlug        = regexp.MustCompile(`[^a-z0-9]+`)
)

// Account is one test login as the store keeps it.
type Account struct {
	Label     string    `json:"label"`
	Username  string    `json:"username"`
	Password  string    `json:"password"`
	Role      string    `json:"role"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

// Summary is a test login without its password, as Settings lists it and
// Mayka sees it.
type Summary struct {
	Label     string    `json:"label"`
	Username  string    `json:"username"`
	Role      string    `json:"role"`
	Note      string    `json:"note"`
	CreatedAt time.Time `json:"created_at"`
}

func (account Account) summary() Summary {
	return Summary{Label: account.Label, Username: account.Username, Role: account.Role,
		Note: account.Note, CreatedAt: account.CreatedAt}
}

// PreviewAccount is one login in the preview's file.
type PreviewAccount struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Role     string `json:"role"`
	Label    string `json:"label"`
}

// PreviewFile is what the preview's app reads at COMMITARIUM_TEST_ACCOUNTS:
// every login to create or update, and the domain whose logins it removes
// when they are no longer listed.
type PreviewFile struct {
	Accounts []PreviewAccount `json:"accounts"`
	Domain   string           `json:"domain"`
}

// Store keeps one file per project, readable by the coordinator alone.
type Store struct {
	root string
	now  func() time.Time
	mu   sync.Mutex
}

// NewStore keeps the lists under root, a directory of credentials that
// backups leave out.
func NewStore(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("%w: create the test accounts directory", ErrUnavailable)
	}
	return &Store{root: root, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (store *Store) path(projectID string) (string, error) {
	if !projectPattern.MatchString(projectID) {
		return "", fmt.Errorf("%w: unknown project", ErrInvalid)
	}
	return filepath.Join(store.root, projectID+".json"), nil
}

// read answers a project's logins. Callers hold mu. Errors never carry the
// file's contents.
func (store *Store) read(projectID string) ([]Account, error) {
	path, err := store.path(projectID)
	if err != nil {
		return nil, err
	}
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrUnavailable
	}
	var accounts []Account
	if err := json.Unmarshal(contents, &accounts); err != nil {
		return nil, ErrUnavailable
	}
	return accounts, nil
}

// write replaces a project's file atomically, 0600. Callers hold mu.
func (store *Store) write(projectID string, accounts []Account) error {
	path, err := store.path(projectID)
	if err != nil {
		return err
	}
	if len(accounts) == 0 {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return ErrUnavailable
		}
		return nil
	}
	contents, err := json.MarshalIndent(accounts, "", "  ")
	if err != nil {
		return ErrUnavailable
	}
	temporary, err := os.CreateTemp(store.root, ".test-accounts-*.tmp")
	if err != nil {
		return ErrUnavailable
	}
	defer os.Remove(temporary.Name())
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return ErrUnavailable
	}
	if _, err := temporary.Write(contents); err != nil {
		temporary.Close()
		return ErrUnavailable
	}
	if err := temporary.Close(); err != nil {
		return ErrUnavailable
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return ErrUnavailable
	}
	return nil
}

// List answers a project's logins without their passwords, oldest first.
func (store *Store) List(_ context.Context, projectID string) ([]Summary, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	accounts, err := store.read(projectID)
	if err != nil {
		return nil, err
	}
	summaries := make([]Summary, 0, len(accounts))
	for _, account := range accounts {
		summaries = append(summaries, account.summary())
	}
	return summaries, nil
}

// Add makes a login for label: its username is the label's slug at Domain,
// numbered when taken, and its password is new and random.
func (store *Store) Add(_ context.Context, projectID, label, role, note string) (Summary, error) {
	label, role, note = strings.TrimSpace(label), strings.TrimSpace(role), strings.TrimSpace(note)
	if label == "" || !fits(label, maxLabel) || !fits(role, maxRole) || !fits(note, maxNote) {
		return Summary{}, ErrInvalid
	}
	password, err := newPassword()
	if err != nil {
		return Summary{}, ErrUnavailable
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	accounts, err := store.read(projectID)
	if err != nil {
		return Summary{}, err
	}
	if len(accounts) >= MaxAccounts {
		return Summary{}, ErrTooMany
	}
	account := Account{
		Label: label, Username: uniqueUsername(slug(label), accounts), Password: password,
		Role: role, Note: note, CreatedAt: store.now(),
	}
	if err := store.write(projectID, append(accounts, account)); err != nil {
		return Summary{}, err
	}
	return account.summary(), nil
}

// Update changes a login's role or note; nil leaves one as it is.
func (store *Store) Update(_ context.Context, projectID, username string, role, note *string) (Summary, error) {
	if role != nil {
		trimmed := strings.TrimSpace(*role)
		if !fits(trimmed, maxRole) {
			return Summary{}, ErrInvalid
		}
		role = &trimmed
	}
	if note != nil {
		trimmed := strings.TrimSpace(*note)
		if !fits(trimmed, maxNote) {
			return Summary{}, ErrInvalid
		}
		note = &trimmed
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	accounts, err := store.read(projectID)
	if err != nil {
		return Summary{}, err
	}
	index := slices.IndexFunc(accounts, func(account Account) bool { return account.Username == username })
	if index < 0 {
		return Summary{}, ErrNotFound
	}
	if role != nil {
		accounts[index].Role = *role
	}
	if note != nil {
		accounts[index].Note = *note
	}
	if err := store.write(projectID, accounts); err != nil {
		return Summary{}, err
	}
	return accounts[index].summary(), nil
}

// Remove forgets a login; the app removes it from the preview on its next
// start.
func (store *Store) Remove(_ context.Context, projectID, username string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	accounts, err := store.read(projectID)
	if err != nil {
		return err
	}
	index := slices.IndexFunc(accounts, func(account Account) bool { return account.Username == username })
	if index < 0 {
		return ErrNotFound
	}
	return store.write(projectID, slices.Delete(accounts, index, index+1))
}

// Password answers one login's password, for the user to copy.
func (store *Store) Password(_ context.Context, projectID, username string) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	accounts, err := store.read(projectID)
	if err != nil {
		return "", err
	}
	for _, account := range accounts {
		if account.Username == username {
			return account.Password, nil
		}
	}
	return "", ErrNotFound
}

// Preview answers the file a preview's app reads. A project without logins
// gets an empty list, so the app removes any it seeded before.
func (store *Store) Preview(_ context.Context, projectID string) (PreviewFile, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	accounts, err := store.read(projectID)
	if err != nil {
		return PreviewFile{}, err
	}
	file := PreviewFile{Accounts: make([]PreviewAccount, 0, len(accounts)), Domain: Domain}
	for _, account := range accounts {
		file.Accounts = append(file.Accounts, PreviewAccount{
			Username: account.Username, Password: account.Password, Role: account.Role, Label: account.Label,
		})
	}
	return file, nil
}

// DeleteProject forgets every login of a project being deleted.
func (store *Store) DeleteProject(_ context.Context, projectID string) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.write(projectID, nil)
}

// fits reports whether text is short enough and has no control characters.
func fits(text string, limit int) bool {
	return len(text) <= limit && strings.IndexFunc(text, unicode.IsControl) < 0
}

// latin spells common accented letters plainly, so "Ólöf" is "olof".
var latin = strings.NewReplacer(
	"á", "a", "à", "a", "â", "a", "ä", "a", "å", "a", "ã", "a", "æ", "ae",
	"ç", "c", "ð", "d", "é", "e", "è", "e", "ê", "e", "ë", "e",
	"í", "i", "ì", "i", "î", "i", "ï", "i", "ñ", "n",
	"ó", "o", "ò", "o", "ô", "o", "ö", "o", "ø", "o", "õ", "o", "œ", "oe",
	"ß", "ss", "þ", "th", "ú", "u", "ù", "u", "û", "u", "ü", "u", "ý", "y", "ÿ", "y",
)

// slug is the label in lowercase letters and digits joined by dashes:
// "Admin (Anna)" becomes "admin-anna". A label of other letters only is
// "user".
func slug(label string) string {
	// A label typed as an email address names the person by its first part:
	// "einar@example.is" becomes einar, not einar-example-is.
	if local, _, found := strings.Cut(label, "@"); found && strings.TrimSpace(local) != "" {
		label = local
	}
	plain := latin.Replace(strings.ToLower(label))
	slugged := strings.Trim(notSlug.ReplaceAllString(plain, "-"), "-")
	if len(slugged) > maxSlug {
		slugged = strings.TrimRight(slugged[:maxSlug], "-")
	}
	if slugged == "" {
		return "user"
	}
	return slugged
}

// uniqueUsername is base@Domain, or base-2@Domain and up when taken.
func uniqueUsername(base string, accounts []Account) string {
	taken := func(username string) bool {
		return slices.ContainsFunc(accounts, func(account Account) bool { return account.Username == username })
	}
	username := base + "@" + Domain
	for number := 2; taken(username); number++ {
		username = base + "-" + strconv.Itoa(number) + "@" + Domain
	}
	return username
}

const passwordAlphabet = "ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789"

// newPassword is random letters and digits, without look-alikes: nothing a
// sign-in form, a shell, or JSON treats specially.
func newPassword() (string, error) {
	var password strings.Builder
	limit := byte(256 - 256%len(passwordAlphabet))
	buffer := make([]byte, 64)
	for password.Len() < passwordLength {
		if _, err := rand.Read(buffer); err != nil {
			return "", err
		}
		for _, value := range buffer {
			if value < limit && password.Len() < passwordLength {
				password.WriteByte(passwordAlphabet[int(value)%len(passwordAlphabet)])
			}
		}
	}
	return password.String(), nil
}
