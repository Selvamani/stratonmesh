package gitops

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/selvamani/stratonmesh/pkg/importer"
	"github.com/selvamani/stratonmesh/pkg/store"
	"go.uber.org/zap"
)

// PushEvent is a normalised push notification from GitHub or GitLab.
type PushEvent struct {
	Provider   string   // "github" | "gitlab"
	Repository string   // "owner/repo"
	Branch     string   // "main"
	CommitSHA  string
	CommitMsg  string
	PushedBy   string
	ChangedFiles []string
}

// SyncConfig describes a blueprint that should be continuously synced from Git.
type SyncConfig struct {
	BlueprintName string        `yaml:"blueprintName" json:"blueprintName"`
	GitURL        string        `yaml:"gitUrl" json:"gitUrl"`
	Branch        string        `yaml:"branch" json:"branch"`
	Path          string        `yaml:"path,omitempty" json:"path,omitempty"`
	// AutoDeploy, if non-empty, triggers a deployment to the given environment on sync.
	AutoDeploy    string        `yaml:"autoDeploy,omitempty" json:"autoDeploy,omitempty"`
	// PollInterval overrides the default sync polling interval.
	PollInterval  time.Duration `yaml:"pollInterval,omitempty" json:"pollInterval,omitempty"`
}

// Syncer is the GitOps engine. It provides:
//  1. A webhook receiver (POST /webhook/github, POST /webhook/gitlab) that
//     triggers immediate re-import on push events.
//  2. A poll loop that periodically checks for upstream Git changes (drift detection).
type Syncer struct {
	store        *store.Store
	importer     *importer.Importer
	logger       *zap.SugaredLogger
	webhookSecret string
	configs       []SyncConfig
	mux           *http.ServeMux
}

// Config holds GitOps settings.
type Config struct {
	// WebhookSecret is the HMAC-SHA256 secret for verifying GitHub/GitLab payloads.
	WebhookSecret string `yaml:"webhookSecret" json:"webhookSecret"`
	// DefaultPollInterval is how often all blueprints are re-checked (default 5m).
	DefaultPollInterval time.Duration `yaml:"defaultPollInterval" json:"defaultPollInterval"`
}

// New creates a Syncer.
func New(st *store.Store, imp *importer.Importer, cfg Config, logger *zap.SugaredLogger) *Syncer {
	s := &Syncer{
		store:         st,
		importer:      imp,
		logger:        logger,
		webhookSecret: cfg.WebhookSecret,
		mux:           http.NewServeMux(),
	}
	s.mux.HandleFunc("/webhook/github", s.handleGitHub)
	s.mux.HandleFunc("/webhook/gitlab", s.handleGitLab)
	s.mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	return s
}

// Register adds a blueprint to the continuous sync list.
func (s *Syncer) Register(cfg SyncConfig) {
	s.configs = append(s.configs, cfg)
	s.logger.Infow("gitops: registered sync", "blueprint", cfg.BlueprintName, "git", cfg.GitURL, "branch", cfg.Branch)
}

// Handler returns the HTTP handler for the webhook receiver.
func (s *Syncer) Handler() http.Handler { return s.mux }

// StartPollLoop begins periodic sync for all registered blueprints.
// Blocks until ctx is cancelled.
func (s *Syncer) StartPollLoop(ctx context.Context, defaultInterval time.Duration) {
	if defaultInterval <= 0 {
		defaultInterval = 5 * time.Minute
	}
	go func() {
		ticker := time.NewTicker(defaultInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.syncAll(ctx)
			case <-ctx.Done():
				return
			}
		}
	}()
	s.logger.Infow("gitops poll loop started", "interval", defaultInterval)
}

// --- Webhook handlers ---

func (s *Syncer) handleGitHub(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read error", http.StatusBadRequest)
		return
	}

	// Verify HMAC signature (X-Hub-Signature-256 header)
	if s.webhookSecret != "" {
		sig := r.Header.Get("X-Hub-Signature-256")
		if !verifyGitHubSig(body, s.webhookSecret, sig) {
			s.logger.Warnw("github webhook: invalid signature", "remote", r.RemoteAddr)
			http.Error(w, "invalid signature", http.StatusUnauthorized)
			return
		}
	}

	event := r.Header.Get("X-GitHub-Event")
	if event != "push" {
		// Acknowledge but ignore non-push events
		w.WriteHeader(http.StatusOK)
		return
	}

	var payload struct {
		Ref        string `json:"ref"`
		Repository struct {
			FullName string `json:"full_name"`
			CloneURL string `json:"clone_url"`
		} `json:"repository"`
		HeadCommit struct {
			ID      string   `json:"id"`
			Message string   `json:"message"`
			Added   []string `json:"added"`
			Modified []string `json:"modified"`
			Removed []string `json:"removed"`
			Author struct {
				Name string `json:"name"`
			} `json:"author"`
		} `json:"head_commit"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	branch := strings.TrimPrefix(payload.Ref, "refs/heads/")
	push := PushEvent{
		Provider:   "github",
		Repository: payload.Repository.FullName,
		Branch:     branch,
		CommitSHA:  payload.HeadCommit.ID,
		CommitMsg:  payload.HeadCommit.Message,
		PushedBy:   payload.HeadCommit.Author.Name,
	}
	push.ChangedFiles = append(push.ChangedFiles, payload.HeadCommit.Added...)
	push.ChangedFiles = append(push.ChangedFiles, payload.HeadCommit.Modified...)

	go s.onPush(r.Context(), push, payload.Repository.CloneURL)
	w.WriteHeader(http.StatusAccepted)
}

func (s *Syncer) handleGitLab(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	// GitLab uses a token header instead of HMAC
	if s.webhookSecret != "" {
		token := r.Header.Get("X-Gitlab-Token")
		if token != s.webhookSecret {
			s.logger.Warnw("gitlab webhook: invalid token", "remote", r.RemoteAddr)
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
	}

	event := r.Header.Get("X-Gitlab-Event")
	if event != "Push Hook" {
		w.WriteHeader(http.StatusOK)
		return
	}

	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var payload struct {
		Ref        string `json:"ref"`
		UserName   string `json:"user_name"`
		Repository struct {
			GitHTTPURL string `json:"git_http_url"`
			Name       string `json:"name"`
			PathWithNamespace string `json:"path_with_namespace"`
		} `json:"repository"`
		Commits []struct {
			ID      string   `json:"id"`
			Message string   `json:"message"`
			Added   []string `json:"added"`
			Modified []string `json:"modified"`
		} `json:"commits"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	branch := strings.TrimPrefix(payload.Ref, "refs/heads/")
	push := PushEvent{
		Provider:   "gitlab",
		Repository: payload.Repository.PathWithNamespace,
		Branch:     branch,
		PushedBy:   payload.UserName,
	}
	if len(payload.Commits) > 0 {
		push.CommitSHA = payload.Commits[0].ID
		push.CommitMsg = payload.Commits[0].Message
		push.ChangedFiles = append(payload.Commits[0].Added, payload.Commits[0].Modified...)
	}

	go s.onPush(r.Context(), push, payload.Repository.GitHTTPURL)
	w.WriteHeader(http.StatusAccepted)
}

// --- Sync logic ---

func (s *Syncer) onPush(ctx context.Context, push PushEvent, cloneURL string) {
	s.logger.Infow("gitops: push received",
		"provider", push.Provider,
		"repo", push.Repository,
		"branch", push.Branch,
		"sha", push.CommitSHA[:min(8, len(push.CommitSHA))],
		"by", push.PushedBy,
	)

	// Find matching sync configs
	for _, cfg := range s.configs {
		if !gitURLMatches(cfg.GitURL, cloneURL) {
			continue
		}
		if cfg.Branch != "" && cfg.Branch != push.Branch {
			continue
		}
		s.syncBlueprint(ctx, cfg, push.CommitSHA)
	}
}

func (s *Syncer) syncAll(ctx context.Context) {
	for _, cfg := range s.configs {
		s.syncBlueprint(ctx, cfg, "")
	}
}

func (s *Syncer) syncBlueprint(ctx context.Context, cfg SyncConfig, sha string) {
	s.logger.Infow("gitops: syncing blueprint", "blueprint", cfg.BlueprintName, "git", cfg.GitURL)

	result, err := s.importer.Import(ctx, importer.ImportRequest{
		GitURL: cfg.GitURL,
		Branch: cfg.Branch,
		Path:   cfg.Path,
		Name:   cfg.BlueprintName,
	})
	if err != nil {
		s.logger.Errorw("gitops: import failed", "blueprint", cfg.BlueprintName, "error", err)
		return
	}

	s.logger.Infow("gitops: blueprint synced",
		"blueprint", cfg.BlueprintName,
		"version", result.Blueprint.Version,
		"services", result.Services,
	)
}

// --- Helpers ---

func verifyGitHubSig(body []byte, secret, signature string) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	sig, err := hex.DecodeString(strings.TrimPrefix(signature, "sha256="))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	expected := mac.Sum(nil)
	return hmac.Equal(expected, sig)
}

func gitURLMatches(cfgURL, pushURL string) bool {
	// Normalise both URLs: strip .git suffix, lowercase
	norm := func(u string) string {
		u = strings.ToLower(u)
		u = strings.TrimSuffix(u, ".git")
		u = strings.TrimSuffix(u, "/")
		return u
	}
	return norm(cfgURL) == norm(pushURL)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// SyncStatus reports the current sync state of a blueprint.
type SyncStatus struct {
	BlueprintName string    `json:"blueprintName"`
	GitURL        string    `json:"gitUrl"`
	Branch        string    `json:"branch"`
	LastSynced    time.Time `json:"lastSynced"`
	LastSHA       string    `json:"lastSha"`
	Status        string    `json:"status"` // ok, error
	Error         string    `json:"error,omitempty"`
}

// StatusAll returns sync status for all registered configs.
func (s *Syncer) StatusAll(ctx context.Context) []SyncStatus {
	var out []SyncStatus
	for _, cfg := range s.configs {
		bp, err := s.store.GetBlueprint(ctx, cfg.BlueprintName)
		ss := SyncStatus{
			BlueprintName: cfg.BlueprintName,
			GitURL:        cfg.GitURL,
			Branch:        cfg.Branch,
			Status:        "ok",
		}
		if err != nil {
			ss.Status = "error"
			ss.Error = err.Error()
		} else if bp != nil {
			ss.LastSynced = bp.UpdatedAt
			ss.LastSHA = bp.GitSHA
		}
		out = append(out, ss)
	}
	return out
}
